package server

import (
	"context"
	"fmt"
	"time"

	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/trust"
	"github.com/sebastianrcnt/atto/ui"
	"os"
)

// The inbox: job exits, timers, monitors and `atto reload` from the
// agent's shell arrive as files (package events); the server polls them
// for every loaded thread, the runtime delivers them. Waking events start
// a turn when idle, or steer the running turn; quiet ones wait for the
// next turn. While a compaction runs, a prompt or picker is open, or the
// queue is paused, they wait.

// watchInbox polls the inbox of every loaded thread.
func (s *Server) watchInbox() {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-tick.C:
		}
		s.mu.Lock()
		threads := make([]*thread, 0, len(s.threads))
		for _, t := range s.threads {
			threads = append(threads, t)
		}
		s.mu.Unlock()
		for _, t := range threads {
			t.pollInbox()
		}
	}
}

// pollInbox takes the thread's events and hands them to the lane (one
// tick of watchInbox).
func (t *thread) pollInbox() {
	if t.inboxOff.Load() {
		return
	}
	var taken []events.Event
	if t.mgd != nil && t.mgd.hold.Load() {
		// An idle agent is not woken by what arrives: its events wait in the
		// inbox for its next turn. Timers still fire into it.
		events.FireDue(t.id, time.Now())
	} else {
		taken = core.Poll(t.id)
	}
	reload, evs := events.SplitReload(taken)
	nJobs, nTimers := jobs.ActiveCount(t.id), len(events.Timers(t.id))
	if !t.do(func() { t.inboxTick(reload, evs, nJobs, nTimers) }) {
		events.Requeue(t.id, taken) // closed meanwhile: left for whoever opens it next
	}
}

func (t *thread) inboxTick(reload bool, evs []events.Event, nJobs, nTimers int) {
	if t.closing {
		events.Requeue(t.id, evs)
		return
	}
	t.setCounts(nJobs, nTimers)
	t.goal.Poll()
	t.turns.PendingEvents = append(t.turns.PendingEvents, evs...)
	t.turns.RequeueQuiet(t.id)
	if reload { // atto reload, run by the agent
		t.requestReload(true)
	}
	t.deliverEvents()
	if t.elements != nil {
		t.elements.Invalidate(ui.Match{Site: ui.Pane, ID: "atto/jobs"})
	}
	t.refreshUIStatus()
	t.goalChanged() // the time of a running turn, and reports from atto goal
	t.maybeRetire()
}

// deliverEvents hands pending events to the agent: a new turn when idle,
// a steer (after the next tool call) during a turn.
func (t *thread) deliverEvents() {
	delivery := t.turns.DeliverEvents(t.id, t.gated() || t.turns.QueuePaused || t.readOnly != "", t.runKind == "turn")
	evs := delivery.Events
	if len(evs) == 0 {
		return
	}
	for _, e := range evs {
		title := e.Title
		if title == "" {
			title = e.Text
		}
		t.publish("event", map[string]any{"title": title, "source": e.Source})
	}
	if !t.turns.Busy { // a steer reaches a running turn; idle, the user may be away
		t.notify("background_event", firstTitle(evs))
	}
	if t.turns.Busy {
		t.turns.SteerEvents(t.agent, delivery)
		return
	}
	t.beginInboxTurn(evs)
}

// firstTitle describes the first of evs (and how many more follow).
func firstTitle(evs []events.Event) string {
	title := evs[0].Title
	if title == "" {
		title = evs[0].Text
	}
	if len(evs) > 1 {
		title += fmt.Sprintf(" (+%d more)", len(evs)-1)
	}
	return title
}

// sourceReloaded is the source of the event that tells the model what an
// `atto reload` did.
const sourceReloaded = "reloaded"

// requestReload reads AGENTS files, skills, hooks, settings.json and
// models.json again: now when idle, else between the running turn's steps
// (a request in flight keeps the prompt it was sent with). forModel: the
// agent asked (atto reload) and is told the result.
func (t *thread) requestReload(forModel bool) {
	if t.elements != nil {
		t.elements.ResetBindings()
	}
	t.cancelExtensionPrompts()
	if !t.turns.Busy {
		t.reloadNow(forModel)
		return
	}
	if !forModel {
		t.notice("", "Reloading after the current step.")
	}
	id, path := t.id, t.sess.Path
	t.agent.AtBoundary(func() string { return t.reloadBetweenSteps(id, path, forModel) })
}

// reloadBetweenSteps runs on the turn's goroutine, at a step boundary (or
// after the run, if it reached none). Its result goes to the model with
// the turn's next request.
func (t *thread) reloadBetweenSteps(id, path string, forModel bool) string {
	var prev core.Loaded
	_ = t.call(func() error { prev = t.loaded; return nil })
	r, err := core.Reload(t.agent, id, path, prev)
	t.do(func() { t.applyReload(r, err) })
	if !forModel {
		return ""
	}
	return events.Format([]events.Event{{Text: reloadReport(r, err)}})
}

// reloadNow reloads while no turn runs. The model's report, if it asked,
// is delivered like any event: it starts a turn.
func (t *thread) reloadNow(forModel bool) {
	r, err := core.Reload(t.agent, t.id, t.sess.Path, t.loaded)
	t.applyReload(r, err)
	if forModel {
		t.turns.PendingEvents = append(t.turns.PendingEvents, events.Event{Source: sourceReloaded, Title: "Reload result sent to the agent", Text: reloadReport(r, err)})
	}
	t.deliverEvents()
	t.maybeSendNextQueued()
}

func reloadReport(r core.Reloaded, err error) string {
	if err != nil {
		return "Reload failed, nothing changed: " + err.Error()
	}
	return r.ForModel()
}

// applyReload takes over what a reload read and tells clients what
// changed (thread/reloaded: they read their own settings again).
func (t *thread) applyReload(r core.Reloaded, err error) {
	if err != nil {
		t.errorNotice(fmt.Errorf("reload failed, nothing changed: %w", err))
		t.publish("thread/reloaded", map[string]any{"error": err.Error()})
		return
	}
	trust.WarnProject(os.Stderr, t.cwd)
	t.models, t.hooks, t.hookSrc, t.loaded = r.Models, r.Hooks, r.HookSrc, r.Loaded
	t.showLoaded(true, r.Changes, r.PromptNote())
	t.publish("thread/reloaded", map[string]any{"context": r.Loaded, "changes": r.Changes, "promptChanged": r.PromptChanged, "note": r.PromptNote()})
	t.catalogChanged()
	t.updated()
	if t.elements != nil {
		t.elements.Invalidate(ui.Match{})
	}
	t.askMCPApprovals() // a project server the reload found
}

// isEvent reports whether a committed steer came from the inbox.
func isEvent(s string) bool { return events.IsEvent(s) }

// beginInboxTurn keeps events if a turn cannot be claimed. Requests and ticks
// normally share the lane; closing can still refuse delivery.
func (t *thread) beginInboxTurn(evs []events.Event) {
	if t.turns.Busy || t.closing {
		events.Requeue(t.id, evs)
		return
	}
	text := events.Format(evs)
	t.recordSettings()
	t.feed(transcript.Input{Text: text})
	t.start("turn", "Thinking", func(ctx context.Context, emit func(any)) error {
		return t.turns.Run(ctx, t.agent, core.TurnRequest{Text: text}, emit)
	})
}

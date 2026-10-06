package app

import (
	"context"
	"fmt"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/core/transcript"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/tui"
)

// eventBlock shows an [atto event] (job exit, timer, monitor) in the
// transcript; the model receives the full text.
type eventBlock struct {
	title string
	cache tui.RenderCache[string]
}

func (e *eventBlock) Render(width int) []string {
	return e.cache.Render(width, e.title, func() []string {
		return []string{tui.Truncate("  "+tui.FG(5, e.title), width, "…")}
	})
}

// liveSession is the session ID the inbox watcher drains; it changes on
// /clear and /resume.
var liveSession atomic.Value

func (a *App) setLiveSession(id string) { liveSession.Store(id) }

// watchInbox polls the session inbox, fires due timers, and refreshes the
// job and timer counts for the status line.
func (a *App) watchInbox() {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-a.quit:
			return
		case <-tick.C:
		}
		if s, _ := liveSession.Load().(string); s != "" {
			a.pollInbox(s)
		}
	}
}

// pollInbox takes session s's events and hands them over (one tick of
// watchInbox). Call it off the UI goroutine.
func (a *App) pollInbox(s string) {
	taken := core.Poll(s)
	reload, evs := events.SplitReload(taken)
	nJobs, nTimers := jobs.ActiveCount(s), len(events.Timers(s))
	a.ui.Do(func() {
		if s != a.sess.ID {
			events.Requeue(s, taken)
			return // session switched; leave events for when it is resumed
		}
		a.jobCount, a.timerCount = nJobs, nTimers
		a.goal.Poll()
		a.pendingEvents = append(a.pendingEvents, evs...)
		if !a.busy && !events.Wakes(a.pendingEvents) {
			// Quiet messages wait in the inbox for the next turn, which
			// takes them after its first step.
			events.Requeue(s, a.pendingEvents)
			a.pendingEvents = nil
		}
		if reload { // atto reload, run by the agent
			a.requestReload(true)
		}
		a.deliverEvents()
		a.remoteGoal() // the time of a running turn, and reports from atto goal
		a.remotePending()
	})
}

// deliverEvents hands pending events to the agent: a new turn when idle,
// a steer (after the next tool call) during a turn. While compacting, a
// picker is open or the queue is paused, they wait.
func (a *App) deliverEvents() {
	if len(a.pendingEvents) == 0 || a.modal != nil || a.queuePaused {
		return
	}
	if a.busy && a.runKind != "turn" {
		return
	}
	evs := a.pendingEvents
	a.pendingEvents = nil
	for _, e := range evs {
		title := e.Title
		if title == "" {
			title = e.Text
		}
		a.add(&eventBlock{title: title})
	}
	text := events.Format(evs)
	if !a.busy { // a steer reaches a running turn; idle, the user may be away
		a.notify("background_event", firstTitle(evs))
	}
	if a.busy {
		a.agent.Steer(text) // shown above; not a user steer
		return
	}
	a.runKind = "turn"
	a.recordSettings()
	a.tr().Event(transcript.Input{Text: text}) // shown above
	a.start("Thinking", func(ctx context.Context, emit func(any)) error {
		return a.agent.Run(ctx, text, emit)
	})
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

// isEvent reports whether a committed steer came from the inbox.
func isEvent(s string) bool { return events.IsEvent(s) }

func (a *App) cmdJobs(arg string) {
	list := jobs.List(a.sess.ID)
	if len(list) == 0 {
		a.notice("No background jobs. The agent starts them with `atto job start -- <command>`.")
		return
	}
	var lines []string
	for _, j := range list {
		st := string(j.Status)
		if j.ExitCode != nil {
			st += fmt.Sprintf(" (%d)", *j.ExitCode)
		}
		lines = append(lines, fmt.Sprintf("%-4d %-10s %-12s %-8s %s", j.ID, j.KindLabel(), st, j.Runtime(), j.Label()))
	}
	a.add(&contextBlock{lines: append([]string{tui.Bold("Background jobs") + tui.Dim("  · /stop stops all · output: atto job output <id>")}, lines...)})
}

func (a *App) cmdStop(string) {
	n := jobs.KillAll(a.sess.ID)
	a.jobCount = 0
	a.notice("Stopped %d background jobs.", n)
}

func (a *App) cmdTimers(string) {
	ts := events.Timers(a.sess.ID)
	if len(ts) == 0 {
		a.notice("No timers. Set one with /timer 10m <message>.")
		return
	}
	var lines []string
	for _, t := range ts {
		sched := ""
		if t.Recurring() {
			sched = " [" + t.Schedule() + "]"
		}
		lines = append(lines, fmt.Sprintf("%s  %s (in %s)%s  %s", t.ID, t.Due.Format("15:04"), time.Until(t.Due).Round(time.Second), sched, t.Message))
	}
	a.add(&contextBlock{lines: append([]string{tui.Bold("Timers") + tui.Dim("  · cancel: /timer cancel <id>")}, lines...)})
}

// cmdTimer: /timer 10m <message>, /timer 15:30 <message>, /timer cancel <id>.
// Recurring timers are set by the model with `atto timer every`; here they
// are listed and can be canceled.
func (a *App) cmdTimer(arg string) {
	when, msg, _ := strings.Cut(strings.TrimSpace(arg), " ")
	if when == "cancel" {
		if err := events.CancelTimer(a.sess.ID, strings.TrimSpace(msg)); err != nil {
			a.errorNotice(err)
		} else {
			a.notice("Timer canceled.")
		}
		return
	}
	if when == "" || strings.TrimSpace(msg) == "" {
		a.notice("Usage: /timer <10m|15:30> <message> — the message is sent to the agent when it fires.")
		return
	}
	due, err := events.ParseWhen(when, time.Now())
	if err != nil {
		a.errorNotice(err)
		return
	}
	t, err := events.AddTimer(a.sess.ID, due, strings.TrimSpace(msg))
	if err != nil {
		a.errorNotice(err)
		return
	}
	a.timerCount++
	a.notice("Timer %s set for %s.", t.ID, due.Format("15:04:05"))
}

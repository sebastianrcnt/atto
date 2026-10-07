package server

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/textfmt"
)

// Runs: a turn, a compaction or a branch summary. start runs fn on a
// goroutine of its own; the agent's events and the end come back to the
// lane in order. afterRun then settles what waited for the run, as the
// terminal did: shell results, a move in the tree, steers that raced with
// the end, Ctrl+Enter's message, inbox events, the queue and the goal.

// userMeta is who sent the user item an input makes (see feed).
type userMeta struct {
	client, input, group string
}

// handler is the transcript handler of the thread: items become
// notifications, completed ones are kept.
func (t *thread) handler() transcript.Handler {
	return transcript.Handler{
		Started: func(it *transcript.Item) {
			w := t.wire(it)
			if len(t.metas) > 0 {
				m := t.metas[0]
				t.metas = t.metas[1:]
				w.ClientID, w.InputID, w.SteerGroup = m.client, m.input, m.group
				t.itemMeta[it.ID] = m
			}
			t.publish("item/started", map[string]any{"turnId": t.turnID, "item": w})
		},
		Delta: func(it *transcript.Item, d string) {
			t.publish("item/delta", map[string]any{"turnId": t.turnID, "itemId": it.ID, "delta": d})
		},
		Updated: func(it *transcript.Item) {
			t.publish("item/updated", map[string]any{"turnId": t.turnID, "item": t.wire(it)})
		},
		Completed: func(it *transcript.Item) {
			w := t.blocks.attach(t.wire(it))
			if m, ok := t.itemMeta[it.ID]; ok {
				w.ClientID, w.InputID, w.SteerGroup = m.client, m.input, m.group
				delete(t.itemMeta, it.ID)
			}
			t.items = append(t.items, w)
			t.publish("item/completed", map[string]any{"turnId": t.turnID, "item": w})
		},
		Saved: func(it *transcript.Item) {
			t.blocks.saved(t.id, it)
			// Reasoning completes when the text starts, before the response
			// is saved: the kept item learns its block ID now, and clients
			// that have it too (item/updated), before any item/display.
			for i := len(t.items) - 1; i >= 0; i-- {
				if t.items[i].ID == it.ID {
					t.items[i].BlockID, t.items[i].EntryID = blockID(t.id, it), it.EntryID
					t.publish("item/updated", map[string]any{"turnId": t.turnID, "item": t.items[i]})
					break
				}
			}
		},
	}
}

// feed gives the builder ev, with metas for the user items it makes.
func (t *thread) feed(ev any, metas ...userMeta) {
	t.metas = metas
	t.tr.Event(ev)
	t.metas = nil
}

// start runs fn as the thread's run of kind, with activity as what it
// is doing at first.
func (t *thread) start(kind, activity string, fn func(context.Context, func(any)) error) {
	t.typed, t.replied = nil, false
	t.runKind = kind
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.busy, t.cancel, t.runDone = true, cancel, done
	t.runStart, t.activity, t.tools = time.Now(), activity, 0
	t.lastActive = t.runStart
	t.cancelGoalRetry() // whatever starts, a goal retry waiting is moot
	t.usage = provider.Usage{}
	t.turn = TurnInfo{StartedAt: t.runStart.UnixMilli()}
	t.turnSeq++
	t.turnID = fmt.Sprintf("%s-t%d", t.id, t.turnSeq)
	if kind == "turn" {
		t.goal.BeginTurn()
	}
	t.publish("turn/started", map[string]any{"turnId": t.turnID, "startedAt": t.turn.StartedAt, "runKind": kind, "activity": activity})
	t.goalChanged() // a goal on hold is pursued while the turn runs
	go func() {
		defer close(done)
		err := fn(ctx, func(ev any) { t.do(func() { t.onEvent(ev) }) })
		// A reload that no step boundary reached (the turn ended first, or
		// this was a compaction) runs now; its report for the model is
		// delivered like an event.
		var reported []events.Event
		for _, f := range t.agent.TakeBoundary() {
			if text := f(); text != "" {
				reported = append(reported, events.Event{Source: sourceReloaded, Title: "Reload result sent to the agent", Text: strings.TrimPrefix(text, events.Prefix)})
			}
		}
		ctxTokens := t.agent.ContextTokens() // safe: the run is over
		t.do(func() { t.finish(err, ctxTokens, reported, cancel) })
	}()
}

// finish ends a run on the lane.
func (t *thread) finish(err error, ctxTokens int, reported []events.Event, cancel context.CancelFunc) {
	t.pendingEvents = append(t.pendingEvents, reported...)
	t.tr.End() // a compaction that did not finish disappears
	t.busy, t.ctx = false, ctxTokens
	cancel()
	t.cancel = nil
	t.lastActive = time.Now()
	switch {
	case errors.Is(err, context.Canceled) && t.runKind == "branchSummary":
		t.notice("", "Branch summary canceled.")
	case errors.Is(err, context.Canceled):
		t.notice("", "Interrupted.")
	case errors.Is(err, agent.ErrPromptBlocked), errors.Is(err, agent.ErrStoppedByHook):
		// The hook's reason was already shown.
	case err != nil:
		t.errorNotice(err)
		// The model never answered: the message stays in the session
		// (sending it again adds no copy), and its text goes back to the
		// client that sent it, unless it has started on something else.
		if in := t.typed; in != nil && !t.replied {
			t.recover(in.Client, true, []string{in.Text}, in.Images)
		}
	}
	t.typed = nil
	if werr := t.sess.Err(); werr != nil {
		t.errorNotice(fmt.Errorf("saving session: %w", werr))
	}
	status, msg := "completed", ""
	switch {
	case errors.Is(err, context.Canceled):
		status = "interrupted"
	case err != nil:
		status, msg = "failed", err.Error()
	}
	params := map[string]any{"turnId": t.turnID, "status": status, "contextTokens": t.ctx, "runKind": t.runKind,
		"usage": map[string]int{"inputTokens": t.usage.PromptTokens, "cachedInputTokens": t.usage.CachedTokens, "outputTokens": t.usage.CompletionTokens}}
	if msg != "" {
		params["error"] = msg
	}
	durMs := time.Since(t.runStart).Milliseconds()
	params["durationMs"] = durMs
	t.turnID = ""
	t.publish("turn/completed", params)
	t.afterRun(err)
	t.goalChanged()
	t.pendingChanged()
	t.updated()
	t.maybeRetire()
}

// onEvent handles an event of the running agent.
func (t *thread) onEvent(ev any) {
	if e, ok := ev.(agent.SteerCommitted); ok {
		t.commitSteers(e)
		return
	}
	t.feed(ev)
	t.goal.Event(ev)
	switch ev.(type) {
	case agent.TextDelta, agent.ReasoningDelta, agent.ToolDraft, agent.ToolStart, agent.StepEnd:
		t.replied = true
	}
	t.handoffEvent(ev)
	activity := t.activity
	switch e := ev.(type) {
	case agent.ToolDraft:
		activity = "Working"
	case agent.ToolStart:
		activity = "Working"
		t.tools++
	case agent.ToolEnd:
		activity = "Thinking"
		t.tools = max(0, t.tools-1)
	case agent.TextDelta, agent.ReasoningDelta:
		if activity == "Retrying" {
			activity = "Thinking"
		}
	case agent.StepEnd:
		t.usage.PromptTokens += e.Usage.PromptTokens
		t.usage.CachedTokens += e.Usage.CachedTokens
		t.usage.CompletionTokens += e.Usage.CompletionTokens
		t.total.Add(e.Usage)
		t.turn.InputTokens += max(0, e.Usage.PromptTokens-e.Usage.CachedTokens-e.Usage.CacheWriteTokens)
		t.turn.OutputTokens += e.Usage.CompletionTokens
		t.ctx = e.Context
		t.publish("thread/usage", map[string]any{"usage": t.total, "step": StepUsage(e.Usage), "contextTokens": e.Context})
	case agent.StreamRetry:
		activity = "Retrying"
	case agent.CompactStart:
		activity = "Compacting context"
	case agent.CompactEnd:
		t.ctx = e.After
		activity = "Thinking"
		t.notice("", "Long threads and repeated compactions can make the model less accurate. Start a new conversation (/clear) when you can.")
	case agent.HookNotice:
		// Also as the notification clients had before hook items.
		t.publish("hook", map[string]any{"turnId": t.turnID, "event": e.Event, "message": e.Message, "blocked": e.Blocked})
	}
	t.setActivity(activity)
	t.goalChanged()
}

// setActivity tells clients what the run is doing when it changed.
func (t *thread) setActivity(a string) {
	if a == t.activity && t.toolsShown == t.tools {
		return
	}
	t.activity, t.toolsShown = a, t.tools
	t.publish("turn/activity", map[string]any{"turnId": t.turnID, "activity": t.activityInfo()})
}

func (t *thread) activityInfo() *Activity {
	if !t.busy {
		return nil
	}
	return &Activity{Phase: t.activity, RunKind: t.runKind, StartedAt: t.runStart.UnixMilli(), ToolsRunning: t.tools}
}

// commitSteers takes a committed steer: the user's steers it holds are no
// longer pending, and their messages show as one block.
func (t *thread) commitSteers(e agent.SteerCommitted) {
	e.User = make([]bool, len(e.Texts))
	t.steerSeq++
	group := fmt.Sprintf("%s-s%d", t.id, t.steerSeq)
	metas := make([]userMeta, len(e.Texts))
	n := 0
	for j, text := range e.Texts {
		metas[j].group = group
		if i := slices.IndexFunc(t.steers, func(p *pendingInput) bool { return p.Text == text }); i >= 0 {
			e.User[j] = true
			metas[j].client, metas[j].input = t.steers[i].Client, t.steers[i].ID
			t.steers = slices.Delete(t.steers, i, i+1)
			n++
		}
	}
	if n > 0 {
		t.goal.UserInput() // the goal waits for the user once this turn ends
	}
	t.feed(e, metas...)
	t.pendingChanged()
}

// afterRun settles pending input once a run finishes.
func (t *thread) afterRun(err error) {
	t.flushShell()
	if t.handoffAfterRun(err) {
		return
	}
	if t.runKind == "branchSummary" {
		t.runKind = ""
		if t.afterBranchSummary(err) {
			return
		}
	}
	if t.runKind == "turn" {
		t.goal.EndTurn(err)
	}
	if t.runKind == "turn" && err == nil {
		took := "<1s"
		if d := time.Since(t.runStart); d >= time.Second {
			took = textfmt.Duration(d.Truncate(100 * time.Millisecond))
		}
		t.notice("", "Worked for %s • %s", took, time.Now().Format("3:04 PM"))
		if t.goal.Held() {
			t.notice("", goalWaitingNotice)
		}
		t.notifyIdle()
	}
	t.runKind = ""
	if id := t.pendingTree; id != "" {
		t.dropSendNow()
		sum := t.pendingSummary
		t.pendingTree, t.pendingSummary = "", nil
		t.moveTo(id, sum, t.treeClient)
		return
	}
	canceled := errors.Is(err, context.Canceled)
	// Goal notes were for the turn that just ended: they never start one.
	leftover := slices.DeleteFunc(t.agent.DrainSteers(), goal.IsMessage)
	clients := t.steerOrigins(leftover)
	t.steers = nil
	sendSteers := t.sendSteers
	t.sendSteers = false
	now := t.sendNow
	t.sendNow = nil

	if now != nil {
		// Ctrl+Enter: the steers and the draft, in the order typed. A turn
		// that failed instead gives them back, as for steers.
		if err == nil || canceled {
			t.runTurn(t.newInput(now.Client, joinTexts(append(leftover, now.Text)), now.Images), false)
			return
		}
		t.recover(now.Client, false, append(leftover, now.Text), now.Images)
		leftover = nil
	}

	if len(leftover) > 0 {
		// Steers that raced with the end of the turn, or that the user asked
		// to send right away with Esc, start the next turn. Otherwise (error,
		// Ctrl+C) they go back to the editor.
		if err == nil || (canceled && sendSteers) {
			t.runTurn(t.newInput(firstClient(clients), joinTexts(leftover), nil), false)
			return
		}
		t.recover(firstClient(clients), false, leftover, nil)
	}

	if len(t.pendingEvents) > 0 && err == nil {
		t.deliverEvents()
		if t.busy {
			return
		}
	}
	if err != nil && len(t.queued) > 0 {
		t.queuePaused = true
		t.notice("", "Queued messages paused. Press enter on an empty prompt to resume, or shift+← to edit.")
		return
	}
	t.maybeSendNextQueued()
}

// steerOrigins are the clients of the user's steers among texts.
func (t *thread) steerOrigins(texts []string) []string {
	var out []string
	for _, text := range texts {
		for _, s := range t.steers {
			if s.Text == text {
				out = append(out, s.Client)
				break
			}
		}
	}
	return out
}

func firstClient(c []string) string {
	if len(c) == 0 {
		return ""
	}
	return c[0]
}

// dropSendNow gives Ctrl+Enter's message back when the turn it
// interrupted ended in a move instead.
func (t *thread) dropSendNow() {
	if n := t.sendNow; n != nil {
		t.sendNow = nil
		t.recover(n.Client, false, []string{n.Text}, n.Images)
	}
}

// notifyAfter is how long a turn must have run for atto to notify the user
// when it finishes; shorter turns end while the user is likely still looking.
var notifyAfter = 15 * time.Second

// notifyIdle sends the idle_prompt notification when a long turn has ended
// and atto now waits for the user.
func (t *thread) notifyIdle() {
	if time.Since(t.runStart) < notifyAfter {
		return
	}
	if len(t.queued) > 0 || len(t.pendingEvents) > 0 || len(t.steers) > 0 || (t.goal.Active() && !t.goal.Held()) {
		return
	}
	t.notify("idle_prompt", "atto finished and is waiting for your input")
}

// notify runs Notification hooks in the background, for when atto needs
// the user's attention (kind is the notification type).
func (t *thread) notify(kind, message string) {
	hk := t.hooks // a reload may replace t.hooks while this runs
	if hk == nil {
		return
	}
	go func() {
		notices := hk.Notification(context.Background(), kind, message)
		t.do(func() {
			for _, n := range notices {
				t.notice("", "%s", n)
			}
		})
	}()
}

// recordSettings writes model/effort entries when they changed since the
// last one, so a resumed session picks them back up.
func (t *thread) recordSettings() {
	m, e := t.agent.Current()
	if id := m.ProviderName + "/" + m.Model.ID; id != t.recModel {
		t.sess.Append(session.Entry{Type: session.TypeModel, Provider: m.ProviderName, Model: m.Model.ID})
		t.recModel = id
	}
	if e != t.recEffort {
		t.sess.Append(session.Entry{Type: session.TypeEffort, Effort: e})
		t.recEffort = e
	}
}

package server

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
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
			if len(t.attached) > 0 {
				t.startedItem(it.ID)
			}
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
			if len(t.attached) > 0 {
				t.items = append(t.items, w)
				t.trimItems()
			}
			t.publish("item/completed", map[string]any{"turnId": t.turnID, "item": w})
		},
		Saved: func(it *transcript.Item) {
			if len(t.attached) == 0 {
				if t.headlessBlocks == nil {
					t.headlessBlocks = blocks{}
				}
				t.headlessBlocks.saved(t.id, it)
				if len(t.headlessBlocks) > 4 {
					for id, b := range t.headlessBlocks {
						if b.entryID != it.EntryID {
							delete(t.headlessBlocks, id)
						}
					}
				}
				if it.Status != transcript.InProgress {
					t.publish("item/updated", map[string]any{"turnId": t.turnID, "item": t.wire(it)})
				}
				return
			}
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
	if len(t.attached) == 0 {
		t.tr.ForgetCompleted()
	}
	t.metas = nil
}

// start runs fn as the thread's run of kind, with activity as what it
// is doing at first.
func (t *thread) start(kind, activity string, fn func(context.Context, func(any)) error) {
	if t.closing {
		return
	}
	ctx, err := t.turns.Begin(context.Background())
	if err != nil {
		t.errorNotice(err)
		return
	}
	t.typed, t.replied = nil, false
	t.runKind = kind
	if t.s.memory != nil {
		t.s.memory.Begin()
	}
	done := make(chan struct{})
	t.runDone = done
	t.runStart, t.activity, t.tools = time.Now(), activity, 0
	t.lastActive = t.runStart
	if t.mgd != nil {
		t.mgd.hold.Store(false)
	}
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
	if kind == "turn" {
		// Quiet interrupt-detached exits wait for a turn, not for its duration
		// to happen to cross an inbox tick. They steer this turn, never wake idle.
		reload, evs := events.SplitReload(core.Poll(t.id))
		t.turns.PendingEvents = append(t.turns.PendingEvents, evs...)
		if reload {
			t.requestReload(true)
		}
		t.deliverEvents()
	}
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
		_ = t.call(func() error { t.finish(err, ctxTokens, reported); return nil })
	}()
}

// finish ends a run on the lane.
func (t *thread) finish(err error, ctxTokens int, reported []events.Event) {
	t.turns.PendingEvents = append(t.turns.PendingEvents, reported...)
	t.tr.EndTurn()
	if len(t.attached) == 0 {
		t.dropDisplay()
	} // user shells run independently of the model
	t.ctx = ctxTokens
	t.turns.End()
	if t.s.memory != nil {
		t.s.memory.End()
	}
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
	if t.mgd != nil {
		t.mgd.runEnded(err)
	}
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
		if t.mgd != nil {
			t.mgd.step(e.Usage)
		}
		t.total.Add(e.Usage)
		t.total.LastCost = t.model().Model.Cost
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
	if !t.turns.Busy {
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
			t.turns.Committed(text)
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
	if t.runKind == "compact" {
		defer debug.FreeOSMemory()
	}
	t.flushShell()
	if t.closing {
		return
	}
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
			took = tui.FormatDuration(d.Truncate(100 * time.Millisecond))
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
	follow := t.turns.Settle(t.agent, err)
	clients := t.steerOrigins(follow.Steers)
	t.steers = nil
	var now *pendingInput
	if follow.Now != nil {
		now = *follow.Now
	}
	if follow.Send && (len(follow.Steers) > 0 || now != nil) {
		texts := follow.Steers
		client := firstClient(clients)
		var imgs []provider.Image
		if now != nil {
			texts = append(texts, now.Text)
			client, imgs = now.Client, now.Images
		}
		t.runTurn(t.newInput(client, joinTexts(texts), imgs), false)
		return
	}
	if len(follow.Steers) > 0 || now != nil {
		byClient := map[string][]string{}
		var order []string
		for i, text := range follow.Steers {
			client := clients[i]
			if _, ok := byClient[client]; !ok {
				order = append(order, client)
			}
			byClient[client] = append(byClient[client], text)
		}
		if now != nil {
			if _, ok := byClient[now.Client]; !ok {
				order = append(order, now.Client)
			}
			byClient[now.Client] = append(byClient[now.Client], now.Text)
		}
		for _, client := range order {
			var imgs []provider.Image
			if now != nil && now.Client == client {
				imgs = now.Images
			}
			t.recover(client, false, byClient[client], imgs)
		}
	}

	if len(t.turns.PendingEvents) > 0 && err == nil {
		t.deliverEvents()
		if t.turns.Busy {
			return
		}
	}
	if err != nil && len(t.turns.Queued) > 0 {
		t.turns.QueuePaused = true
		t.notice("", "Queued messages paused. Press enter on an empty prompt to resume, or shift+← to edit.")
		return
	}
	t.maybeSendNextQueued()
}

// steerOrigins are the clients of the user's steers among texts.
func (t *thread) steerOrigins(texts []string) []string {
	remaining := slices.Clone(t.steers)
	out := make([]string, len(texts))
	for j, text := range texts {
		if i := slices.IndexFunc(remaining, func(p *pendingInput) bool { return p.Text == text }); i >= 0 {
			out[j] = remaining[i].Client
			remaining = slices.Delete(remaining, i, i+1)
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
	if ptr := t.turns.SendNow; ptr != nil {
		n := *ptr
		t.turns.SendNow = nil
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
	if len(t.turns.Queued) > 0 || len(t.turns.PendingEvents) > 0 || len(t.steers) > 0 || (t.goal.Active() && !t.goal.Held()) {
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

package app

import (
	"errors"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

// The transcript builder (core/transcript) turns the agent's events, and
// a resumed session's entries, into items; the TUI turns items into
// blocks. Rendering, expanding and clicking stay with the blocks.

// tr is the App's transcript builder, wired to the blocks.
func (a *App) tr() *transcript.Builder {
	if a.items.Handler.Started == nil {
		// Blocks first, then the /remote clients, if any.
		a.items.Handler = transcript.Handler{
			Started:   func(it *transcript.Item) { a.itemStarted(it); a.remoteItem("item/started", it) },
			Delta:     func(it *transcript.Item, d string) { a.itemDelta(it, d); a.remoteDelta(it, d) },
			Updated:   func(it *transcript.Item) { a.itemUpdated(it); a.remoteItem("item/updated", it) },
			Completed: func(it *transcript.Item) { a.itemCompleted(it); a.remoteItem("item/completed", it) },
			Saved:     a.itemSaved, Display: a.itemDisplay}
	}
	return &a.items
}

// replay rebuilds the transcript blocks from session entries: pass the
// active branch (session.Active), not the whole file.
func (a *App) replay(entries []session.Entry) {
	a.replaying = true
	defer func() { a.replaying = false }()
	a.items.IDPrefix = a.sess.ID + "-i"
	a.resetItems()
	a.tr().Replay(entries)
	a.remoteSwitched()
}

// resetItems forgets the items of a cleared transcript.
func (a *App) resetItems() {
	a.tr().Reset()
	a.thinking, a.text, a.compact, a.summaryBlk, a.shellBlk = nil, nil, nil, nil, nil
	clear(a.tools)
	clear(a.itemBlocks)
	clear(a.blocks) // late results for blocks of the old transcript find nothing
}

func (a *App) itemStarted(it *transcript.Item) {
	switch it.Kind {
	case transcript.User:
		if a.steered != nil { // a steer: its messages show as one block
			a.steered = append(a.steered, it.Text)
			return
		}
		a.add(&userBlock{text: it.Text, remote: a.fromRemote && !a.replaying})
	case transcript.Event:
		// Live, events are shown with their titles when delivered.
		if a.replaying {
			for _, t := range eventTitles(it.Text) {
				a.add(&eventBlock{title: t})
			}
		}
	case transcript.Goal:
		// Live, continuing shows the goal's progress; status changes are
		// announced as such.
		if a.replaying {
			a.add(&eventBlock{title: goalMessageTitle(it.Text)})
		}
	case transcript.GoalStatus:
		if b := goalStatusBlock(it.GoalState); b != nil {
			a.add(b)
		}
	case transcript.Hook:
		style := tui.Dim
		if it.Blocked {
			style = func(s string) string { return tui.FG(3, s) }
		}
		a.add(&noticeBlock{text: "⚑ " + it.HookEvent + ": " + it.Text, style: style})
	case transcript.Notice:
		a.add(&noticeBlock{text: it.Text, style: tui.Dim})
	case transcript.Reasoning:
		a.thinking = &thinkingBlock{start: time.Now(), d: &a.details}
		a.thinking.disp.orig.d = &a.origView
		a.trackBlock(it, a.thinking)
		if r := a.openRun(); r != nil { // between two calls, or after the last
			r.add(a.thinking)
		} else {
			a.add(a.thinking)
		}
	case transcript.Assistant:
		a.text = &textBlock{}
		a.text.disp.orig.d = &a.origView
		a.trackBlock(it, a.text)
		a.add(a.text)
	case transcript.Tool:
		b := &toolBlock{args: agent.BashArgs{Description: it.Description, Command: it.Command},
			timeout: it.Timeout, pending: it.Pending, start: time.Now(), d: &a.details}
		if a.tools == nil {
			a.tools = map[string]*toolBlock{}
		}
		a.tools[it.ID] = b
		r := a.openRun()
		if r == nil {
			r = &toolRun{d: &a.details, off: &a.noToolGroups}
			a.add(r)
		}
		r.add(b)
	case transcript.Compaction:
		a.compact = &compactBlock{auto: it.Auto, running: true, d: &a.details}
		a.add(a.compact)
	case transcript.BranchSummary:
		a.summaryItem(it, true, "")
	case transcript.Shell:
		a.shellItemStarted(it)
	case transcript.ExtText:
		a.extTextStarted(it)
	}
}

// openRun is the run of calls the transcript ends with, if it does: a
// call or reasoning that comes next joins it, anything else ends it.
func (a *App) openRun() *toolRun {
	if ch := a.ui.Body.Children; len(ch) > 0 {
		if g, ok := ch[len(ch)-1].(gap); ok {
			if r, ok := g.Component.(*toolRun); ok {
				return r
			}
		}
	}
	return nil
}

// trackBlock remembers the block of an item until the item is saved and
// has its block ID (itemSaved).
func (a *App) trackBlock(it *transcript.Item, b displayBlock) {
	if a.itemBlocks == nil {
		a.itemBlocks = map[string]displayBlock{}
	}
	a.itemBlocks[it.ID] = b
}

func (a *App) itemDelta(it *transcript.Item, d string) {
	switch it.Kind {
	case transcript.Reasoning:
		if a.thinking != nil {
			a.thinking.text.WriteString(d)
		}
	case transcript.Assistant:
		if a.text != nil {
			a.text.text.WriteString(d)
		}
	case transcript.Tool:
		if b := a.tools[it.ID]; b != nil {
			b.append(d)
		}
	case transcript.Compaction:
		if a.compact != nil {
			a.compact.notes.WriteString(d)
		}
	case transcript.BranchSummary:
		a.summaryItem(it, false, d)
	case transcript.Shell:
		a.shellItemDelta(d)
	}
}

// itemUpdated follows a tool call the model is writing, and starts its
// timer when the call begins running.
func (a *App) itemUpdated(it *transcript.Item) {
	if b := a.tools[it.ID]; b != nil && it.Kind == transcript.Tool {
		b.args = agent.BashArgs{Description: it.Description, Command: it.Command}
		b.timeout = it.Timeout
		if b.pending && !it.Pending {
			b.pending, b.start = false, time.Now()
		}
	}
}

func (a *App) itemCompleted(it *transcript.Item) {
	switch it.Kind {
	case transcript.Reasoning:
		if t := a.thinking; t != nil {
			t.done, t.dur = true, it.Duration
		}
		a.thinking = nil
	case transcript.Assistant:
		a.text = nil
	case transcript.Tool:
		if b := a.tools[it.ID]; b != nil {
			b.pending, b.done = false, true
			for _, im := range it.Images {
				b.images = append(b.images, images.ViewLabel(im))
			}
			if r := it.Result; r != nil {
				b.res = agent.BashResult{ExitCode: r.ExitCode, TimedOut: r.TimedOut, Canceled: r.Canceled, Duration: it.Duration,
					Job: r.Job, Background: r.Background}
				if r.Err != "" {
					b.res.Err = errors.New(r.Err)
				}
			}
			delete(a.tools, it.ID)
		}
	case transcript.Compaction:
		c := a.compact
		a.compact = nil
		if c == nil {
			return
		}
		if it.Status == transcript.Failed { // failed or canceled
			a.ui.Body.Remove(gap{c})
			return
		}
		c.running = false
		c.notes.Reset()
		c.notes.WriteString(it.Text)
		c.before, c.after, c.elapsed = it.TokensBefore, it.TokensAfter, it.Duration
	case transcript.BranchSummary:
		a.summaryItem(it, false, "")
	case transcript.Shell:
		a.shellItemCompleted(it)
	}
}

// eventTitles are the first lines of the events in a message.
func eventTitles(text string) []string {
	var out []string
	for _, e := range events.Split(text) {
		out = append(out, events.TitleOf(e))
	}
	return out
}

// goalMessageTitle names a goal message on replay.
func goalMessageTitle(text string) string {
	if strings.Contains(text, "<objective>") {
		return "◎ Continuing goal"
	}
	return "◎ " + tui.FirstLine(goal.Body(text))
}

// goalStatusBlock announces a goal status change, as codex words it: "Goal
// stalled" with the goal's usage summary (and the model's note); nil for
// an active goal.
func goalStatusBlock(g *goal.Goal) *infoBlock {
	if g == nil || g.Status == goal.Active {
		return nil
	}
	hint := g.Summary()
	if g.Note != "" {
		hint += "\nNote: " + g.Note
	}
	return &infoBlock{title: "Goal " + g.Status.Label(), hint: hint}
}

// onEvent handles an event of the running agent: the transcript builder
// makes the blocks; the rest is the footer's state and the goal.
func (a *App) onEvent(ev any) {
	if e, ok := ev.(agent.SteerCommitted); ok {
		n := 0 // the user's own steers, shown as pending until now
		for _, t := range e.Texts {
			if !isEvent(t) && !goal.IsMessage(t) {
				n++
			}
		}
		a.pendingSteers = a.pendingSteers[min(n, len(a.pendingSteers)):]
		if n > 0 {
			a.goal.UserInput() // the goal waits for the user once this turn ends
		}
		a.steered = []string{}
		a.tr().Event(ev)
		if len(a.steered) > 0 {
			remote := false
			for _, t := range a.steered {
				remote = a.takeRemoteSteer(t) || remote
			}
			a.add(&userBlock{text: strings.Join(a.steered, "\n\n"), remote: remote})
		}
		a.steered = nil
		a.remotePending()
		return
	}
	a.tr().Event(ev)
	a.goal.Event(ev)
	a.lastEvent = a.clock()
	switch ev.(type) {
	case agent.TextDelta, agent.ReasoningDelta, agent.ToolDraft, agent.ToolStart, agent.StepEnd:
		a.replied = true
	}
	a.remoteGoal()
	a.backgroundEvent(ev)
	switch e := ev.(type) {
	case agent.ToolDraft:
		// The call's block shows what it is and how long it has run.
		a.activity = "Working"
		if a.draftChars == nil {
			a.draftChars = map[int]int{}
		}
		a.draftChars[e.Index] = len(e.Args.Command) + len(e.Args.Description)
	case agent.ToolStart:
		a.activity = "Working"
		a.toolsRunning++
	case agent.ToolEnd:
		a.activity = "Thinking"
		a.toolsRunning = max(0, a.toolsRunning-1)
	case agent.TextDelta:
		a.streamChars += len(e.Text)
	case agent.ReasoningDelta:
		a.streamChars += len(e.Text)
	case agent.StepEnd:
		a.turnOut += e.Usage.CompletionTokens
		a.draftChars = nil
		a.turnIn += max(0, e.Usage.PromptTokens-e.Usage.CachedTokens-e.Usage.CacheWriteTokens)
		a.streamChars = 0
		a.ctxTokens = e.Context
		a.usage.add(e.Usage)
		a.usage.lastCost = a.model().Model.Cost
		a.statusTrigger()
		a.remoteStep(e.Usage)
	case agent.CompactStart:
		a.activity = "Compacting context"
	case agent.CompactEnd:
		a.ctxTokens = e.After
		a.activity = "Thinking"
		a.notice("Long threads and repeated compactions can make the model less accurate. Start a new conversation (/clear) when you can.")
	}
}

// announceGoal shows a goal status change in the transcript.
func (a *App) announceGoal(g *goal.Goal) {
	if g == nil || g.Status == goal.Active {
		return
	}
	c := *g
	a.tr().Add(transcript.Item{Kind: transcript.GoalStatus, GoalState: &c})
	if g.Status == goal.Blocked {
		msg := "The goal is blocked"
		if g.Note != "" {
			msg += ": " + g.Note
		}
		a.notify("goal_blocked", msg)
	}
}

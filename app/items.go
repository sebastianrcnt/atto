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
	"github.com/sebastianrcnt/atto/tui"
)

// The runtime's transcript builder (core/transcript) turns the agent's
// events, and a resumed session's entries, into items; the terminal turns
// the items it is sent into blocks (notify.go). Rendering, expanding and
// clicking stay with the blocks.

// resetItems forgets the items of a cleared transcript.
func (a *App) resetItems() {
	a.thinking, a.text, a.compact, a.summaryBlk, a.shellBlk = nil, nil, nil, nil, nil
	a.steerGroup, a.steerBlock = "", nil
	clear(a.tools)
	clear(a.kinds)
	clear(a.itemBlocks)
	clear(a.blocks) // late results for blocks of the old transcript find nothing
}

func (a *App) itemStarted(it *transcript.Item) {
	switch it.Kind {
	case transcript.User:
		a.add(&userBlock{text: it.Text})
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

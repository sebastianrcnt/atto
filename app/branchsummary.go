package app

import (
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

// Branch summaries, after pi: picking an entry in /tree that leaves part
// of the conversation behind asks "Summarize branch?" first. With a
// summary, the runtime has the current model write one (Esc cancels it and
// the tree opens again) and records it where the leaf moves to, as a
// "branch_summary" entry the model sees on the new branch.
// settings.json "branchSummary": {"skipPrompt": true} never asks and goes
// back without one.

// summaryRequest asks for a summary of the branch being left;
// instructions are the user's own focus for it ("" for none).
type summaryRequest struct{ instructions string }

// Summary choices, in pi's order and wording.
const (
	summaryNone   = "No summary"
	summaryPlain  = "Summarize"
	summaryCustom = "Summarize with custom prompt"
)

// selectTreeEntry is picking id in the tree: it asks whether to summarize
// the branch being left, when there is one, then moves.
func (a *App) selectTreeEntry(id string) {
	entries := a.loadSession()
	if id == session.Leaf(entries) {
		a.notice("Already at this point.")
		return
	}
	leaf, _, ok := session.BranchPoint(entries, id)
	if !ok || a.skipSummary || !agent.HasBranchContent(session.Abandoned(entries, session.Leaf(entries), leaf)) {
		a.navigateTree(id)
		return
	}
	a.askSummary(id)
}

// askSummary shows the "Summarize branch?" choice for moving to id. Esc
// goes back to the tree.
func (a *App) askSummary(id string) {
	sel := &tui.SelectList{Title: "Summarize branch?"}
	for _, c := range []string{summaryNone, summaryPlain, summaryCustom} {
		sel.Items = append(sel.Items, tui.SelectItem{Label: c, Value: c})
	}
	sel.OnCancel = func() {
		a.closeModal()
		a.cmdTree("")
	}
	sel.OnSelect = func(it tui.SelectItem) {
		a.closeModal()
		switch it.Value {
		case summaryNone:
			a.navigateTree(id)
		case summaryPlain:
			a.moveTo(id, &summaryRequest{})
		case summaryCustom:
			a.askSummaryInstructions(id)
		}
	}
	a.openModal(sel)
}

// askSummaryInstructions reads the custom focus for the summary; Esc goes
// back to the choice.
func (a *App) askSummaryInstructions(id string) {
	in := &labelInput{title: "Custom summarization instructions:", hint: "enter summarize  esc back"}
	in.onDone = func(ok bool, text string) {
		a.closeModal()
		if !ok {
			a.askSummary(id)
			return
		}
		a.moveTo(id, &summaryRequest{instructions: strings.TrimSpace(text)})
	}
	a.openModal(labelModal{in})
}

// labelModal shows a labelInput on its own, framed like the tree.
type labelModal struct{ *labelInput }

func (m labelModal) Render(width int) []string {
	rule := tui.Dim(strings.Repeat("─", width))
	return append(append([]string{rule}, m.labelInput.Render(width)...), rule)
}

// summaryBlock shows a branch summary: streaming while the model writes
// it, then one line that expands to the summary on click.
type summaryBlock struct {
	expander
	clickable
	running bool
	text    strings.Builder
	elapsed time.Duration
	cache   tui.RenderCache[summaryKey]
}

// summaryKey is what a branch summary block's lines depend on.
type summaryKey struct {
	running, expanded bool
	text              string
	elapsed           time.Duration
}

func (c *summaryBlock) Click(line int) bool {
	if c.running || !c.hit(line) {
		return false
	}
	c.toggle()
	return true
}

func (c *summaryBlock) Render(width int) []string {
	key := summaryKey{running: c.running, expanded: c.expanded(), text: c.text.String(), elapsed: c.elapsed}
	return c.cache.Render(width, key, func() []string { return c.render(width) })
}

func (c *summaryBlock) render(width int) []string {
	if c.running {
		out := []string{tui.Dim("  ⎇ Summarizing the branch being left · esc to cancel…")}
		lines := tui.Wrap(strings.TrimSpace(c.text.String()), max(1, width-4))
		if len(lines) > thinkingPreviewLines {
			lines = lines[len(lines)-thinkingPreviewLines:]
		}
		for _, l := range lines {
			out = append(out, "    "+tui.Dim(l))
		}
		return c.clicks(false, out, false)
	}
	head := tui.FG(5, "  ⎇ ") + "Branch summary"
	if c.elapsed > 0 {
		head += tui.Dim(" · " + tui.FormatDuration(c.elapsed))
	}
	if !c.expanded() {
		return c.clicks(true, []string{tui.Truncate(head+tui.Dim(" · what was tried on the branch left · click to view"), width, "…")}, false)
	}
	out := []string{tui.Truncate(head, width, "…")}
	for _, l := range tui.Markdown(c.text.String(), max(1, width-4)) {
		out = append(out, "    "+l)
	}
	return c.clicks(true, append(out, disclosure(true, 0, "")), true)
}

// summaryItem applies a branch summary item's changes to its block; the
// item handlers in items.go call it.
func (a *App) summaryItem(it *transcript.Item, started bool, delta string) {
	switch {
	case started:
		a.summaryBlk = &summaryBlock{running: true, d: &a.details}
		a.add(a.summaryBlk)
	case a.summaryBlk == nil:
	case it.Status == transcript.InProgress:
		a.summaryBlk.text.WriteString(delta)
	case it.Status == transcript.Failed: // canceled or failed
		a.ui.Body.Remove(gap{a.summaryBlk})
		a.summaryBlk = nil
	default:
		c := a.summaryBlk
		a.summaryBlk = nil
		c.running = false
		c.text.Reset()
		c.text.WriteString(it.Text)
		c.elapsed = it.Duration
	}
}

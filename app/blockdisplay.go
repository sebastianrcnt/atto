package app

import (
	"strings"

	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

// Display-only changes extensions make to assistant blocks: a short status
// next to the header (ctx.ui.setBlockStatus) and a replacement for what the
// block shows (ctx.ui.setBlockDisplay), with a line to flip back to the
// original. None of it reaches the model or the session's messages; it is
// recorded as block_display entries so a resumed session shows it again.

// blockDisplay is the display state of one block. The zero value shows the
// block as it is.
//
// ver counts changes to anything the block renders from it (statuses, the
// override and who owns it). A render that is cached must be keyed on ver
// and on showingOriginal(), which also changes with ctrl+o and with a click
// on the toggle line, without ver changing: see key.
type blockDisplay struct {
	orig expander // expanded() means: showing the original text

	id      string // the block ID extensions use
	item    string // the transcript item's ID, for /remote
	entryID string // of the assistant message, for the session
	kind    string // session.BlockText or session.BlockReasoning

	state transcript.BlockDisplay
	ver   int

	metaLine int // line of the last render holding the toggle, -1 when none
}

// key identifies what a render of the block depends on besides its text
// and width.
func (b *blockDisplay) key() (ver int, original bool) { return b.ver, b.showingOriginal() }

func (b *blockDisplay) hasOverride() bool { return b.state.Text != "" }

func (b *blockDisplay) showingOriginal() bool { return b.hasOverride() && b.orig.expanded() }

// shown is what the block displays given its own text.
func (b *blockDisplay) shown(original string) string {
	if b.hasOverride() && !b.showingOriginal() {
		return b.state.Text
	}
	return original
}

// setStatus sets (text "" removes) the status of ext; it reports a change.
func (b *blockDisplay) setStatus(ext, text string) bool {
	if !b.state.SetStatus(ext, text) {
		return false
	}
	b.ver++
	return true
}

// setDisplay sets (text "" restores) the text ext shows in place of the
// block's own; it reports a change. The latest extension to set a text
// owns the override, and only the owner can restore the original.
func (b *blockDisplay) setDisplay(ext, text string) bool {
	if !b.state.SetDisplay(ext, text) {
		return false
	}
	b.ver++
	return true
}

// header is the statuses as a dim suffix (" · translating…"), "" when none.
func (b *blockDisplay) header() string {
	var out strings.Builder
	for _, s := range b.state.Statuses {
		out.WriteString(tui.Dim(" · " + tui.StripControls(s.Text)))
	}
	return out.String()
}

// toggleLine is the line that flips between the override and the original:
// "" when the block has no override.
func (b *blockDisplay) toggleLine(width int) string {
	if !b.hasOverride() {
		return ""
	}
	owner := tui.StripControls(b.state.Owner)
	what := "shown: " + owner + " (click or ctrl+o to show original)"
	if b.showingOriginal() {
		what = "original shown (click or ctrl+o to show " + owner + "'s)"
	}
	return tui.Truncate(tui.Dim("  · "+what), width, "…")
}

// click handles a click on line of the block; the toggle line is the only
// one that reacts.
func (b *blockDisplay) click(line int) bool {
	if !b.hasOverride() || b.metaLine < 0 || line != b.metaLine {
		return false
	}
	b.orig.toggle()
	return true
}

// displayBlock is a block extensions can change.
type displayBlock interface {
	display() *blockDisplay
}

func (t *textBlock) display() *blockDisplay     { return &t.disp }
func (t *thinkingBlock) display() *blockDisplay { return &t.disp }

// bindBlock gives the block of a saved reasoning or assistant item its
// block ID, so the runtime's item/display can name it.
func (a *App) bindBlock(w server.Item) {
	b := a.itemBlocks[w.ID]
	if b == nil || w.BlockID == "" {
		return
	}
	kind := session.BlockText
	if w.Type == server.ItemReasoning {
		kind = session.BlockReasoning
	}
	d := b.display()
	d.entryID, d.kind, d.item, d.id = w.EntryID, kind, w.ID, w.BlockID
	if a.blocks == nil {
		a.blocks = map[string]displayBlock{}
	}
	a.blocks[d.id] = b
	delete(a.itemBlocks, w.ID)
}

// wireDisplay applies what extensions show on block id (item/display).
func (a *App) wireDisplay(id string, w *server.BlockDisplay) {
	b := a.blocks[id]
	if b == nil || id == "" {
		return
	}
	var st transcript.BlockDisplay
	if w != nil {
		st.Owner, st.Text = w.Ext, w.Text
		for _, s := range w.Statuses {
			st.Statuses = append(st.Statuses, transcript.BlockStatus{Ext: s.Ext, Text: s.Text})
		}
	}
	d := b.display()
	d.state = st
	d.ver++
}

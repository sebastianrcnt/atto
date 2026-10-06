package app

import (
	"strings"

	"github.com/sebastianrcnt/atto/core/transcript"
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

// itemSaved gives the block of a saved reasoning or assistant item its
// block ID, so extensions can name it. Live and on replay alike.
func (a *App) itemSaved(it *transcript.Item) {
	b := a.itemBlocks[it.ID]
	if b == nil || a.sess == nil {
		return
	}
	kind := session.BlockText
	if it.Kind == transcript.Reasoning {
		kind = session.BlockReasoning
	}
	d := b.display()
	d.entryID, d.kind, d.item = it.EntryID, kind, it.ID
	d.id = session.BlockID(a.sess.ID, it.EntryID, kind)
	if a.blocks == nil {
		a.blocks = map[string]displayBlock{}
	}
	a.blocks[d.id] = b
	delete(a.itemBlocks, it.ID)
}

// itemDisplay applies a saved block_display entry on replay.
func (a *App) itemDisplay(d transcript.Display) {
	if a.sess == nil {
		return
	}
	b := a.blocks[session.BlockID(a.sess.ID, d.EntryID, d.Block)]
	if b == nil {
		return
	}
	bd := b.display()
	bd.setStatus(d.Ext, d.Status)
	bd.setDisplay(d.Ext, d.Text)
}

// blockStatus is ctx.ui.setBlockStatus: a block that is gone (the session
// changed) is ignored.
func (a *App) blockStatus(ext, id, text string) {
	if b := a.blocks[id]; b != nil && b.display().setStatus(ext, text) {
		a.recordBlock(b.display(), ext)
	}
}

// blockText is ctx.ui.setBlockDisplay.
func (a *App) blockText(ext, id, text string) {
	if b := a.blocks[id]; b != nil && b.display().setDisplay(ext, text) {
		a.recordBlock(b.display(), ext)
	}
}

// recordBlock saves ext's state for the block in the session, and tells
// the /remote clients.
func (a *App) recordBlock(d *blockDisplay, ext string) {
	a.remoteDisplay(d)
	if a.sess == nil || d.entryID == "" || strings.HasPrefix(d.entryID, "n") { // not recorded: nothing to attach to
		return
	}
	status, display := d.state.Snapshot(ext)
	a.sess.Append(session.Entry{Type: session.TypeBlockDisplay, TargetID: d.entryID, Block: d.kind, Ext: ext, Status: status, Display: display})
}

// blockState is what extensions show on the block blockID; nil for a
// block not known ("" is an item that has no block ID yet).
func (a *App) blockState(blockID string) *transcript.BlockDisplay {
	if b := a.blocks[blockID]; b != nil && blockID != "" {
		return &b.display().state
	}
	return nil
}

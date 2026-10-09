package server

import (
	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/ui"
)

// Legacy data is folded during replay, then converted to the single portable
// drawing contract. It is never emitted as a string UI notification.
func legacyDisplayTree(w Item, d *transcript.LegacyDisplay) *UIDisplay {
	if d == nil || d.IsZero() {
		return nil
	}
	text := w.Text
	if d.Text != "" {
		text = d.Text
	}
	children := []ui.Node{ui.Markdown(ui.MarkdownProps{Text: text})}
	for _, s := range d.Statuses {
		children = append(children, ui.Text(ui.TextProps{Text: s.Ext + ": " + s.Text, Color: ui.Muted}))
	}
	n := ui.Box(ui.BoxProps{}, children...)
	return &UIDisplay{Tree: &n}
}

// Legacy replay identities, not a live string rendering API.
// block is a reasoning or agentMessage item extensions can name.
type block struct {
	item    string // the item's ID
	entryID string // of the assistant message
	kind    string // session.BlockText or session.BlockReasoning
	disp    transcript.LegacyDisplay
}

// blocks are a thread's blocks by block ID.
type blocks map[string]*block

// saved makes a block of a reasoning or assistant item saved in session sid.
func (bl blocks) saved(sid string, it *transcript.Item) {
	if it.EntryID == "" {
		return
	}
	kind := session.BlockText
	if it.Kind == transcript.Reasoning {
		kind = session.BlockReasoning
	}
	bl[session.BlockID(sid, it.EntryID, kind)] = &block{item: it.ID, entryID: it.EntryID, kind: kind}
}

// attach gives an item what extensions show on its block.
func (bl blocks) attach(w Item) Item {
	if b := bl[w.BlockID]; b != nil && w.BlockID != "" && w.UIDisplay == nil {
		w.UIDisplay = legacyDisplayTree(w, &b.disp)
	}
	return w
}

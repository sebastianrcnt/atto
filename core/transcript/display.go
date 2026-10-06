package transcript

import "slices"

// BlockDisplay is what extensions show on a reasoning or assistant block:
// short statuses for its header (ctx.ui.setBlockStatus) and a text shown
// in place of the block's own (ctx.ui.setBlockDisplay). It is display
// only: the model and the session's messages never change. The zero value
// shows the block as it is.
//
// Statuses is replaced, never changed in place, so a copy of the struct
// keeps what it had.
type BlockDisplay struct {
	Statuses []BlockStatus // in the order the extensions first set them
	Owner    string        // the extension whose text replaces the block's
	Text     string        // that text; "" means no replacement
}

// BlockStatus is one extension's status on a block.
type BlockStatus struct{ Ext, Text string }

// IsZero reports whether the block shows as it is.
func (b *BlockDisplay) IsZero() bool { return len(b.Statuses) == 0 && b.Text == "" }

// SetStatus sets (text "" removes) the status of ext; it reports a change.
func (b *BlockDisplay) SetStatus(ext, text string) bool {
	for i, s := range b.Statuses {
		if s.Ext != ext {
			continue
		}
		if s.Text == text {
			return false
		}
		if text == "" {
			b.Statuses = append(b.Statuses[:i:i], b.Statuses[i+1:]...)
		} else {
			b.Statuses = append([]BlockStatus(nil), b.Statuses...)
			b.Statuses[i].Text = text
		}
		return true
	}
	if text == "" {
		return false
	}
	b.Statuses = append(slices.Clip(b.Statuses), BlockStatus{ext, text})
	return true
}

// SetDisplay sets (text "" restores) the text ext shows in place of the
// block's own; it reports a change. The latest extension to set a text
// owns the replacement, and only the owner can restore the original.
func (b *BlockDisplay) SetDisplay(ext, text string) bool {
	if text == "" {
		if b.Owner != ext || b.Text == "" {
			return false
		}
		b.Owner, b.Text = "", ""
		return true
	}
	if b.Owner == ext && b.Text == text {
		return false
	}
	b.Owner, b.Text = ext, text
	return true
}

// Apply applies a saved block_display entry: the extension's whole state
// for the block. It reports a change.
func (b *BlockDisplay) Apply(d Display) bool {
	s := b.SetStatus(d.Ext, d.Status)
	return b.SetDisplay(d.Ext, d.Text) || s
}

// Snapshot is ext's whole state for the block, as a block_display entry
// keeps it.
func (b *BlockDisplay) Snapshot(ext string) (status, display string) {
	for _, s := range b.Statuses {
		if s.Ext == ext {
			status = s.Text
		}
	}
	if b.Owner == ext {
		display = b.Text
	}
	return status, display
}

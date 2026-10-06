package tui

import "strings"

// A scrollbar on the right edge of the fullscreen body. The last screen
// column is always reserved for it, so text never reflows when it appears
// or disappears: with PaddingX >= 1 that is the margin that is blank
// anyway, with no padding the content is one column narrower. The body is
// truncated to the reserved width, so even a wide character at the right
// edge cannot reach the bar column.

const (
	barTrack = "│"
	barThumb = "┃"
)

// scrollbar is the bar of the last fullscreen frame; zero when none showed.
type scrollbar struct {
	on    bool
	col   int // 1-based screen column
	rows  int // track height, the screen rows showing body
	size  int // thumb height
	top   int // thumb offset from the top of the track
	total int // body lines
}

// rightMargin is the blank columns on the right of fullscreen content: the
// padding, but at least the column the scrollbar uses.
func (t *TUI) rightMargin() int { return max(t.PaddingX, 1) }

// fullscreenWidth is the width the body and footer render at in fullscreen.
func (t *TUI) fullscreenWidth(width int) int {
	return max(1, width-t.PaddingX-t.rightMargin())
}

// thumbGeometry sizes the thumb for a track of rows cells over total lines
// of which rows are visible starting at start. The size is proportional to
// rows/total (at least one cell); the top is proportional to start, and is
// at the very bottom exactly when the view is.
func thumbGeometry(total, rows, start int) (top, size int) {
	if rows <= 0 || total <= rows {
		return 0, max(0, rows)
	}
	size = min(rows, max(1, (rows*rows+total/2)/total))
	maxStart := total - rows
	start = min(max(start, 0), maxStart)
	free := rows - size
	top = (start*free + maxStart/2) / maxStart
	if start == maxStart {
		top = free
	}
	return min(max(top, 0), free), size
}

// decorate appends the bar to the body rows of frame, which holds the
// visible body lines at frame[gap:gap+rows].
func (t *TUI) decorate(frame []string, gap, start, rows, total, width int) {
	t.bar = scrollbar{}
	if rows <= 0 || total <= rows {
		return
	}
	top, size := thumbGeometry(total, rows, start)
	t.bar = scrollbar{on: true, col: width, rows: rows, size: size, top: top, total: total}
	for k := range rows {
		glyph := Dim(barTrack)
		if k >= top && k < top+size {
			glyph = barThumb
		}
		l := frame[gap+k]
		// A component that ignores its width must not push the bar out.
		if VisibleWidth(l) > width-1 {
			l = Truncate(l, width-1, "")
		}
		if w := VisibleWidth(l); w < width-1 {
			l += strings.Repeat(" ", width-1-w)
		}
		frame[gap+k] = l + Reset + glyph
	}
}

// onBar reports whether the 1-based screen cell is on the bar's track.
func (t *TUI) onBar(x, y int) bool {
	row := y - 1 - t.viewTop
	return t.bar.on && x == t.bar.col && row >= 0 && row < t.bar.rows
}

// pressBar starts a bar drag. A press on the thumb grabs it where it was
// touched; a press on the track jumps there, centering the thumb on the
// pointer, and goes on as a drag.
func (t *TUI) pressBar(y int) {
	b := t.bar
	row := y - 1 - t.viewTop
	t.mouse = mouseState{bar: true, barGrab: b.size / 2}
	if row >= b.top && row < b.top+b.size {
		t.mouse.barGrab = row - b.top
	}
	t.dragBar(y)
}

// dragBar scrolls so the thumb follows the pointer.
func (t *TUI) dragBar(y int) {
	b := t.bar
	free := b.rows - b.size
	if !b.on || free <= 0 {
		return
	}
	top := min(max(y-1-t.viewTop-t.mouse.barGrab, 0), free)
	maxStart := b.total - b.rows
	start := (top*maxStart + free/2) / free
	t.scroll = max(0, maxStart-start)
}

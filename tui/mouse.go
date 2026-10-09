package tui

import (
	"fmt"
	"strings"
	"time"
)

// Mouse reporting for fullscreen mode: 1000 reports presses and releases,
// 1002 adds motion while a button is held (for drag selection), and 1006
// encodes reports as SGR, which has no coordinate limit and says which
// button was released. Windows Terminal and conhost deliver the same SGR
// reports through VT input mode.
const (
	mouseOn  = "\x1b[?1000h\x1b[?1002h\x1b[?1006h"
	mouseOff = "\x1b[?1006l\x1b[?1002l\x1b[?1000l"
)

func (t *TUI) enterFullscreen() string {
	s := "\x1b[?1049h"
	if !t.NoMouse {
		s += mouseOn
	}
	return s + kittyOn + "\x1b[2J"
}

func (t *TUI) leaveFullscreen() string {
	if t.NoMouse {
		return kittyOff + "\x1b[?1049l"
	}
	return kittyOff + mouseOff + "\x1b[?1049l"
}

// mouseEvent is one decoded mouse report.
type mouseEvent struct {
	btn    int  // 0-2 buttons, 64/65 wheel; modifier and motion bits removed
	x, y   int  // 1-based column and row
	press  bool // false for a release
	motion bool // the pointer moved with a button held (mode 1002)
}

// parseMouse decodes a mouse report in SGR form (ESC [ < b ; x ; y M|m) or
// the legacy X10 form (ESC [ M b x y, each byte offset by 32). ok is false
// when data is not a mouse report.
func parseMouse(data string) (m mouseEvent, ok bool) {
	var b int
	switch {
	case strings.HasPrefix(data, "\x1b[<"):
		var final byte
		if n, _ := fmt.Sscanf(data, "\x1b[<%d;%d;%d%c", &b, &m.x, &m.y, &final); n != 4 {
			return mouseEvent{}, true // malformed but still a mouse report
		}
		m.press = final == 'M'
	case len(data) == 6 && strings.HasPrefix(data, "\x1b[M"):
		b = int(data[3]) - 32
		m.x, m.y = int(data[4])-32, int(data[5])-32
		// X10 reports a release as button 3 and can't say which one.
		m.press = b&3 != 3 || b&64 != 0
	default:
		return mouseEvent{}, false
	}
	m.motion = b&32 != 0
	m.btn = b &^ 0b111100 // drop shift/meta/ctrl and motion bits
	return m, true
}

// mouseState tracks button 0 between press and release.
type mouseState struct {
	down   bool // pressed in the body, not released yet
	moved  bool // the pointer left the press cell
	px, py int  // press cell (1-based)
	x, y   int  // latest pointer cell
	count  int  // 1, 2 or 3 for a single, double or triple click
	// at and the press cell of the previous press, for counting clicks.
	at     time.Time
	lx, ly int
	// hadSel: a selection was showing at the press, so a click only clears it.
	hadSel   bool
	pinPress bool // pressed on the pinned prompt
	line     int  // body line pressed: output may arrive before the release
	autoDir  int  // -1 or +1 while dragging past the top or bottom edge
	autoOn   bool // the auto-scroll ticker runs
	bar      bool // dragging the scrollbar thumb
	barGrab  int  // thumb row held by the pointer
}

// multiClickTime is how soon a press must follow the last one, on the
// same cell, to count as a double or triple click.
const multiClickTime = 500 * time.Millisecond

// handleScroll consumes mouse and paging input in fullscreen mode.
func (t *TUI) handleScroll(data string) bool {
	if t.Mode != Fullscreen {
		return false
	}
	if m, ok := parseMouse(data); ok {
		t.handleMouse(m)
		return true // swallow all other mouse events
	}
	_, h := t.term.Size()
	switch Key(data) {
	case "pageup":
		t.ScrollBy(max(1, h/2))
		return true
	case "pagedown":
		t.ScrollBy(-max(1, h/2))
		return true
	}
	return false
}

func (t *TUI) handleMouse(m mouseEvent) {
	if t.Side != nil && t.sideLeft > 0 && m.x > t.sideLeft && (m.btn == 64 || m.btn == 65) {
		if s, ok := t.Side.(interface{ Scroll(int) }); ok {
			delta := 3
			if m.btn == 64 {
				delta = -3
			}
			s.Scroll(delta)
		}
		return
	}
	switch {
	case m.btn == 64:
		t.ScrollBy(3) // a drag in progress follows on the next frame
	case m.btn == 65:
		t.ScrollBy(-3)
	case m.motion:
		if t.mouse.bar {
			t.dragBar(m.y)
		} else if t.mouse.down {
			t.drag(m.x, m.y)
		}
	case m.press && m.btn == 0:
		t.press(m.x, m.y)
	case !m.press && t.mouse.bar:
		t.mouse.bar = false
	case !m.press && t.mouse.down:
		t.release(m.x, m.y)
	}
}

func (t *TUI) clock() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
}

// press starts a click or a drag. Clicks act on release, so that a drag
// that starts on a block header doesn't toggle it. The footer is the
// exception: its components are buttons and act at once.
func (t *TUI) press(x, y int) {
	row := y - 1
	if t.Side != nil && t.sideLeft > 0 && x > t.sideLeft {
		if c, ok := t.Side.(CellClickable); ok {
			c.ClickAt(x-1-t.sideLeft, row)
		} else if c, ok := t.Side.(Clickable); ok {
			c.Click(row)
		}
		return
	}
	if row >= t.footerTop {
		t.sel = selection{}
		t.mouse.down = false
		t.Footer.ClickAt(max(0, x-1-t.PaddingX), row-t.footerTop)
		return
	}
	if t.onBar(x, y) {
		t.pressBar(y) // never a selection
		return
	}
	now := t.clock()
	count := 1
	if x == t.mouse.lx && y == t.mouse.ly && now.Sub(t.mouse.at) < multiClickTime {
		count = t.mouse.count%3 + 1
	}
	t.mouse = mouseState{
		down: t.viewRows > 0, px: x, py: y, x: x, y: y, count: count,
		at: now, lx: x, ly: y, hadSel: t.sel.active,
		pinPress: t.pinned && row == t.viewTop,
	}
	if !t.mouse.down {
		return
	}
	pos := t.posAt(x, y)
	t.mouse.line = pos.line
	switch count {
	case 1:
		t.sel = selection{aLo: pos, aHi: pos}
	case 2:
		lo, hi := t.wordSpan(pos)
		t.sel = selection{active: true, unit: unitWord, aLo: lo, aHi: hi, lo: lo, hi: hi, headHi: true}
	case 3:
		lo, hi := t.lineSpan(pos)
		t.sel = selection{active: true, unit: unitLine, aLo: lo, aHi: hi, lo: lo, hi: hi, headHi: true}
	}
}

// drag extends the selection to the pointer, and starts scrolling when it
// is past the top or bottom of the transcript.
func (t *TUI) drag(x, y int) {
	t.mouse.x, t.mouse.y = x, y
	if x != t.mouse.px || y != t.mouse.py {
		t.mouse.moved = true
	}
	if !t.mouse.moved && t.mouse.count == 1 {
		return
	}
	t.extendTo(t.posAt(x, y))
	row := y - 1
	switch {
	case row < t.viewTop || (t.viewTop == 0 && row == 0):
		t.mouse.autoDir = -1
	case row >= t.viewTop+t.viewRows:
		t.mouse.autoDir = 1
	default:
		t.mouse.autoDir = 0
	}
	if t.mouse.autoDir != 0 {
		t.autoScrollStep()
		t.startAutoScroll()
	}
}

// release ends a click or a drag: a click (press and release on the same
// cell, no movement) goes to the body; a drag or a double or triple click
// copies what it selected.
func (t *TUI) release(x, y int) {
	if x != t.mouse.x || y != t.mouse.y {
		t.drag(x, y) // some terminals send no motion before the release
	}
	ms := t.mouse
	t.mouse.down, t.mouse.autoDir = false, 0
	if ms.moved {
		t.mouse.at = time.Time{} // the next press starts a new click series
	}
	if ms.count == 1 {
		back := x == ms.px && y == ms.py // a drag that came back is nothing
		if !ms.moved || back {
			t.sel = selection{}
			if ms.moved || ms.hadSel {
				return // a click while a selection showed only clears it
			}
			row := ms.py - 1
			switch {
			case ms.pinPress:
				// Clicking the pinned line scrolls back up to it.
				t.ScrollBy(t.viewRows / 2)
			case row >= t.viewTop && row < t.viewTop+t.viewRows:
				t.Body.ClickAt(max(0, x-1-t.PaddingX), ms.line)
			}
			return
		}
	}
	t.copySelection()
}

// startAutoScroll keeps scrolling while the pointer stays past an edge:
// the terminal reports motion only when the pointer changes cell.
func (t *TUI) startAutoScroll() {
	if t.mouse.autoOn {
		return
	}
	every := t.autoScrollEvery
	if every == 0 {
		every = 50 * time.Millisecond
	}
	t.mouse.autoOn = true
	go func() {
		tick := time.NewTicker(every)
		defer tick.Stop()
		for {
			select {
			case <-t.done:
				return
			case <-tick.C:
			}
			t.mu.Lock()
			more := !t.stopped && t.autoScrollStep()
			if !more {
				t.mouse.autoOn = false
			}
			t.mu.Unlock()
			if !more {
				return
			}
			t.RequestRender()
		}
	}()
}

// autoScrollStep scrolls one line toward the pointer while dragging past
// an edge; the next frame moves the selection's end with the view.
func (t *TUI) autoScrollStep() bool {
	if !t.mouse.down || t.mouse.autoDir == 0 {
		return false
	}
	t.ScrollBy(-t.mouse.autoDir)
	return true
}

// selecting reports whether a selection shows or a drag is under way.
func (t *TUI) selecting() bool { return t.sel.active || t.mouse.down }

// syncSelection runs once the frame's layout is known: a drag follows the
// pointer as the view scrolls under it, and a selection that no longer
// fits the transcript (resized, cleared) is dropped.
func (t *TUI) syncSelection(width int) {
	if (t.prevWidth != 0 && t.prevWidth != width) || t.sel.hi.line >= len(t.lastBody) && t.sel.active {
		t.sel, t.mouse.down = selection{}, false
		return
	}
	if t.mouse.down && (t.mouse.moved || t.mouse.count > 1) {
		t.extendTo(t.posAt(t.mouse.x, t.mouse.y))
	}
}

// posAt maps a screen cell to a body position. Above the transcript it is
// the start of the first visible line, below it the end of the last one.
func (t *TUI) posAt(x, y int) textPos {
	row, col := y-1, max(0, x-1)
	switch {
	case t.viewRows == 0:
		return textPos{}
	case row < t.viewTop:
		return textPos{t.viewStart, 0}
	case row >= t.viewTop+t.viewRows:
		return textPos{t.viewStart + t.viewRows - 1, endCol}
	}
	return textPos{t.viewStart + row - t.viewTop, col}
}

// selectionKey handles keys while a selection shows: Ctrl+C copies it
// (instead of interrupting), Shift+arrows extend it, Esc and paging keep
// it and do what they always do, and any other key clears it first.
func (t *TUI) selectionKey(data string) bool {
	if t.Mode != Fullscreen || !t.sel.active {
		return false
	}
	switch k := Key(data); k {
	case "ctrl+c":
		t.copySelection()
		t.sel = selection{}
		return true
	case "shift+left", "shift+right", "shift+up", "shift+down":
		t.moveHead(k)
		return true
	case "escape", "pageup", "pagedown":
		return false
	}
	t.sel = selection{}
	return false
}

func (t *TUI) copySelection() {
	if text := t.SelectedText(); text != "" && t.OnCopy != nil {
		t.OnCopy(text)
	}
}

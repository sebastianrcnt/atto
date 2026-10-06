package tui

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// minRenderInterval caps the frame rate (~60fps). Streaming tokens request a
// render per token; this coalesces them.
const minRenderInterval = 16 * time.Millisecond

const (
	syncBegin = "\x1b[?2026h" // synchronized output: terminal buffers until syncEnd
	syncEnd   = "\x1b[?2026l"
)

// Mode selects how the TUI uses the terminal.
type Mode int

const (
	// Fullscreen draws on the alternate screen: the footer is pinned to the
	// bottom and the body scrolls inside the remaining rows (mouse wheel,
	// PageUp/PageDown).
	Fullscreen Mode = iota
	// Inline renders into the main screen, so finished output flows into the
	// terminal's native scrollback. Only lines that changed since the previous
	// frame are rewritten; a full redraw happens only when the change is above
	// the visible viewport or the terminal size changes.
	Inline
)

// TUI renders a Body followed by a Footer.
//
// All component state must be mutated inside Do (or from input handlers,
// which already run under the lock).
type TUI struct {
	Body   Container
	Footer Container
	Mode   Mode

	// OnInput, if set, sees every input event before the focused component.
	// Returning true consumes the event. Runs under the TUI lock: do not call
	// Stop from here.
	OnInput func(data string) bool

	// ClearOnShrink forces a full redraw when content shrinks below the
	// largest height rendered so far, so no stale rows remain.
	ClearOnShrink bool

	// PaddingX is the blank margin, in columns, on both sides of all content.
	PaddingX int
	// GapY is the number of blank rows above the body and between the body
	// and the footer (fullscreen only).
	GapY int

	// Screen, if set, takes the whole screen in fullscreen mode instead of
	// the body and footer (input still goes to the focused component).
	Screen Screen

	// Pin, if set, may return a line to pin over the first visible body row
	// in fullscreen mode, given the index of the first visible body line —
	// e.g. the prompt that scrolled out of view (codex does this).
	Pin func(firstVisible, width int) string

	term    Terminal
	mu      sync.Mutex
	focused Component
	stopped bool
	started bool
	wake    chan struct{}
	done    chan struct{}

	// Render state from the previous frame.
	prevLines       []string
	prevWidth       int
	prevHeight      int
	hwCursorRow     int // row (in content coordinates) the terminal cursor is on
	maxLinesRender  int
	prevViewportTop int // content row shown at the top of the screen
	cursorVisible   bool

	// Fullscreen state.
	scroll      int // body lines scrolled up from the bottom
	prevBodyLen int
	viewTop     int  // first screen row showing body
	viewStart   int  // body line shown at viewTop
	viewRows    int  // screen rows showing body
	pinned      bool // the first body row shows the Pin line
	footerTop   int  // first screen row of the footer
	newBelow    bool // output arrived below the view while scrolled up
	prevFrame   []string
	bar         scrollbar // see scrollbar.go

	anchor anchorState // keeps the view put while the body changes, see anchor.go
	// Per-line work reused from the previous frame, see lineMemo.
	padMemo, prepMemo lineMemo

	// FullRepaint rewrites every visible row, from column 1, on every frame
	// that changes anything, instead of only the rows that differ. It trades
	// bandwidth for robustness on terminals that mishandle sparse positioned
	// writes (ConPTY-backed hosts such as Windows Terminal). Inline mode
	// repaints the bottom viewport of its content; rows already in scrollback
	// are never touched. New sets it from DefaultFullRepaint.
	FullRepaint bool

	// Colors is how many colors the terminal shows, for styles that blend
	// colors (the activity line's shimmer). New sets it from
	// DetectColorDepth; the rest of atto assumes 256 colors.
	Colors ColorDepth

	// FullRedraws counts full redraws; useful for tests and debugging.
	FullRedraws int

	// NoMouse leaves the mouse to the terminal: no wheel scrolling, clicks
	// or in-app selection, but the terminal's own selection works without
	// a modifier key. Set it before Start.
	NoMouse bool
	// OnCopy, if set, receives the text of a finished selection (on mouse
	// release, or Ctrl+C while a selection shows). Runs under the TUI lock.
	OnCopy func(text string)

	// Selection state (fullscreen only), see selection.go and mouse.go.
	sel      selection
	mouse    mouseState
	lastBody []string // body lines of the last fullscreen frame, padded
	// now and autoScrollEvery are replaced by tests.
	now             func() time.Time
	autoScrollEvery time.Duration
}

func New(term Terminal) *TUI {
	return &TUI{term: term, FullRepaint: DefaultFullRepaint(), Colors: DetectColorDepth(os.Getenv), wake: make(chan struct{}, 1), done: make(chan struct{})}
}

// DefaultFullRepaint reports whether full-repaint mode is on by default: on
// for Windows, off elsewhere, overridable with ATTO_FULL_REPAINT=1 / =0.
func DefaultFullRepaint() bool {
	return fullRepaintFor(runtime.GOOS, os.Getenv("ATTO_FULL_REPAINT"))
}

// AnimationInterval is how often a busy UI should request a render to move
// its animations: about 30 frames a second, so the activity line's shimmer
// glides; a frame that changes only that line writes only that line with
// the differential renderer. Full repaint rewrites the whole viewport per
// frame, so it ticks at 250ms. Animations derive their state from the
// elapsed time at render, never from counting ticks (see GlyphInterval).
func (t *TUI) AnimationInterval() time.Duration {
	if t.FullRepaint {
		return 250 * time.Millisecond
	}
	return 33 * time.Millisecond
}

// GlyphInterval is how long a spinner glyph shows before the next: 80ms,
// or one per tick in full repaint, which ticks slower (stepping by 80ms
// there skipped frames, and the spinner jerked). It is also the tick for
// what only needs a timer kept moving.
func (t *TUI) GlyphInterval() time.Duration {
	if t.FullRepaint {
		return 250 * time.Millisecond
	}
	return 80 * time.Millisecond
}

func fullRepaintFor(goos, env string) bool {
	switch strings.TrimSpace(env) {
	case "1":
		return true
	case "0":
		return false
	}
	return goos == "windows"
}

// Start enters raw mode, starts the render loop and performs the first render.
func (t *TUI) Start() error {
	if err := t.term.Start(t.handleInput, t.RequestRender); err != nil {
		return err
	}
	if t.Mode == Fullscreen {
		t.term.Write(t.enterFullscreen())
	}
	t.term.Write("\x1b[?25l")
	t.started = true
	go t.loop()
	t.RequestRender()
	return nil
}

// Stop renders a final frame, leaves the cursor below the content and
// restores the terminal.
func (t *TUI) Stop() {
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		return
	}
	t.doRender()
	t.stopped = true
	close(t.done)
	if t.Mode == Fullscreen {
		t.term.Write(t.leaveFullscreen())
	} else if n := len(t.prevLines); n > 0 {
		var b strings.Builder
		moveRows(&b, n-1-t.hwCursorRow)
		b.WriteString("\r\n")
		t.term.Write(b.String())
	}
	t.mu.Unlock()
	t.term.Stop()
}

// SetMode switches between fullscreen and inline rendering while running:
// it leaves or enters the alternate screen and mouse reporting, then makes
// the next frame a first frame in the new mode. Call it inside Do (or from
// an input handler). Before Start it only sets Mode.
func (t *TUI) SetMode(m Mode) {
	if t.Mode == m {
		return
	}
	t.Mode = m
	if !t.started {
		return
	}
	if m == Fullscreen {
		t.term.Write(t.enterFullscreen())
	} else {
		// The main screen returns with the cursor where it was left; the
		// first inline frame is drawn from there.
		t.term.Write(t.leaveFullscreen())
	}
	t.sel, t.mouse = selection{}, mouseState{}
	t.prevFrame, t.prevLines = nil, nil
	t.prevWidth, t.prevHeight = 0, 0
	t.hwCursorRow, t.prevViewportTop, t.maxLinesRender = 0, 0, 0
	t.scroll, t.prevBodyLen = 0, 0
}

// Do runs fn under the TUI lock and schedules a render.
func (t *TUI) Do(fn func()) {
	t.mu.Lock()
	fn()
	t.mu.Unlock()
	t.RequestRender()
}

// RequestRender schedules a render; multiple requests coalesce into one frame.
func (t *TUI) RequestRender() {
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

// RenderNow renders synchronously. Intended for tests and shutdown paths.
func (t *TUI) RenderNow() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.doRender()
}

// SetFocus moves keyboard focus to c (nil clears it).
func (t *TUI) SetFocus(c Component) {
	if f, ok := t.focused.(Focusable); ok {
		f.SetFocused(false)
	}
	t.focused = c
	if f, ok := c.(Focusable); ok {
		f.SetFocused(true)
	}
}

// OnPanic, if set, hears a panic in the render loop (with its stack) after
// the terminal is restored; the process then exits with status 2.
var OnPanic func(v any, stack []byte)

func (t *TUI) loop() {
	defer func() {
		if v := recover(); v != nil {
			stack := debug.Stack()
			if t.Mode == Fullscreen {
				t.term.Write(t.leaveFullscreen())
			}
			t.term.Stop()
			if OnPanic != nil {
				OnPanic(v, stack)
			}
			fmt.Fprintf(os.Stderr, "atto: panic: %v\n%s", v, stack)
			os.Exit(2)
		}
	}()
	var last time.Time
	for {
		select {
		case <-t.done:
			return
		case <-t.wake:
		}
		if d := minRenderInterval - time.Since(last); d > 0 {
			select {
			case <-t.done:
				return
			case <-time.After(d):
			}
		}
		t.mu.Lock()
		if !t.stopped {
			t.doRender()
		}
		t.mu.Unlock()
		last = time.Now()
	}
}

// Render returns the inline-mode content: body followed by footer.
func (t *TUI) Render(width int) []string {
	inner := t.innerWidth(width)
	return append(t.padBody(t.Body.Render(inner)), t.pad(t.Footer.Render(inner))...)
}

// padBody is pad for the body, whose lines mostly are those of the last
// frame.
func (t *TUI) padBody(lines []string) []string {
	if t.PaddingX <= 0 {
		return lines
	}
	margin := strings.Repeat(" ", t.PaddingX)
	return t.padMemo.apply(lines, t.PaddingX, func(l string) string {
		if l == "" {
			return l
		}
		return margin + l
	})
}

func (t *TUI) innerWidth(width int) int { return max(1, width-2*t.PaddingX) }

// pad indents lines by PaddingX.
func (t *TUI) pad(lines []string) []string {
	if t.PaddingX <= 0 {
		return lines
	}
	margin := strings.Repeat(" ", t.PaddingX)
	for i, l := range lines {
		if l != "" {
			lines[i] = margin + l
		}
	}
	return lines
}

// ScrollBy scrolls the fullscreen body up (n > 0) or down (n < 0).
func (t *TUI) ScrollBy(n int) { t.scroll = max(0, t.scroll+n) }

// ScrollToBottom jumps back to the newest output.
func (t *TUI) ScrollToBottom() { t.scroll = 0 }

// ScrollOffset reports how many body lines are hidden below the viewport.
func (t *TUI) ScrollOffset() int { return t.scroll }

// NewBelow reports whether output has arrived below the view since it was
// scrolled up; it clears when the view is back at the bottom.
func (t *TUI) NewBelow() bool { return t.newBelow }

// ForceRedraw repaints everything now; safe from any goroutine.
func (t *TUI) ForceRedraw() {
	t.mu.Lock()
	t.Redraw()
	t.mu.Unlock()
	t.RequestRender()
}

// Emit writes s to the terminal as is, between frames.
func (t *TUI) Emit(s string) { t.term.Write(s) }

// Redraw forces the next frame to repaint everything.
func (t *TUI) Redraw() {
	t.prevFrame = nil
	t.prevLines = nil
	t.prevWidth, t.prevHeight = -1, -1
	t.maxLinesRender = 0
}

func (t *TUI) handleInput(data string) {
	t.mu.Lock()
	consumed := (t.Screen == nil && (t.handleScroll(data) || t.selectionKey(data))) || (t.OnInput != nil && t.OnInput(data))
	if !consumed {
		if h, ok := t.focused.(InputHandler); ok {
			h.HandleInput(data)
		}
	}
	t.mu.Unlock()
	t.RequestRender()
}

func moveRows(b *strings.Builder, n int) {
	if n > 0 {
		fmt.Fprintf(b, "\x1b[%dB", n)
	} else if n < 0 {
		fmt.Fprintf(b, "\x1b[%dA", -n)
	}
}

type cursorPos struct{ row, col int }

// prepareLines flattens embedded newlines, extracts the cursor marker,
// truncates overflowing lines and appends a style reset to each line.
func (t *TUI) prepareLines(raw []string, width, height int) ([]string, *cursorPos) {
	lines := make([]string, 0, len(raw))
	for _, l := range raw {
		if strings.IndexByte(l, '\n') >= 0 {
			lines = append(lines, strings.Split(l, "\n")...)
		} else {
			lines = append(lines, l)
		}
	}

	var cur *cursorPos
	top := max(0, len(lines)-height)
	for row := len(lines) - 1; row >= top; row-- {
		if i := strings.Index(lines[row], CursorMarker); i >= 0 {
			cur = &cursorPos{row: row, col: VisibleWidth(lines[row][:i])}
			lines[row] = lines[row][:i] + lines[row][i+len(CursorMarker):]
			break
		}
	}

	return t.prepMemo.apply(lines, width, func(l string) string {
		l = StripWrapMarks(l)
		if strings.IndexByte(l, '\t') >= 0 {
			l = strings.ReplaceAll(l, "\t", "   ")
		}
		if VisibleWidth(l) > width {
			l = Truncate(l, width, "")
		}
		return l + Reset
	}), cur
}

func (t *TUI) doRender() {
	if t.Mode == Fullscreen {
		t.doRenderFullscreen()
		return
	}
	width, height := t.term.Size()
	widthChanged := t.prevWidth != 0 && t.prevWidth != width
	heightChanged := t.prevHeight != 0 && t.prevHeight != height

	prevBufLen := height
	if t.prevHeight > 0 {
		prevBufLen = t.prevViewportTop + t.prevHeight
	}
	prevViewportTop := t.prevViewportTop
	if heightChanged {
		prevViewportTop = max(0, prevBufLen-height)
	}
	viewportTop := prevViewportTop
	hw := t.hwCursorRow
	lineDiff := func(target int) int {
		return (target - viewportTop) - (hw - prevViewportTop)
	}

	newLines, cur := t.prepareLines(t.Render(width), width, height)
	prev := t.prevLines

	commit := func() {
		t.prevLines = newLines
		t.prevWidth = width
		t.prevHeight = height
	}

	fullRender := func(clear bool) {
		t.FullRedraws++
		var b strings.Builder
		b.WriteString(syncBegin)
		if clear {
			b.WriteString("\x1b[2J\x1b[H\x1b[3J") // clear screen, home, clear scrollback
		}
		b.WriteString(strings.Join(newLines, "\r\n"))
		if clear {
			t.maxLinesRender = len(newLines)
		} else {
			t.maxLinesRender = max(t.maxLinesRender, len(newLines))
		}
		t.hwCursorRow = max(0, len(newLines)-1)
		t.prevViewportTop = max(0, max(height, len(newLines))-height)
		t.positionCursor(&b, cur, len(newLines))
		b.WriteString(syncEnd)
		t.term.Write(b.String())
		commit()
	}

	// First frame: assume a clean area below the shell prompt.
	if len(prev) == 0 && !widthChanged && !heightChanged {
		fullRender(false)
		return
	}
	// Wrapping depends on width, and the viewport alignment on height.
	if widthChanged || heightChanged {
		fullRender(true)
		return
	}
	if t.ClearOnShrink && len(newLines) < t.maxLinesRender {
		fullRender(true)
		return
	}

	first, last := -1, -1
	for i := 0; i < max(len(newLines), len(prev)); i++ {
		var o, n string
		if i < len(prev) {
			o = prev[i]
		}
		if i < len(newLines) {
			n = newLines[i]
		}
		if o != n {
			if first == -1 {
				first = i
			}
			last = i
		}
	}
	appended := len(newLines) > len(prev)
	if appended {
		if first == -1 {
			first = len(prev)
		}
		last = len(newLines) - 1
	}
	appendStart := appended && first == len(prev) && first > 0

	if first == -1 {
		var b strings.Builder
		t.positionCursor(&b, cur, len(newLines))
		if b.Len() > 0 {
			t.term.Write(b.String())
		}
		return
	}

	// Only deletions at the tail: clear the stale rows.
	if first >= len(newLines) {
		target := max(0, len(newLines)-1)
		extra := len(prev) - len(newLines)
		if target < prevViewportTop || extra > height {
			fullRender(true)
			return
		}
		var b strings.Builder
		b.WriteString(syncBegin)
		moveRows(&b, lineDiff(target))
		b.WriteString("\r")
		offset := 0
		if len(newLines) > 0 {
			offset = 1
			b.WriteString("\x1b[1B")
		}
		for i := range extra {
			b.WriteString("\r\x1b[2K")
			if i < extra-1 {
				b.WriteString("\x1b[1B")
			}
		}
		moveRows(&b, -(extra - 1 + offset))
		t.hwCursorRow = target
		t.positionCursor(&b, cur, len(newLines))
		b.WriteString(syncEnd)
		t.term.Write(b.String())
		t.prevViewportTop = prevViewportTop
		commit()
		return
	}

	// Rows above the viewport live in scrollback and cannot be rewritten.
	if first < prevViewportTop {
		fullRender(true)
		return
	}

	// Full repaint: rewrite every visible content row, not just the changed
	// ones, so ConPTY never has to merge a sparse update with older cells.
	if t.FullRepaint {
		first, last = prevViewportTop, len(newLines)-1
		appendStart = false
	}

	var b strings.Builder
	b.WriteString(syncBegin)
	prevViewportBottom := prevViewportTop + height - 1
	moveTarget := first
	if appendStart {
		moveTarget = first - 1
	}
	if moveTarget > prevViewportBottom {
		// Scroll so the target row becomes visible.
		screenRow := min(height-1, max(0, hw-prevViewportTop))
		moveRows(&b, height-1-screenRow)
		scroll := moveTarget - prevViewportBottom
		b.WriteString(strings.Repeat("\r\n", scroll))
		prevViewportTop += scroll
		viewportTop += scroll
		hw = moveTarget
	}
	moveRows(&b, lineDiff(moveTarget))
	if appendStart {
		b.WriteString("\r\n")
	} else {
		b.WriteString("\r")
	}

	renderEnd := min(last, len(newLines)-1)
	for i := first; i <= renderEnd; i++ {
		if i > first {
			b.WriteString("\r\n")
		}
		b.WriteString("\x1b[2K")
		b.WriteString(newLines[i])
	}
	finalRow := renderEnd

	if len(prev) > len(newLines) {
		if renderEnd < len(newLines)-1 {
			moveRows(&b, len(newLines)-1-renderEnd)
			finalRow = len(newLines) - 1
		}
		extra := len(prev) - len(newLines)
		for range extra {
			b.WriteString("\r\n\x1b[2K")
		}
		moveRows(&b, -extra)
	}

	t.hwCursorRow = finalRow
	t.maxLinesRender = max(t.maxLinesRender, len(newLines))
	t.prevViewportTop = max(prevViewportTop, finalRow-height+1)
	t.positionCursor(&b, cur, len(newLines))
	b.WriteString(syncEnd)
	t.term.Write(b.String())
	commit()
}

// positionCursor moves the hardware cursor to the cursor marker, or hides it
// when no component is showing one.
func (t *TUI) positionCursor(b *strings.Builder, cur *cursorPos, total int) {
	if cur == nil || total <= 0 {
		if t.cursorVisible {
			b.WriteString("\x1b[?25l")
			t.cursorVisible = false
		}
		return
	}
	row := min(max(cur.row, 0), total-1)
	moveRows(b, row-t.hwCursorRow)
	fmt.Fprintf(b, "\x1b[%dG", max(cur.col, 0)+1)
	t.hwCursorRow = row
	if !t.cursorVisible {
		b.WriteString("\x1b[?25h")
		t.cursorVisible = true
	}
}

// doRenderFullscreen composes a full frame (body window + pinned footer) and
// rewrites only the screen rows that changed (every row with FullRepaint).
func (t *TUI) doRenderFullscreen() {
	width, height := t.term.Size()
	if t.Screen != nil {
		frame := t.Screen.RenderScreen(width, height)
		if len(frame) > height {
			frame = frame[:height]
		}
		for len(frame) < height {
			frame = append(frame, "")
		}
		lines, cur := t.prepareLines(frame, width, height)
		t.writeFrame(lines, cur, width, height)
		return
	}
	inner := t.fullscreenWidth(width)
	anchor, anchored := t.anchorBefore()
	body := t.padBody(t.Body.Render(inner))

	// Keep the view anchored while scrolled up and the body changes height:
	// to the line at its top (see anchor.go), else to the bottom, for output
	// arriving below.
	if t.scroll > 0 && len(body) != t.prevBodyLen {
		was := t.scroll
		if s, ok := t.anchorScroll(anchor, len(body)); anchored && ok {
			t.scroll = s
		} else if len(body) > t.prevBodyLen {
			t.scroll += len(body) - t.prevBodyLen
		}
		if t.scroll > was {
			t.newBelow = true
		}
	}
	t.prevBodyLen = len(body)

	// The footer may show something while scrolled up (a "jump to bottom"
	// pill) and so change height with it; render it again if clamping the
	// scroll to the body changed that.
	var footer []string
	var gap, avail, end, start int
	for range 2 {
		scrolled := t.scroll > 0
		footer = t.pad(t.Footer.Render(inner))
		if len(footer) > height {
			footer = footer[len(footer)-height:]
		}
		gap = min(t.GapY, max(0, (height-len(footer))/4))
		avail = max(0, height-len(footer)-2*gap)
		t.scroll = min(t.scroll, max(0, len(body)-avail))
		if (t.scroll > 0) == scrolled {
			break
		}
	}
	if t.scroll == 0 {
		t.newBelow = false
	}
	end = len(body) - t.scroll
	start = max(0, end-avail)
	t.footerTop = height - len(footer)
	t.anchor.scroll = t.scroll

	t.viewTop, t.viewStart, t.viewRows = gap, start, end-start
	t.lastBody = body
	t.syncSelection(width)
	frame := make([]string, gap, height)
	frame = append(frame, body[start:end]...)
	t.highlight(frame[gap:], start)
	t.pinned = false
	// No pinned prompt while selecting: the row must show the text that
	// is being selected.
	if t.Pin != nil && end > start && !t.selecting() {
		if p := t.Pin(start, inner); p != "" {
			frame[gap] = t.pad([]string{p})[0]
			t.pinned = true
		}
	}
	for len(frame) < avail+2*gap {
		frame = append(frame, "")
	}
	t.decorate(frame, gap, start, end-start, len(body), width)
	frame = append(frame, footer...)
	lines, cur := t.prepareLines(frame, width, height)
	t.writeFrame(lines, cur, width, height)
}

// writeFrame puts a fullscreen frame on the terminal, rewriting only the
// rows that changed (every row with FullRepaint).
func (t *TUI) writeFrame(lines []string, cur *cursorPos, width, height int) {
	full := t.prevWidth != width || t.prevHeight != height || len(t.prevFrame) != len(lines)
	changed := full
	for i := 0; !changed && i < len(lines); i++ {
		changed = t.prevFrame[i] != lines[i]
	}
	var b strings.Builder
	if full {
		t.FullRedraws++
		b.WriteString("\x1b[2J")
	}
	for i, l := range lines {
		// Full repaint rewrites every row of a frame that changed; each row
		// is cleared first, then the whole line is written from column 1.
		if full || (t.FullRepaint && changed) || t.prevFrame[i] != l {
			fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K%s", i+1, l)
		}
	}
	if cur != nil {
		fmt.Fprintf(&b, "\x1b[%d;%dH", cur.row+1, cur.col+1)
		if !t.cursorVisible {
			b.WriteString("\x1b[?25h")
			t.cursorVisible = true
		}
	} else if t.cursorVisible {
		b.WriteString("\x1b[?25l")
		t.cursorVisible = false
	}
	t.prevFrame = lines
	t.prevWidth, t.prevHeight = width, height
	if b.Len() > 0 {
		t.term.Write(syncBegin + b.String() + syncEnd)
	}
}

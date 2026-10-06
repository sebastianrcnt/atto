package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// selRig is a fullscreen TUI on the test terminal with simulated mouse
// input and a fake clock.
type selRig struct {
	ui     *TUI
	v      *logTerm
	body   *clicky
	copied []string
	input  []string // what reached OnInput
	clock  time.Time
}

func newSelRig(t *testing.T, w, h int, body ...string) *selRig {
	t.Helper()
	r := &selRig{v: &logTerm{vterm: newVterm(w, h)}, clock: time.Unix(1000, 0)}
	r.ui = New(r.v)
	r.ui.FullRepaint = false
	r.ui.now = func() time.Time { return r.clock }
	r.ui.autoScrollEvery = time.Hour // tests step auto-scroll by hand
	r.body = &clicky{l: body}
	r.ui.Body.Add(r.body)
	r.ui.Footer.Add(&lines{l: []string{"foot"}})
	r.ui.OnCopy = func(s string) { r.copied = append(r.copied, s) }
	r.ui.OnInput = func(d string) bool { r.input = append(r.input, d); return true }
	r.ui.RenderNow()
	return r
}

func (r *selRig) send(format string, args ...any) {
	r.ui.handleInput(fmt.Sprintf(format, args...))
}

// Cells are 1-based, as the terminal reports them.
func (r *selRig) press(x, y int)   { r.send("\x1b[<0;%d;%dM", x, y) }
func (r *selRig) move(x, y int)    { r.send("\x1b[<32;%d;%dM", x, y) }
func (r *selRig) release(x, y int) { r.send("\x1b[<0;%d;%dm", x, y) }
func (r *selRig) click(x, y int)   { r.press(x, y); r.release(x, y) }
func (r *selRig) later()           { r.clock = r.clock.Add(time.Second) }

func (r *selRig) drag(x0, y0, x1, y1 int) {
	r.later()
	r.press(x0, y0)
	r.move(x1, y1)
	r.release(x1, y1)
	r.ui.RenderNow()
}

func (r *selRig) lastCopy(t *testing.T) string {
	t.Helper()
	if len(r.copied) == 0 {
		t.Fatal("nothing was copied")
	}
	return r.copied[len(r.copied)-1]
}

// reversed returns, per screen row of the last frame, the text drawn in
// reverse video.
func (r *selRig) reversed() []string {
	out := make([]string, len(r.ui.prevFrame))
	for i, l := range r.ui.prevFrame {
		var b strings.Builder
		on := false
		cs, _ := cells(l)
		for _, c := range cs {
			for j := 0; j < len(c.esc); {
				n := escapeLen(c.esc, j)
				switch c.esc[j : j+n] {
				case "\x1b[7m":
					on = true
				case "\x1b[27m", "\x1b[0m":
					on = false
				}
				j += n
			}
			if on {
				b.WriteString(c.text)
			}
		}
		out[i] = b.String()
	}
	return out
}

func TestSelectionDrag(t *testing.T) {
	r := newSelRig(t, 20, 5, "hello world", "second line")
	r.drag(1, 1, 5, 2)
	if got := r.lastCopy(t); got != "hello world\nsecon" {
		t.Fatalf("copied %q", got)
	}
	if got := r.reversed(); got[0] != "hello world" || got[1] != "secon" || got[2] != "" {
		t.Fatalf("highlight %q", got)
	}
	if len(r.body.clicked) != 0 {
		t.Fatalf("a drag clicked %v", r.body.clicked)
	}
	// Backwards: the press cell stays selected.
	r.drag(5, 2, 3, 1)
	if got := r.lastCopy(t); got != "llo world\nsecon" {
		t.Fatalf("backwards copied %q", got)
	}
	// Past the right end of a line takes the rest of it.
	r.drag(7, 1, 20, 1)
	if got := r.lastCopy(t); got != "world" {
		t.Fatalf("to the end copied %q", got)
	}
}

func TestClickVersusDrag(t *testing.T) {
	r := newSelRig(t, 20, 5, "aaaa", "bbbb", "cccc")
	r.click(2, 2)
	if !slices.Equal(r.body.clicked, []int{1}) || len(r.copied) != 0 {
		t.Fatalf("click: clicked %v copied %q", r.body.clicked, r.copied)
	}
	// Moving away and back is not a click and selects nothing.
	r.later()
	r.press(2, 2)
	r.move(3, 2)
	r.move(2, 2)
	r.release(2, 2)
	if len(r.body.clicked) != 1 || len(r.copied) != 0 || r.ui.sel.active {
		t.Fatalf("wiggle: clicked %v copied %q", r.body.clicked, r.copied)
	}
	// A drag never toggles.
	r.drag(1, 1, 3, 3)
	if len(r.body.clicked) != 1 || r.lastCopy(t) != "aaaa\nbbbb\nccc" {
		t.Fatalf("drag: clicked %v copied %q", r.body.clicked, r.copied)
	}
	// With a selection showing, a click only clears it.
	r.later()
	r.click(2, 2)
	r.ui.RenderNow()
	if len(r.body.clicked) != 1 || r.ui.sel.active || r.reversed()[0] != "" {
		t.Fatalf("clearing click: clicked %v active %v", r.body.clicked, r.ui.sel.active)
	}
	r.later()
	r.click(2, 2)
	if len(r.body.clicked) != 2 {
		t.Fatalf("next click goes through: %v", r.body.clicked)
	}
}

func TestSelectWordAndLine(t *testing.T) {
	text := "see /usr/local/bin/atto, then https://example.com/a?b=1."
	r := newSelRig(t, 80, 5, text, "next")
	dbl := func(x, y int) {
		r.later()
		r.click(x, y)
		r.click(x, y)
	}
	dbl(10, 1) // in "local"
	if got := r.lastCopy(t); got != "/usr/local/bin/atto" {
		t.Fatalf("path: %q", got)
	}
	dbl(strings.Index(text, "example")+1, 1)
	if got := r.lastCopy(t); got != "https://example.com/a?b=1" {
		t.Fatalf("url: %q", got)
	}
	dbl(2, 1)
	if got := r.lastCopy(t); got != "see" {
		t.Fatalf("word: %q", got)
	}
	n := len(r.copied)
	r.later()
	r.click(2, 1)
	r.click(2, 1)
	r.click(2, 1)
	if got := r.lastCopy(t); got != text || len(r.copied) != n+2 {
		t.Fatalf("triple click: %q (%d copies)", got, len(r.copied)-n)
	}
	if len(r.body.clicked) != 1 {
		// Only the first click of the first series is a click: later
		// ones start while a selection shows, and only clear it.
		t.Fatalf("clicks %v", r.body.clicked)
	}
	// A double click then drag extends by words.
	r.later()
	r.click(2, 1)
	r.press(2, 1)
	r.move(32, 1) // in "https"
	r.release(32, 1)
	if got := r.lastCopy(t); got != "see /usr/local/bin/atto, then https://example.com/a?b=1" {
		t.Fatalf("word drag: %q", got)
	}
}

func TestSelectWideChars(t *testing.T) {
	r := newSelRig(t, 20, 5, "한글 text", "x")
	// Column 2 is the right half of 한, column 3 the left half of 글.
	r.drag(2, 1, 3, 1)
	if got := r.lastCopy(t); got != "한글" {
		t.Fatalf("copied %q", got)
	}
	if got := r.reversed()[0]; got != "한글" {
		t.Fatalf("highlight %q", got)
	}
	r.later()
	r.click(3, 1)
	r.click(3, 1)
	if got := r.lastCopy(t); got != "한글" {
		t.Fatalf("double click %q", got)
	}
	r.ui.RenderNow()
	if got := r.v.screenRows()[0]; got != "한글 text" {
		t.Fatalf("screen %q", got)
	}
}

func TestSelectJoinsSoftWraps(t *testing.T) {
	var body []string
	for i, l := range Wrap("the quick brown fox jumps over", 10) {
		lead := "  "
		if i == 0 {
			lead = "• "
		}
		body = append(body, lead+l)
	}
	for _, l := range WrapHard("abcdefghij", 4) {
		body = append(body, "│ "+l)
	}
	body = append(body, "plain one", "plain two")
	r := newSelRig(t, 20, 12, body...)
	if got := r.v.screenRows()[1]; got != "  brown fox" {
		t.Fatalf("marks reached the screen: %q", got)
	}
	r.drag(1, 1, 20, 3)
	if got := r.lastCopy(t); got != "• the quick brown fox jumps over" {
		t.Fatalf("word wrap: %q", got)
	}
	r.drag(3, 4, 20, 6)
	if got := r.lastCopy(t); got != "abcdefghij" {
		t.Fatalf("hard wrap: %q", got)
	}
	r.drag(1, 7, 20, 8)
	if got := r.lastCopy(t); got != "plain one\nplain two" {
		t.Fatalf("separate lines: %q", got)
	}
	// Triple click takes the whole wrapped line.
	r.later()
	r.click(4, 2)
	r.click(4, 2)
	r.click(4, 2)
	if got := r.lastCopy(t); got != "• the quick brown fox jumps over" {
		t.Fatalf("triple: %q", got)
	}
	// A split word is one word.
	r.later()
	r.click(4, 5)
	r.click(4, 5)
	if got := r.lastCopy(t); got != "abcdefghij" {
		t.Fatalf("split word: %q", got)
	}
}

func TestSelectDedentsAndSkipsMargin(t *testing.T) {
	r := newSelRig(t, 30, 8, "    if x {", "        y()", "    }")
	r.ui.PaddingX = 1
	r.ui.RenderNow()
	r.drag(1, 1, 30, 3)
	if got := r.lastCopy(t); got != "if x {\n    y()\n}" {
		t.Fatalf("copied %q", got)
	}
}

func TestSelectScrollDuringDrag(t *testing.T) {
	var body []string
	for i := range 20 {
		body = append(body, fmt.Sprintf("line %d", i))
	}
	r := newSelRig(t, 20, 6, body...) // lines 15-19 visible, then the footer
	r.later()
	r.press(1, 3) // line 17
	r.move(1, 1)  // the top row: scrolls one line at once
	r.ui.RenderNow()
	if r.ui.viewStart != 14 || r.ui.sel.lo != (textPos{14, 0}) {
		t.Fatalf("view %d, sel %+v", r.ui.viewStart, r.ui.sel)
	}
	// Held there, the ticker keeps scrolling and the end follows.
	r.ui.autoScrollStep()
	r.ui.autoScrollStep()
	r.ui.RenderNow()
	if r.ui.sel.lo != (textPos{12, 0}) {
		t.Fatalf("auto-scroll: %+v", r.ui.sel)
	}
	// The wheel scrolls under the drag too.
	r.send("\x1b[<64;1;1M")
	r.ui.RenderNow()
	if r.ui.sel.lo != (textPos{9, 0}) {
		t.Fatalf("wheel: %+v", r.ui.sel)
	}
	r.move(1, 2) // back inside: scrolling stops
	if r.ui.autoScrollStep() {
		t.Fatal("still auto-scrolling inside the view")
	}
	r.release(1, 2)
	want := strings.Join(body[10:17], "\n") + "\nl"
	if got := r.lastCopy(t); got != want {
		t.Fatalf("copied %q want %q", got, want)
	}
	// The selection stays on its text as the view moves.
	r.ui.ScrollToBottom()
	r.ui.RenderNow()
	if got := r.reversed(); got[2] != "l" || got[3] != "" {
		t.Fatalf("after scrolling: %q", got)
	}
	// Past the bottom edge scrolls down.
	r.ui.ScrollBy(5)
	r.ui.RenderNow()
	r.later()
	r.press(1, 1)
	r.move(1, 6) // the footer row
	if r.ui.mouse.autoDir != 1 {
		t.Fatalf("autoDir %d", r.ui.mouse.autoDir)
	}
	r.release(1, 6)
}

func TestSelectionKeys(t *testing.T) {
	r := newSelRig(t, 20, 5, "hello world", "second")
	r.send("\x03")
	if len(r.copied) != 0 || !slices.Equal(r.input, []string{"\x03"}) {
		t.Fatal("without a selection Ctrl+C goes to the app")
	}
	r.drag(1, 1, 5, 1)
	r.send("\x1b[1;2C") // shift+right
	r.send("\x1b[1;2B") // shift+down
	r.send("\x1b")      // esc: keeps the selection, still reaches the app
	r.send("\x1b[5~")   // pageup: keeps it
	r.ui.RenderNow()
	if !r.ui.sel.active {
		t.Fatal("selection lost")
	}
	r.input = nil
	r.send("\x03")
	if len(r.input) != 0 || r.lastCopy(t) != "hello world\nsecond" {
		t.Fatalf("ctrl+c: input %q copied %q", r.input, r.copied)
	}
	if r.ui.sel.active {
		t.Fatal("ctrl+c clears the selection")
	}
	r.drag(1, 1, 5, 1)
	r.send("\x1b[1;2D") // shift+left
	r.send("x")
	if r.ui.sel.active || !slices.Equal(r.input, []string{"x"}) {
		t.Fatalf("other keys clear the selection and pass on: %q", r.input)
	}
}

func TestSelectionHighlightBothRepaintModes(t *testing.T) {
	for _, full := range []bool{false, true} {
		r := newSelRig(t, 20, 5, "abc", "def")
		r.ui.FullRepaint = full
		r.ui.RenderNow()
		r.v.out.Reset()
		r.drag(2, 1, 2, 2)
		out := r.v.out.String()
		if !strings.Contains(out, "\x1b[7m") {
			t.Fatalf("full=%v: no highlight written: %q", full, out)
		}
		if got := r.reversed(); got[0] != "bc" || got[1] != "de" {
			t.Fatalf("full=%v: %q", full, got)
		}
		r.v.out.Reset()
		r.later()
		r.click(1, 1) // clears
		r.ui.RenderNow()
		if !strings.Contains(r.v.out.String(), "abc") {
			t.Fatalf("full=%v: clearing did not redraw: %q", full, r.v.out.String())
		}
	}
}

func TestFooterClickWithSelection(t *testing.T) {
	r := newSelRig(t, 20, 5, "abc")
	foot := &clicky{l: []string{"button"}}
	r.ui.Footer.Children = []Component{foot}
	r.ui.RenderNow()
	r.drag(1, 1, 3, 1)
	r.later()
	r.click(1, 5)
	if !slices.Equal(foot.clicked, []int{0}) || r.ui.sel.active {
		t.Fatalf("footer clicked %v, selection %v", foot.clicked, r.ui.sel.active)
	}
}

func TestPinnedRowClick(t *testing.T) {
	var body []string
	for i := range 20 {
		body = append(body, fmt.Sprintf("line %d", i))
	}
	r := newSelRig(t, 20, 6, body...)
	r.ui.Pin = func(int, int) string { return "PIN" }
	r.ui.RenderNow()
	if noBar(r.v.screenRows())[0] != "PIN" {
		t.Fatalf("pin %q", r.v.screenRows())
	}
	r.later()
	r.click(1, 1)
	if r.ui.ScrollOffset() == 0 || len(r.body.clicked) != 0 {
		t.Fatalf("pin click: scroll %d clicked %v", r.ui.ScrollOffset(), r.body.clicked)
	}
	// While selecting, the row shows the line under it.
	r.drag(1, 1, 3, 2)
	if noBar(r.v.screenRows())[0] == "PIN" {
		t.Fatal("pin shown over a selection")
	}
}

func TestNoMouse(t *testing.T) {
	ui := New(newVterm(10, 3))
	ui.NoMouse = true
	if s := ui.enterFullscreen() + ui.leaveFullscreen(); strings.Contains(s, "?1000") || strings.Contains(s, "?1002") {
		t.Fatalf("mouse modes with NoMouse: %q", s)
	}
	ui.NoMouse = false
	if s := ui.enterFullscreen(); !strings.Contains(s, "\x1b[?1002h") {
		t.Fatalf("no button-motion tracking: %q", s)
	}
}

func TestSelectionClearedOnResize(t *testing.T) {
	r := newSelRig(t, 20, 5, "hello world")
	r.drag(1, 1, 5, 1)
	r.v.resize(15, 5)
	r.ui.RenderNow()
	if r.ui.sel.active {
		t.Fatal("selection kept across a width change")
	}
}

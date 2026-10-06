package tui

import (
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"
)

// lines is a mutable component for driving the renderer in tests.
type lines struct{ l []string }

func (c *lines) Render(int) []string { return slices.Clone(c.l) }

func setup(w, h int) (*TUI, *vterm, *lines) {
	v := newVterm(w, h)
	ui := New(v)
	ui.Mode = Inline
	c := &lines{}
	ui.Body.Add(c)
	return ui, v, c
}

func assertTranscript(t *testing.T, v *vterm, want []string, step string) {
	t.Helper()
	got := v.rows()
	if !slices.Equal(got, want) {
		t.Fatalf("%s: transcript mismatch\n got (%d): %q\nwant (%d): %q", step, len(got), got, len(want), want)
	}
}

func TestAppendDoesNotRedraw(t *testing.T) {
	ui, v, c := setup(20, 5)
	for i := range 30 {
		c.l = append(c.l, fmt.Sprintf("line %d", i))
		ui.RenderNow()
		assertTranscript(t, v, c.l, fmt.Sprintf("append %d", i))
	}
	if ui.FullRedraws != 1 { // only the first frame
		t.Fatalf("expected 1 full redraw, got %d", ui.FullRedraws)
	}
}

func TestStreamingLastLine(t *testing.T) {
	ui, v, c := setup(20, 4)
	c.l = []string{"a", "b", "c", "d", "e", "f", ""}
	ui.RenderNow()
	for _, tok := range []string{"he", "llo", " wor", "ld"} {
		c.l[len(c.l)-1] += tok
		ui.RenderNow()
		assertTranscript(t, v, c.l, "stream "+tok)
	}
	if ui.FullRedraws != 1 {
		t.Fatalf("streaming caused full redraws: %d", ui.FullRedraws)
	}
}

func TestChangeAboveViewportRedraws(t *testing.T) {
	ui, v, c := setup(20, 3)
	c.l = []string{"0", "1", "2", "3", "4", "5"}
	ui.RenderNow()
	c.l[0] = "zero"
	ui.RenderNow()
	assertTranscript(t, v, c.l, "edit scrollback")
	if ui.FullRedraws != 2 {
		t.Fatalf("expected a full redraw, got %d", ui.FullRedraws)
	}
}

func TestShrink(t *testing.T) {
	ui, v, c := setup(20, 10)
	c.l = []string{"a", "b", "c", "d", "e"}
	ui.RenderNow()
	c.l = c.l[:2]
	ui.RenderNow()
	assertTranscript(t, v, c.l, "shrink")
	c.l = append(c.l, "x")
	ui.RenderNow()
	assertTranscript(t, v, c.l, "regrow")
}

func TestWideAndStyledLinesAreTruncated(t *testing.T) {
	ui, v, c := setup(10, 5)
	c.l = []string{FG(2, "abcdefghijklmnop"), "한글은두칸씩차지함"}
	ui.RenderNow()
	assertTranscript(t, v, []string{"abcdefghij", "한글은두칸"}, "truncate")
}

func TestCursorMarker(t *testing.T) {
	ui, v, c := setup(20, 5)
	c.l = []string{"hello", "> ab" + CursorMarker + "c", "status"}
	ui.RenderNow()
	assertTranscript(t, v, []string{"hello", "> abc", "status"}, "marker stripped")
	if v.r != 1 || v.c != 4 || !v.cursorOn {
		t.Fatalf("cursor at (%d,%d) visible=%v, want (1,4) visible", v.r, v.c, v.cursorOn)
	}
	// A subsequent append must still land correctly with the cursor parked mid-content.
	c.l = []string{"hello", "world", "> abc" + CursorMarker, "status"}
	ui.RenderNow()
	assertTranscript(t, v, []string{"hello", "world", "> abc", "status"}, "after cursor")
}

// TestRandomOperations checks the core invariant: whatever sequence of edits
// happens, scrollback + screen always equals the rendered lines.
func TestRandomOperations(t *testing.T) {
	for seed := range int64(200) {
		rng := rand.New(rand.NewSource(seed))
		w, h := 30, 3+rng.Intn(8)
		ui, v, c := setup(w, h)
		gen := 0
		next := func() string { gen++; return fmt.Sprintf("l%d", gen) }
		for step := range 60 {
			switch op := rng.Intn(10); {
			case op < 3: // append a few
				for k := rng.Intn(4) + 1; k > 0; k-- {
					c.l = append(c.l, next())
				}
			case op < 5 && len(c.l) > 0: // edit near the tail (streaming)
				i := len(c.l) - 1 - rng.Intn(min(len(c.l), h))
				c.l[i] = next()
			case op < 6 && len(c.l) > 0: // edit anywhere
				c.l[rng.Intn(len(c.l))] = next()
			case op < 8 && len(c.l) > 0: // delete tail
				c.l = c.l[:len(c.l)-rng.Intn(min(len(c.l), h)+1)]
			case op < 9: // replace tail block (spinner disappears, text arrives)
				cut := len(c.l) - rng.Intn(min(len(c.l), h)+1)
				c.l = c.l[:cut]
				for k := rng.Intn(3); k > 0; k-- {
					c.l = append(c.l, next())
				}
			default: // resize
				nw, nh := 20+rng.Intn(20), 3+rng.Intn(8)
				v.resize(nw, nh)
			}
			ui.RenderNow()
			assertTranscript(t, v, c.l, fmt.Sprintf("seed %d step %d", seed, step))
		}
	}
}

func TestWrap(t *testing.T) {
	cases := []struct {
		in    string
		width int
		want  []string
	}{
		// Continuation lines carry a soft-wrap mark: ws after a space,
		// wj inside a word.
		{"hello world foo", 11, []string{"hello world", wrapSpace + "foo"}},
		{"abcdefghij", 4, []string{"abcd", wrapJoin + "efgh", wrapJoin + "ij"}},
		{"a\n\nb", 5, []string{"a", "", "b"}},
		{"안녕하세요 세계", 6, []string{"안녕하", wrapJoin + "세요", wrapSpace + "세계"}},
	}
	for _, tc := range cases {
		got := Wrap(tc.in, tc.width)
		if !slices.Equal(got, tc.want) {
			t.Errorf("Wrap(%q,%d) = %q, want %q", tc.in, tc.width, got, tc.want)
		}
		for _, l := range got {
			if VisibleWidth(l) > tc.width {
				t.Errorf("line %q exceeds width %d", l, tc.width)
			}
		}
	}
}

func TestWrapCarriesStyle(t *testing.T) {
	got := Wrap(FG(1, "red red red"), 3)
	for i, l := range got {
		if !strings.Contains(l, "\x1b[38;5;1m") {
			t.Errorf("line %d lost its color: %q", i, l)
		}
	}
}

func TestInputParser(t *testing.T) {
	var p inputParser
	got := p.feed("a\x1b[A한\x1b[20")
	got = append(got, p.feed("0~pasted\ntext\x1b[201~\r")...)
	want := []string{"a", "\x1b[A", "한", PastePrefix + "pasted\ntext", "\r"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestEditor(t *testing.T) {
	e := NewEditor("> ")
	var submitted string
	e.OnSubmit = func(s string, _ []Attachment) { submitted = s }
	for _, k := range []string{"h", "i", "\x1b\r", "y", "o", "\x1b[D", "\x7f", "\r"} {
		e.HandleInput(k)
	}
	if submitted != "hi\no" {
		t.Fatalf("submitted %q", submitted)
	}
	e.HandleInput("\x1b[A")
	if e.Text() != "hi\no" {
		t.Fatalf("history recall got %q", e.Text())
	}
}

func TestFullscreen(t *testing.T) {
	v := newVterm(20, 5)
	ui := New(v)
	body := &lines{}
	foot := &lines{l: []string{"> in" + CursorMarker, "status"}}
	ui.Body.Add(body)
	ui.Footer.Add(foot)

	body.l = []string{"a", "b"}
	ui.RenderNow()
	want := []string{"a", "b", "", "> in", "status"}
	if got := v.screenRows(); !slices.Equal(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
	if v.r != 3 || v.c != 4 {
		t.Fatalf("cursor at (%d,%d)", v.r, v.c)
	}

	body.l = []string{"a", "b", "c", "d", "e"}
	ui.RenderNow()
	want = []string{"c", "d", "e", "> in", "status"}
	if got := noBar(v.screenRows()); !slices.Equal(got, want) {
		t.Fatalf("overflow: got %q want %q", got, want)
	}

	// Scroll up, then new output must not move the view.
	ui.ScrollBy(1)
	ui.RenderNow()
	body.l = append(body.l, "f", "g")
	ui.RenderNow()
	want = []string{"b", "c", "d", "> in", "status"}
	if got := noBar(v.screenRows()); !slices.Equal(got, want) {
		t.Fatalf("anchored: got %q want %q", got, want)
	}
	ui.ScrollToBottom()
	ui.RenderNow()
	want = []string{"e", "f", "g", "> in", "status"}
	if got := noBar(v.screenRows()); !slices.Equal(got, want) {
		t.Fatalf("bottom: got %q want %q", got, want)
	}
	if ui.FullRedraws != 1 {
		t.Fatalf("full redraws %d", ui.FullRedraws)
	}
}

type clicky struct {
	lines
	clicked []int
}

func (c *clicky) Click(line int) bool { c.clicked = append(c.clicked, line); return true }

func TestFullscreenClick(t *testing.T) {
	v := newVterm(20, 6)
	ui := New(v)
	a := &clicky{l: []string{"a0", "a1", "a2"}}
	b := &clicky{l: []string{"b0", "b1", "b2"}}
	ui.Body.Add(a, b)
	ui.Footer.Add(&lines{l: []string{"footer"}})
	ui.RenderNow() // 6 lines of body, 5 rows available: shows a1..b2
	click := func(x, y int) {
		ui.handleScroll(fmt.Sprintf("\x1b[<0;%d;%dM", x, y))
		ui.handleScroll(fmt.Sprintf("\x1b[<0;%d;%dm", x, y))
	}
	click(3, 1) // row 1 -> a1
	ui.handleScroll("\x1b[<0;3;4M")
	if len(b.clicked) != 0 {
		t.Fatal("a click acts on release, not on press")
	}
	ui.handleScroll("\x1b[<0;3;4m") // row 4 -> b1
	click(3, 6)                     // footer row: not the body
	if !slices.Equal(a.clicked, []int{1}) || !slices.Equal(b.clicked, []int{1}) {
		t.Fatalf("a=%v b=%v", a.clicked, b.clicked)
	}
}

func TestParseMouse(t *testing.T) {
	cases := []struct {
		in   string
		want mouseEvent
		ok   bool
	}{
		{"\x1b[<0;5;7M", mouseEvent{btn: 0, x: 5, y: 7, press: true}, true},
		{"\x1b[<0;5;7m", mouseEvent{btn: 0, x: 5, y: 7}, true},
		{"\x1b[<64;5;7M", mouseEvent{btn: 64, x: 5, y: 7, press: true}, true},
		{"\x1b[<4;5;7M", mouseEvent{btn: 0, x: 5, y: 7, press: true}, true},                // shift held
		{"\x1b[<32;9;7M", mouseEvent{btn: 0, x: 9, y: 7, press: true, motion: true}, true}, // drag (1002)
		{"\x1b[<32;9;300M", mouseEvent{btn: 0, x: 9, y: 300, press: true, motion: true}, true},
		{"\x1b[M" + string(rune(32)) + "%'", mouseEvent{btn: 0, x: 5, y: 7, press: true}, true}, // X10 press at (5,7)
		{"\x1b[M" + string(rune(35)) + "%'", mouseEvent{btn: 3, x: 5, y: 7}, true},              // X10 release
		{"\x1b[M" + string(rune(64)) + "&'", mouseEvent{btn: 0, x: 6, y: 7, press: true, motion: true}, true},
		{"\x1b[M" + string(rune(96)) + "%'", mouseEvent{btn: 64, x: 5, y: 7, press: true}, true}, // X10 wheel
		{"\x1b[A", mouseEvent{}, false},
	}
	for _, c := range cases {
		got, ok := parseMouse(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parseMouse(%q) = %+v,%v want %+v,%v", c.in, got, ok, c.want, c.ok)
		}
	}
	var p inputParser
	got := p.feed("\x1b[M %'x")
	if !slices.Equal(got, []string{"\x1b[M %'", "x"}) {
		t.Fatalf("parser split X10 report wrong: %q", got)
	}
}

// logTerm records what the renderer writes, on top of the test terminal.
type logTerm struct {
	*vterm
	out strings.Builder
}

func (l *logTerm) Write(s string) { l.out.WriteString(s); l.vterm.Write(s) }

func TestSetMode(t *testing.T) {
	l := &logTerm{vterm: newVterm(20, 5)}
	ui := New(l)
	ui.Body.Add(&lines{l: []string{"a", "b"}})
	ui.Footer.Add(&lines{l: []string{"foot"}})

	ui.SetMode(Inline) // before Start: only the mode changes
	ui.SetMode(Fullscreen)
	if l.out.Len() != 0 || ui.Mode != Fullscreen {
		t.Fatalf("SetMode before Start wrote %q", l.out.String())
	}

	ui.started = true
	ui.RenderNow()
	l.out.Reset()
	ui.SetMode(Inline)
	if got := l.out.String(); got != kittyOff+"\x1b[?1006l\x1b[?1002l\x1b[?1000l\x1b[?1049l"+kittyOn {
		t.Fatalf("leaving fullscreen wrote %q", got)
	}
	ui.RenderNow()
	if strings.Contains(l.out.String(), "\x1b[2J") {
		t.Fatalf("the first inline frame cleared the screen: %q", l.out.String())
	}
	if !strings.Contains(l.out.String(), "foot") {
		t.Fatalf("inline frame missing: %q", l.out.String())
	}

	l.out.Reset()
	ui.SetMode(Fullscreen)
	if !strings.HasPrefix(l.out.String(), kittyOff+"\x1b[?1049h\x1b[?1000h\x1b[?1002h\x1b[?1006h") {
		t.Fatalf("entering fullscreen wrote %q", l.out.String())
	}
	ui.RenderNow()
	if got := l.screenRows(); got[len(got)-1] != "foot" {
		t.Fatalf("fullscreen footer not pinned: %q", got)
	}
}

// pill is a footer component that shows only while scrolled up and jumps
// to the bottom when clicked.
type pill struct{ ui *TUI }

func (p pill) Render(int) []string {
	if p.ui.ScrollOffset() == 0 {
		return nil
	}
	if p.ui.NewBelow() {
		return []string{"NEW"}
	}
	return []string{"JUMP"}
}

func (p pill) Click(int) bool { p.ui.ScrollToBottom(); return true }

func TestFooterClickAndNewBelow(t *testing.T) {
	v := newVterm(20, 6)
	ui := New(v)
	body := &lines{l: []string{"a", "b", "c", "d", "e", "f", "g", "h"}}
	ui.Body.Add(body)
	ui.Footer.Add(pill{ui}, &lines{l: []string{"> in", "status"}})

	ui.RenderNow()
	if got := noBar(v.screenRows()); got[3] != "h" || strings.Contains(strings.Join(got, "|"), "JUMP") {
		t.Fatalf("at the bottom there is no pill: %q", got)
	}
	ui.ScrollBy(2)
	ui.RenderNow()
	got := noBar(v.screenRows())
	if got[len(got)-3] != "JUMP" {
		t.Fatalf("scrolled up shows the pill above the input: %q", got)
	}
	if ui.NewBelow() {
		t.Fatal("nothing new yet")
	}

	body.l = append(body.l, "i")
	ui.RenderNow()
	got = noBar(v.screenRows())
	if got[len(got)-3] != "NEW" || !ui.NewBelow() {
		t.Fatalf("new output while scrolled up is flagged: %q", got)
	}

	// A click on the pill row scrolls to the bottom and removes it.
	ui.handleScroll("\x1b[<0;3;4M") // row 4 of 6 is the pill
	ui.RenderNow()
	got = noBar(v.screenRows())
	if ui.ScrollOffset() != 0 || ui.NewBelow() || got[len(got)-3] == "NEW" || got[len(got)-3] == "JUMP" {
		t.Fatalf("click did not jump to the bottom: %q", got)
	}
	if got[len(got)-3] != "i" {
		t.Fatalf("newest line is visible: %q", got)
	}
}

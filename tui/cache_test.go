package tui

import (
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRenderCache(t *testing.T) {
	var c RenderCache[string]
	calls := 0
	render := func() []string { calls++; return []string{fmt.Sprint(calls)} }
	steps := []struct {
		width int
		key   string
		want  string
	}{
		{10, "a", "1"}, // first render
		{10, "a", "1"}, // cached
		{10, "b", "2"}, // key changed
		{12, "b", "3"}, // width changed
		{12, "b", "3"},
		{10, "a", "4"}, // only the last render is kept
	}
	for i, s := range steps {
		if got := c.Render(s.width, s.key, render); got[0] != s.want {
			t.Fatalf("step %d: got %q, want %q", i, got, s.want)
		}
	}
	if _, ok := c.Get(10, "b"); ok {
		t.Fatal("Get hit another key")
	}
}

// memoRig drives two TUIs over the same components through the same
// input: one keeps its per-line memos, the other forgets them before
// every frame. Their frames must be the same.
type memoRig struct {
	warm, cold *TUI
	vw, vc     *vterm
	body       *lines
}

func newMemoRig(mode Mode, full bool) *memoRig {
	r := &memoRig{vw: newVterm(30, 8), vc: newVterm(30, 8), body: &lines{}}
	clock := time.Unix(1000, 0)
	for _, p := range []struct {
		ui **TUI
		v  *vterm
	}{{&r.warm, r.vw}, {&r.cold, r.vc}} {
		ui := New(p.v)
		ui.Mode, ui.FullRepaint, ui.PaddingX, ui.GapY = mode, full, 1, 1
		ui.now = func() time.Time { return clock }
		ui.autoScrollEvery = time.Hour
		ui.Body.Add(r.body)
		ui.Footer.Add(&lines{l: []string{"foot " + CursorMarker}})
		*p.ui = ui
	}
	return r
}

func (r *memoRig) input(s string) { r.warm.handleInput(s); r.cold.handleInput(s) }

func (r *memoRig) render(t *testing.T, step string) {
	t.Helper()
	r.warm.RenderNow()
	r.cold.padMemo, r.cold.prepMemo = lineMemo{}, lineMemo{}
	r.cold.RenderNow()
	if !slices.Equal(r.warm.prevFrame, r.cold.prevFrame) || !slices.Equal(r.warm.prevLines, r.cold.prevLines) {
		t.Fatalf("%s: frames differ\nwarm %q %q\ncold %q %q", step, r.warm.prevFrame, r.warm.prevLines, r.cold.prevFrame, r.cold.prevLines)
	}
	if !slices.Equal(r.vw.rows(), r.vc.rows()) {
		t.Fatalf("%s: screens differ\nwarm %q\ncold %q", step, r.vw.rows(), r.vc.rows())
	}
}

// TestLineMemoMatchesFresh edits, wraps, selects, scrolls and resizes at
// random and checks that frames built with the memos warm are the frames
// built without them.
func TestLineMemoMatchesFresh(t *testing.T) {
	pieces := []string{"word", " ", "\t", "漢字", Bold("bold"), FG(3, "red"), "😀", "x"}
	modes := []struct {
		mode Mode
		full bool
	}{{Fullscreen, false}, {Fullscreen, true}, {Inline, false}, {Inline, true}}
	for _, m := range modes {
		for seed := range int64(40) {
			rng := rand.New(rand.NewSource(seed))
			r := newMemoRig(m.mode, m.full)
			text := func() string {
				var b strings.Builder
				for range rng.Intn(30) {
					b.WriteString(pieces[rng.Intn(len(pieces))])
				}
				return b.String()
			}
			for step := range 60 {
				name := fmt.Sprintf("mode %v full %v seed %d step %d", m.mode, m.full, seed, step)
				switch op := rng.Intn(10); {
				case op < 3: // append wrapped text, wrap marks and all
					r.body.l = append(r.body.l, Wrap(text(), 5+rng.Intn(20))...)
				case op < 5 && len(r.body.l) > 0: // edit a line
					r.body.l[rng.Intn(len(r.body.l))] = text()
				case op < 6 && len(r.body.l) > 0: // insert in the middle
					i := rng.Intn(len(r.body.l))
					r.body.l = slices.Insert(r.body.l, i, text())
				case op < 7 && len(r.body.l) > 0: // delete
					i := rng.Intn(len(r.body.l))
					r.body.l = slices.Delete(r.body.l, i, i+1)
				case op < 8 && m.mode == Fullscreen: // select by dragging
					x0, y0, x1, y1 := 1+rng.Intn(30), 1+rng.Intn(8), 1+rng.Intn(30), 1+rng.Intn(8)
					r.input(fmt.Sprintf("\x1b[<0;%d;%dM", x0, y0))
					r.input(fmt.Sprintf("\x1b[<32;%d;%dM", x1, y1))
					if rng.Intn(2) == 0 {
						r.input(fmt.Sprintf("\x1b[<0;%d;%dm", x1, y1))
					}
				case op < 9 && m.mode == Fullscreen: // scroll
					r.warm.ScrollBy(rng.Intn(5) - 2)
					r.cold.ScrollBy(0)
					r.cold.scroll = r.warm.scroll
				default: // resize
					w, h := 15+rng.Intn(30), 4+rng.Intn(8)
					r.vw.resize(w, h)
					r.vc.resize(w, h)
				}
				r.render(t, name)
			}
		}
	}
}

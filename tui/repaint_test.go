package tui

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"
)

func TestAnimationInterval(t *testing.T) {
	ui := New(newVterm(10, 3))
	ui.FullRepaint = false
	if d, g := ui.AnimationInterval(), ui.GlyphInterval(); d != 33*time.Millisecond || g != 80*time.Millisecond {
		t.Fatalf("diff mode intervals = %v, %v", d, g)
	}
	ui.FullRepaint = true
	if d, g := ui.AnimationInterval(), ui.GlyphInterval(); d != 250*time.Millisecond || g != 250*time.Millisecond {
		t.Fatalf("full repaint intervals = %v, %v", d, g)
	}
}

func TestFullRepaintDefault(t *testing.T) {
	cases := []struct {
		goos, env string
		want      bool
	}{
		{"windows", "", true}, {"linux", "", false}, {"darwin", "", false},
		{"linux", "1", true}, {"windows", "0", false}, {"windows", "1", true},
		{"darwin", "other", false}, {"windows", "other", true},
	}
	for _, c := range cases {
		if got := fullRepaintFor(c.goos, c.env); got != c.want {
			t.Errorf("fullRepaintFor(%q, %q) = %v, want %v", c.goos, c.env, got, c.want)
		}
	}
}

// cupCount counts the cleared, column-1 row writes in a fullscreen frame.
func cupCount(s string) int { return strings.Count(s, ";1H\x1b[2K") }

func TestFullRepaintFullscreenRewritesAllRows(t *testing.T) {
	l := &logTerm{vterm: newVterm(20, 6)}
	ui := New(l)
	ui.FullRepaint = true
	body := &lines{l: []string{"하나", "둘", "셋"}}
	ui.Body.Add(body)
	ui.Footer.Add(&lines{l: []string{"foot"}})
	ui.RenderNow()
	l.out.Reset()
	body.l[2] = "셋셋"
	ui.RenderNow()
	out := l.out.String()
	if !strings.HasPrefix(out, syncBegin) || !strings.HasSuffix(out, syncEnd) {
		t.Fatalf("frame not wrapped in synchronized output: %q", out)
	}
	for row := 1; row <= 6; row++ {
		if !strings.Contains(out, fmt.Sprintf("\x1b[%d;1H\x1b[2K", row)) {
			t.Fatalf("row %d not rewritten from column 1: %q", row, out)
		}
	}
	if ui.FullRedraws != 1 {
		t.Fatalf("full repaint caused extra full redraws: %d", ui.FullRedraws)
	}
	// Without full repaint, only the changed row is written.
	ui.FullRepaint = false
	l.out.Reset()
	body.l[2] = "셋"
	ui.RenderNow()
	if n := cupCount(l.out.String()); n != 1 {
		t.Fatalf("diff mode wrote %d rows, want 1: %q", n, l.out.String())
	}
}

func TestFullRepaintInlineRewritesViewport(t *testing.T) {
	l := &logTerm{vterm: newVterm(20, 4)}
	ui := New(l)
	ui.Mode = Inline
	ui.FullRepaint = true
	c := &lines{}
	ui.Body.Add(c)
	for i := 0; i < 10; i++ {
		c.l = append(c.l, fmt.Sprintf("한글 %d", i))
		ui.RenderNow()
		assertTranscript(t, l.vterm, c.l, fmt.Sprintf("append %d", i))
	}
	l.out.Reset()
	c.l[9] = "한글 바뀜"
	ui.RenderNow()
	out := l.out.String()
	// The 4 visible rows are all rewritten, the scrolled-off ones are not.
	for _, want := range []string{"한글 6", "한글 7", "한글 8", "한글 바뀜"} {
		if !strings.Contains(out, want) {
			t.Fatalf("viewport row %q not rewritten: %q", want, out)
		}
	}
	if strings.Contains(out, "한글 5") {
		t.Fatalf("row in scrollback was rewritten: %q", out)
	}
	if ui.FullRedraws != 1 {
		t.Fatalf("full redraws = %d, want 1", ui.FullRedraws)
	}
	assertTranscript(t, l.vterm, c.l, "after edit")
}

// TestHangulStreaming streams Korean text one character at a time, wrapped,
// in both modes with and without full repaint, and checks the emulated
// screen after every character.
func TestHangulStreaming(t *testing.T) {
	text := "네, 알겠어요. 이제 돼요! 한글이 두 번 그려지면 안 돼요 — 정말로요."
	for _, mode := range []Mode{Inline, Fullscreen} {
		for _, full := range []bool{false, true} {
			t.Run(fmt.Sprintf("mode%d_full%v", mode, full), func(t *testing.T) {
				v := newVterm(16, 8)
				ui := New(v)
				ui.Mode = mode
				ui.FullRepaint = full
				c := &lines{}
				ui.Body.Add(c)
				var got string
				for _, r := range text {
					got += string(r)
					c.l = []string{"user: 질문"}
					for _, l := range Wrap(got, 16) {
						c.l = append(c.l, StripWrapMarks(l))
					}
					ui.RenderNow()
					if mode == Inline {
						assertTranscript(t, v, c.l, "stream "+got)
						continue
					}
					rows := v.screenRows()
					for i, l := range c.l {
						if rows[i] != l {
							t.Fatalf("after %q row %d = %q, want %q", got, i, rows[i], l)
						}
					}
				}
			})
		}
	}
}

func TestRandomOperationsFullRepaint(t *testing.T) {
	for seed := int64(0); seed < 100; seed++ {
		rng := rand.New(rand.NewSource(seed))
		ui, v, c := setup(30, 3+rng.Intn(8))
		ui.FullRepaint = true
		for step := 0; step < 60; step++ {
			switch op := rng.Intn(6); {
			case op < 2:
				c.l = append(c.l, fmt.Sprintf("한%d글", step))
			case op < 4 && len(c.l) > 0:
				c.l[len(c.l)-1-rng.Intn(min(len(c.l), 3))] = fmt.Sprintf("바뀜%d", step)
			case op < 5 && len(c.l) > 0:
				c.l = c.l[:len(c.l)-rng.Intn(min(len(c.l), 3)+1)]
			default:
				c.l = append(c.l, "x")
			}
			ui.RenderNow()
			assertTranscript(t, v, c.l, fmt.Sprintf("seed %d step %d", seed, step))
		}
	}
}

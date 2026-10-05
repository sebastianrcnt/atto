package tui

import (
	"strings"
	"testing"
	"time"
)

func TestDetectColorDepth(t *testing.T) {
	for _, c := range []struct {
		env  map[string]string
		want ColorDepth
	}{
		{map[string]string{"COLORTERM": "truecolor", "TERM": "xterm-256color"}, TrueColor},
		{map[string]string{"COLORTERM": "24bit"}, TrueColor},
		{map[string]string{"WT_SESSION": "x"}, TrueColor},
		{map[string]string{"TERM_PROGRAM": "iTerm.app"}, TrueColor},
		{map[string]string{"TERM_PROGRAM": "Apple_Terminal", "TERM": "xterm-256color"}, Colors256},
		{map[string]string{}, Colors256}, // Windows conhost, say
		{map[string]string{"TERM": "linux"}, Colors16},
		{map[string]string{"TERM": "vt100"}, Colors16},
	} {
		if got := DetectColorDepth(func(k string) string { return c.env[k] }); got != c.want {
			t.Errorf("%v: %d, want %d", c.env, got, c.want)
		}
	}
}

func TestCubeIndex(t *testing.T) {
	for _, c := range []struct {
		c    Color
		want int
	}{
		{Color{R: 0, G: 0, B: 0}, 16}, {Color{R: 255, G: 255, B: 255}, 231},
		{Color{R: 56, G: 178, B: 172}, 73}, {Color{R: 224, G: 168, B: 74}, 179},
		{Color{R: 95, G: 135, B: 175}, 16 + 36 + 12 + 3},
	} {
		if got := cubeIndex(c.c); got != c.want {
			t.Errorf("%v: %d, want %d", c.c, got, c.want)
		}
	}
}

var (
	testBase = Color{R: 56, G: 178, B: 172, Basic: 36}
	testHi   = Color{R: 178, G: 245, B: 234, Basic: 36}
)

// Shimmer keeps the text and its width, colors every depth its own way,
// and resets what it set.
func TestShimmer(t *testing.T) {
	for _, text := range []string{"Blorping…", "글벅거리는 중…"} {
		for _, d := range []ColorDepth{TrueColor, Colors256, Colors16} {
			for center := -4.0; center < 20; center += 0.7 {
				s := Shimmer(text, d, testBase, testHi, center, 3)
				if StripEscapes(s) != text || VisibleWidth(s) != VisibleWidth(text) {
					t.Fatalf("%d %q: %q", d, text, s)
				}
				reset := "\x1b[39m"
				if d == Colors16 {
					reset = "\x1b[22;39m"
				}
				if !strings.HasSuffix(s, reset) {
					t.Fatalf("%d: no reset: %q", d, s)
				}
				has256, hasRGB := strings.Contains(s, "38;5;"), strings.Contains(s, "38;2;")
				if (d == TrueColor) != hasRGB || (d == Colors256) != has256 {
					t.Fatalf("%d: %q", d, s)
				}
			}
		}
	}
	// The band is brightest at its center.
	s := Shimmer("abcdefg", TrueColor, testBase, testHi, 3.5, 3)
	if !strings.Contains(s, "\x1b[38;2;178;245;234md") || !strings.HasPrefix(s, "\x1b[38;2;56;178;172ma") {
		t.Errorf("band: %q", s)
	}
	// Far away it is all base, one color sequence.
	if s := Shimmer("abc", TrueColor, testBase, testHi, 40, 3); s != "\x1b[38;2;56;178;172mabc\x1b[39m" {
		t.Errorf("no band: %q", s)
	}
	if s := Shimmer("abc", Colors16, testBase, testHi, 1.5, 1); s != "\x1b[22;36ma\x1b[1;36mb\x1b[22;36mc\x1b[22;39m" {
		t.Errorf("16 colors: bold band: %q", s)
	}
}

func TestScannerRender(t *testing.T) {
	s := Scanner{Cells: 7, Step: 80 * time.Millisecond, Hold: 2, Trail: 3.5,
		Head: Color{R: 72, G: 236, B: 216, Basic: 36}, Base: testBase, Idle: Color{R: 74, G: 106, B: 104, Basic: 36}}
	for el := time.Duration(0); el < 3*time.Second; el += 33 * time.Millisecond {
		for _, d := range []ColorDepth{TrueColor, Colors256, Colors16} {
			out := s.Render(el, d)
			if VisibleWidth(out) != 7 || !strings.HasSuffix(out, "39m") {
				t.Fatalf("%v %d: %q", el, d, out)
			}
		}
	}
	if got := StripEscapes(s.Render(240*time.Millisecond, TrueColor)); got != "▱▰▰▰▱▱▱" {
		t.Errorf("step 3: %q", got)
	}
	// Between steps the head glides: the next cell lights up.
	if got := StripEscapes(s.Render(280*time.Millisecond, TrueColor)); got != "▱▰▰▰▰▱▱" {
		t.Errorf("step 3.5: %q", got)
	}
}

package tui

import (
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ColorDepth is how many colors a terminal shows.
type ColorDepth int

const (
	// Colors256 is the xterm palette, what most of atto's styles use.
	Colors256 ColorDepth = iota
	// Colors16 is the basic eight colors and their bright forms.
	Colors16
	// TrueColor is 24-bit RGB.
	TrueColor
)

// DetectColorDepth guesses the terminal's colors from its environment:
// COLORTERM=truecolor or 24bit, Windows Terminal (WT_SESSION) and a few
// terminals known to take RGB are TrueColor; the Linux console, "dumb"
// and VT terminals are Colors16; anything else is Colors256.
func DetectColorDepth(getenv func(string) string) ColorDepth {
	switch strings.ToLower(getenv("COLORTERM")) {
	case "truecolor", "24bit":
		return TrueColor
	}
	if getenv("WT_SESSION") != "" {
		return TrueColor
	}
	switch getenv("TERM_PROGRAM") {
	case "iTerm.app", "WezTerm", "vscode", "ghostty":
		return TrueColor
	}
	switch term := getenv("TERM"); {
	case term == "linux", term == "dumb", strings.HasPrefix(term, "vt"):
		return Colors16
	}
	return Colors256
}

// Color is a foreground color: RGB for TrueColor (and, rounded to the
// xterm cube, for Colors256), and Basic, an SGR code 30–37 or 90–97, for
// Colors16.
type Color struct {
	R, G, B uint8
	Basic   uint8
}

// Mix blends a toward b by t (0 is a, 1 is b). Basic, which cannot
// blend, switches halfway.
func Mix(a, b Color, t float64) Color {
	t = max(0, min(1, t))
	lerp := func(x, y uint8) uint8 { return uint8(math.Round(float64(x) + (float64(y)-float64(x))*t)) }
	c := Color{R: lerp(a.R, b.R), G: lerp(a.G, b.G), B: lerp(a.B, b.B), Basic: a.Basic}
	if t >= 0.5 {
		c.Basic = b.Basic
	}
	return c
}

// cubeIndex is the nearest color of the xterm 6x6x6 cube (16–231).
func cubeIndex(c Color) int {
	level := func(v uint8) int { // cube levels: 0, 95, 135, 175, 215, 255
		if v < 48 {
			return 0
		}
		if v < 115 {
			return 1
		}
		return min(5, (int(v)-35)/40)
	}
	return 16 + 36*level(c.R) + 6*level(c.G) + level(c.B)
}

// fg is the SGR sequence setting c. With Colors16, bold stands in for a
// brighter color.
func (d ColorDepth) fg(c Color, bold bool) string {
	switch d {
	case TrueColor:
		return "\x1b[38;2;" + strconv.Itoa(int(c.R)) + ";" + strconv.Itoa(int(c.G)) + ";" + strconv.Itoa(int(c.B)) + "m"
	case Colors16:
		if bold {
			return "\x1b[1;" + strconv.Itoa(int(c.Basic)) + "m"
		}
		return "\x1b[22;" + strconv.Itoa(int(c.Basic)) + "m"
	}
	return "\x1b[38;5;" + strconv.Itoa(cubeIndex(c)) + "m"
}

// reset undoes what fg set.
func (d ColorDepth) reset() string {
	if d == Colors16 {
		return "\x1b[22;39m"
	}
	return "\x1b[39m"
}

// Paint colors s with c, resetting only the foreground.
func (d ColorDepth) Paint(c Color, s string) string { return d.fg(c, false) + s + d.reset() }

// Shimmer colors text base, brightening toward hi in a band around column
// center that fades out over radius columns either side. It writes a color
// only where it changes (the band is quantized to quarter steps) and
// resets only the foreground (and, with Colors16, bold) at the end.
func Shimmer(text string, d ColorDepth, base, hi Color, center, radius float64) string {
	var b strings.Builder
	b.Grow(len(text) + 64)
	prev, col := "", 0.0
	for i := 0; i < len(text); {
		r, n := utf8.DecodeRuneInString(text[i:])
		w := 1.0
		if r >= 0x1100 { // wide (CJK, Hangul) or zero-width: measure it
			w = float64(VisibleWidth(text[i : i+n]))
		}
		t := max(0, 1-math.Abs(col+w/2-center)/radius)
		t = math.Round(t*4) / 4
		if sgr := d.fg(Mix(base, hi, t), t >= 0.5); sgr != prev {
			b.WriteString(sgr)
			prev = sgr
		}
		b.WriteString(text[i : i+n])
		i += n
		col += w
	}
	b.WriteString(d.reset())
	return b.String()
}

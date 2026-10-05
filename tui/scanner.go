package tui

import (
	"math"
	"strings"
	"time"
)

// Scanner is a row of cells a lit head sweeps across and back, a trail
// fading behind it and a short hold at each end, like the Knight Rider car.
// The bidirectional sweep, end holds and trail follow opencode's spinner
// (packages/tui/src/ui/spinner.ts, MIT; see THIRD_PARTY_NOTICES); this is a
// time-based rewrite with its own glyphs and colors.
type Scanner struct {
	Cells int           // how many cells
	Step  time.Duration // time the head takes to move one cell
	Hold  int           // steps it rests at each end
	Trail float64       // cells the trail fades over

	// Head is the color of the lit head, Base the trail's middle and
	// Idle the color of unlit cells.
	Head, Base, Idle Color
}

// Scanner glyphs: lit and unlit cells. Both are East Asian width Neutral,
// one column even in CJK locales, and in the Geometric Shapes block that
// terminal fonts (Menlo, DejaVu Sans Mono, Cascadia, or Segoe UI Symbol as
// fallback) cover. Colors16 uses ASCII.
const (
	scannerLit   = "▰"
	scannerUnlit = "▱"
)

// levels returns each cell's brightness at el, 0 (unlit) to 1 (the head).
func (s Scanner) levels(el time.Duration) []float64 {
	n := s.Cells
	out := make([]float64, n)
	if n <= 0 || s.Step <= 0 {
		return out
	}
	steps := float64(el) / float64(s.Step)
	move, hold := float64(n-1), float64(s.Hold)
	p := math.Mod(steps, 2*move+2*hold)
	// head is where the head is, forward its direction, and rest how long
	// it has held at an end (the trail keeps fading meanwhile).
	var head, rest float64
	forward := true
	switch {
	case p < move:
		head = p
	case p < move+hold:
		head, rest = move, p-move
	case p < 2*move+hold:
		head, forward = move-(p-move-hold), false
	default:
		head, rest, forward = 0, p-2*move-hold, false
	}
	for i := range out {
		d := head - float64(i) // how far behind the head cell i is
		if !forward {
			d = -d
		}
		switch {
		case rest > 0 && d == 0:
			out[i] = 1 // the head stays lit while it holds
		case d < 0: // ahead: lights up as the head arrives
			out[i] = max(0, 1+d)
		default:
			out[i] = max(0, 1-(d+rest)/s.Trail)
		}
	}
	return out
}

// Render draws the scanner as it is el into its run.
func (s Scanner) Render(el time.Duration, d ColorDepth) string {
	var b strings.Builder
	b.Grow(s.Cells * 24)
	prev := ""
	for _, t := range s.levels(el) {
		glyph, c, bold := scannerUnlit, s.Idle, false
		switch {
		case d == Colors16:
			glyph, c = ".", s.Base
			if t >= 0.75 {
				glyph, bold = "=", true
			} else if t >= 0.25 {
				glyph = "-"
			}
		case t >= 0.5:
			glyph, c = scannerLit, Mix(s.Base, s.Head, (t-0.5)*2)
		case t >= 0.2:
			glyph, c = scannerLit, Mix(s.Idle, s.Base, t*2)
		}
		if sgr := d.fg(c, bold); sgr != prev {
			b.WriteString(sgr)
			prev = sgr
		}
		b.WriteString(glyph)
	}
	b.WriteString(d.reset())
	return b.String()
}

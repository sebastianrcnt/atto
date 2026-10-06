package app

import (
	"math"
	"math/rand/v2"
	"time"

	"github.com/sebastianrcnt/atto/tui"
)

// The activity line above the editor while a run is busy:
//
//	 Blorping…  ·  1m 23s  ·  ↑ 8.1k  ↓ 1.2k tokens  ·  esc to interrupt  ·  ctrl+enter to send now
//
// With spinnerScanner, a scanner before the word sweeps a lit teal head
// across seven cells and back, a fading trail behind it (tui.Scanner).
// As soon as there are any, the line adds the run's tokens so far: input
// the server had not cached (↑) and output (↓): thinking, text and the
// tool calls being written. The
// label is teal, with a lighter band
// sweeping across it left to right on its own, slower rhythm: the scanner
// says "busy", the shimmer only adds a little life, and tying them
// together made the band rush across the text (and jump at 250ms). When
// the model has sent nothing for a while (no command running), both turn
// toward amber, and back as soon as something arrives.

var (
	activityTeal    = tui.Color{R: 56, G: 178, B: 172, Basic: 36}
	activityTealHi  = tui.Color{R: 178, G: 245, B: 234, Basic: 36}
	activityAmber   = tui.Color{R: 224, G: 168, B: 74, Basic: 33}
	activityAmberHi = tui.Color{R: 255, G: 222, B: 150, Basic: 33}
	scannerHead     = tui.Color{R: 72, G: 236, B: 216, Basic: 36}
	scannerAmber    = tui.Color{R: 255, G: 196, B: 92, Basic: 33}
	scannerIdle     = tui.Color{R: 74, G: 106, B: 104, Basic: 36}
)

// scanner is the activity line's scanner: seven cells, a step a glyph
// interval (80ms, gliding between cells at 30 frames a second) and a
// two-step hold at each end. Full repaint renders every 250ms: five cells
// and a step a frame, so it moves a cell per frame and a sweep still takes
// only a second. stall (0 to 1) turns the head toward amber.
func (a *App) scanner(stall float64) tui.Scanner {
	s := tui.Scanner{Cells: 7, Step: a.ui.GlyphInterval(), Hold: 2, Trail: 3.5,
		Head: tui.Mix(scannerHead, scannerAmber, stall), Base: tui.Mix(activityTeal, activityAmber, stall), Idle: scannerIdle}
	if a.ui.FullRepaint {
		s.Cells, s.Trail = 5, 3
	}
	return s
}

const (
	// stallAfter is how long the model may send nothing before the line
	// turns toward amber, over stallRamp.
	stallAfter = 15 * time.Second
	stallRamp  = 5 * time.Second
	// The shimmer band moves shimmerSpeed columns a second, fades out
	// shimmerRadius columns either side, and rests shimmerRest columns
	// (of travel) off the end before it starts again.
	shimmerSpeed  = 10.0
	shimmerRadius = 3.0
	shimmerRest   = 8.0
)

func (a *App) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

// verbList is spinnerVerbs' list of words and whether they are Korean.
func verbList(setting string) (verbs []string, korean bool) {
	switch setting {
	case "off":
		return nil, false
	case "ko":
		return koreanVerbs, true
	case "ko-literary":
		return literaryVerbs, true
	}
	return englishVerbs, false // "en", unset or unknown
}

// pickVerb draws the word the activity line shows for "Working" this run,
// formatted ("Blorping", "글벅거리는 중"), never the last one twice in a
// row; "" when spinnerVerbs is "off".
func (a *App) pickVerb() string {
	verbs, korean := verbList(a.spinnerVerbs)
	if len(verbs) == 0 {
		return ""
	}
	intn := rand.IntN
	if a.verbRand != nil {
		intn = a.verbRand.IntN
	}
	for {
		v := verbs[intn(len(verbs))]
		if korean {
			v += " 중"
		}
		if v != a.turnVerb || len(verbs) == 1 {
			return v
		}
	}
}

// activityLabel is what the run is doing, without the ellipsis.
func (a *App) activityLabel() string {
	if a.activity == "Working" && a.turnVerb != "" {
		return a.turnVerb
	}
	return a.activity
}

// liveChars is how much the model call in progress has written so far:
// thinking, text and tool calls.
func (a *App) liveChars() int {
	n := a.streamChars
	for _, c := range a.draftChars {
		n += c
	}
	return n
}

// stallLevel is how far the line has turned toward amber, 0 to 1: the
// time the model has sent nothing beyond stallAfter, over stallRamp. A
// running command is not a stall.
func (a *App) stallLevel(now time.Time) float64 {
	if a.toolsRunning > 0 {
		return 0
	}
	idle := now.Sub(a.lastEvent) - stallAfter
	return max(0, min(1, float64(idle)/float64(stallRamp)))
}

// renderActivity draws the activity line. It runs every animation frame,
// so it stays cheap: the glyph and the shimmer come from the elapsed time.
func (a *App) renderActivity(width int) []string {
	if !a.busy {
		return nil
	}
	now := a.clock()
	el := now.Sub(a.runStart)
	depth := a.ui.Colors
	stall := a.stallLevel(now)
	sc := a.scanner(stall)
	scanAt := el
	if a.ui.FullRepaint { // a cell per frame, wherever in its step the frame falls
		scanAt = el.Truncate(sc.Step)
	}
	base := tui.Mix(activityTeal, activityAmber, stall)
	hi := tui.Mix(activityTealHi, activityAmberHi, stall)
	label := a.activityLabel() + "…"
	travel := float64(tui.VisibleWidth(label)) + 2*shimmerRadius + shimmerRest
	center := math.Mod(el.Seconds()*shimmerSpeed, travel) - shimmerRadius
	line := tui.Shimmer(label, depth, base, hi, center, shimmerRadius)
	if a.spinnerScan {
		line = sc.Render(scanAt, depth) + " " + line
	}
	// A column of margin and wide separators, so the parts read apart.
	const sep = "  ·  "
	meta := sep + tui.FormatDuration(el.Truncate(100*time.Millisecond))
	if out := a.turnOut + a.liveChars()/4; out > 0 || a.turnIn > 0 {
		meta += sep
		if a.turnIn > 0 {
			meta += "↑ " + compactTokens(a.turnIn) + "  "
		}
		meta += "↓ " + compactTokens(out) + " tokens"
	}
	hint := "esc to interrupt"
	if a.runKind == "turn" {
		hint += sep + "ctrl+enter to send now"
	}
	line = " " + line + tui.Dim(meta+sep+hint)
	return []string{"", tui.Truncate(line, width, "…")}
}

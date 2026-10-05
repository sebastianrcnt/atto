package app

import (
	"github.com/sebastianrcnt/atto/provider"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/tui"
)

// busyApp is a test app running for el, its clock stopped there.
func busyApp(t *testing.T, activity string, el time.Duration) (*App, *time.Time) {
	a := testApp(t)
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	now := start.Add(el)
	a.now = func() time.Time { return now }
	a.busy, a.activity, a.runStart, a.lastEvent = true, activity, start, start
	a.ui.Colors = tui.Colors256 // whatever the environment says
	a.spinnerScan = true        // the scanner is what most tests here look at
	return a, &now
}

func activityText(a *App) string { return tui.StripEscapes(a.renderActivity(200)[1]) }

// The scanner moves a cell per step, from the elapsed time, whatever the
// frame rate; in full repaint (a frame every 250ms) it has five cells and
// moves one per frame.
func TestScannerSteps(t *testing.T) {
	for _, c := range []struct {
		full bool
		at   []string // the scanner at steps 0, 1, 2, ...
	}{
		{false, []string{
			"=......", "-=.....", "--=....", ".--=...", "..--=..", "...--=.", "....--=",
			".....-=", "......=", // holding: the trail fades
			".....=-", "....=--", "...=--.", "..=--..", ".=--...", "=--....",
			"=-.....", // holding at the start
			"=......"}},
		{true, []string{"=....", "-=...", "--=..", ".--=.", "..--=", "...-=", "....=", "...=-", "..=--", ".=--.", "=--..", "=-...", "=...."}},
	} {
		a, now := busyApp(t, "Thinking", 0)
		a.ui.FullRepaint, a.ui.Colors = c.full, tui.Colors16
		step := a.ui.GlyphInterval()
		for i, want := range c.at {
			*now = a.runStart.Add(time.Duration(i)*step + step/3) // mid-frame
			if c.full {
				*now = now.Add(step / 3)
			} else {
				*now = now.Add(-step / 3)
			}
			if got := activityText(a); !strings.HasPrefix(got, want+" Thinking…  ") {
				t.Errorf("full repaint %v, step %d: %q, want %q", c.full, i, got, want)
			}
		}
	}
}

func TestScannerGlyphs(t *testing.T) {
	a, now := busyApp(t, "Thinking", 0)
	a.ui.Colors = tui.TrueColor
	*now = now.Add(3 * a.ui.GlyphInterval())
	scan, _, _ := strings.Cut(activityText(a), " ")
	if scan != "▱▰▰▰▱▱▱" {
		t.Errorf("scanner %q", scan)
	}
	for _, g := range []string{"▰", "▱"} {
		if w := tui.VisibleWidth(g); w != 1 {
			t.Errorf("%q is %d columns", g, w)
		}
	}
	// The head is the brightest teal.
	if raw := a.renderActivity(200)[1]; !strings.Contains(raw, "\x1b[38;2;72;236;216m▰") {
		t.Errorf("no teal head: %q", raw)
	}
}

// The 16-color fallback is ASCII, without 256 or RGB colors.
func TestActivityColors16(t *testing.T) {
	a, _ := busyApp(t, "Thinking", 0)
	a.ui.Colors = tui.Colors16
	raw := a.renderActivity(200)[1]
	if strings.Contains(raw, "38;5;") || strings.Contains(raw, "38;2;") {
		t.Errorf("16 colors: %q", raw)
	}
	if got := tui.StripEscapes(raw); !strings.HasPrefix(got, "=...... Thinking…") {
		t.Errorf("16 colors: %q", got)
	}
	a.ui.Colors = tui.Colors256
	if raw := a.renderActivity(200)[1]; !strings.Contains(raw, "\x1b[38;5;73m") || strings.Contains(raw, "38;2;") {
		t.Errorf("256 colors: %q", raw)
	}
}

func TestVerbSettings(t *testing.T) {
	for _, c := range []struct {
		setting string
		list    []string
		suffix  string
	}{
		{"", englishVerbs, "…"}, {"en", englishVerbs, "…"}, {"bogus", englishVerbs, "…"},
		{"ko", koreanVerbs, " 중…"}, {"ko-literary", literaryVerbs, " 중…"}, {"off", nil, ""},
	} {
		a, _ := busyApp(t, "Working", time.Second)
		a.spinnerVerbs, a.verbRand = c.setting, rand.New(rand.NewPCG(1, 2))
		a.turnVerb = a.pickVerb()
		_, label, _ := strings.Cut(strings.SplitN(activityText(a), "  ", 2)[0], " ")
		if c.list == nil {
			if label != "Working…" {
				t.Errorf("%q: %q", c.setting, label)
			}
			continue
		}
		verb, ok := strings.CutSuffix(label, c.suffix)
		if !ok || !slices.Contains(c.list, verb) {
			t.Errorf("%q: %q", c.setting, label)
		}
		a.activity = "Thinking" // specific labels stay
		if got := activityText(a); !strings.Contains(got, " Thinking…  ") {
			t.Errorf("%q: %q", c.setting, got)
		}
	}
}

// The verb is drawn once per run: it stays while the run goes on, and the
// next run draws another; the same seed draws the same.
func TestVerbStablePerTurn(t *testing.T) {
	draw := func() []string {
		a, now := busyApp(t, "Working", 0)
		a.verbRand = rand.New(rand.NewPCG(7, 7))
		var seen []string
		for range 5 {
			a.turnVerb = a.pickVerb() // as start does
			for range 10 {
				*now = now.Add(123 * time.Millisecond)
				if got := activityText(a); !strings.Contains(got, " "+a.turnVerb+"…  ") {
					t.Fatalf("verb %q not shown: %q", a.turnVerb, got)
				}
			}
			if len(seen) > 0 && seen[len(seen)-1] == a.turnVerb {
				t.Errorf("same verb twice in a row: %q", a.turnVerb)
			}
			seen = append(seen, a.turnVerb)
		}
		return seen
	}
	first := draw()
	if again := draw(); !slices.Equal(first, again) {
		t.Errorf("seeded draws differ: %q, %q", first, again)
	}
}

// After stallAfter without events (no command running) the line turns
// toward amber; an event brings the teal back.
func TestActivityStall(t *testing.T) {
	a, now := busyApp(t, "Thinking", 0)
	a.ui.Colors = tui.TrueColor
	teal, amber := "\x1b[38;2;56;178;172m", "\x1b[38;2;224;168;74m"
	color := func() string { // the label's base color
		raw := a.renderActivity(200)[1]
		switch t, m := strings.Contains(raw, teal), strings.Contains(raw, amber); {
		case t && !m:
			return teal
		case m && !t:
			return amber
		}
		return raw
	}
	at := func(d time.Duration) { *now = a.runStart.Add(d) }

	at(stallAfter - time.Millisecond)
	if g := color(); g != teal {
		t.Fatalf("before stallAfter: %q", g)
	}
	at(stallAfter + stallRamp/2)
	if g := color(); g == teal || g == amber {
		t.Fatalf("halfway: %q", g)
	}
	at(stallAfter + stallRamp)
	if g := color(); g != amber {
		t.Fatalf("after the ramp: %q", g)
	}
	a.onEvent(agent.TextDelta{Text: "hi"})
	if g := color(); g != teal {
		t.Fatalf("after output: %q", g)
	}
	// A command running is not a stall.
	a.onEvent(agent.ToolStart{ID: "1"})
	at(time.Hour)
	if g := color(); g != teal {
		t.Fatalf("while a command runs: %q", g)
	}
	a.onEvent(agent.ToolEnd{ID: "1"})
	at(time.Hour + stallAfter + stallRamp)
	if g := color(); g != amber {
		t.Fatalf("after the command: %q", g)
	}
}

// The line keeps its width as the shimmer moves, and resets its colors.
func TestActivityShimmerFrames(t *testing.T) {
	for _, depth := range []tui.ColorDepth{tui.TrueColor, tui.Colors256, tui.Colors16} {
		a, now := busyApp(t, "Working", 0)
		a.ui.Colors, a.turnVerb = depth, "글벅거리는 중"
		var want int
		for i := range 60 {
			*now = a.runStart.Add(time.Duration(i) * 33 * time.Millisecond)
			raw := a.renderActivity(200)[1]
			label := raw[:strings.Index(raw, "\x1b[2m")] // the elapsed time aside
			w := tui.VisibleWidth(label)
			if i == 0 {
				want = w
			} else if w != want {
				t.Fatalf("depth %d frame %d: width %d, want %d", depth, i, w, want)
			}
			if !strings.HasSuffix(label, "39m") {
				t.Fatalf("depth %d: label does not reset its color: %q", depth, label)
			}
		}
	}
}

// BenchmarkRenderActivity draws the activity line, as every frame does.
func BenchmarkRenderActivity(b *testing.B) {
	a := &App{ui: tui.New(nullTerm{}), busy: true, activity: "Working", turnVerb: "Blorvenating"}
	a.ui.Colors = tui.TrueColor
	now := time.Now()
	a.runStart, a.lastEvent = now, now
	a.now = func() time.Time { return now }
	b.ReportAllocs()
	for range b.N {
		now = now.Add(33 * time.Millisecond)
		a.renderActivity(120)
	}
}

// By default there is no scanner: the line starts with the word. After
// half a minute it counts the run's output tokens: the finished calls'
// usage plus the call in progress at four characters a token.
func TestActivityDefaultAndTokens(t *testing.T) {
	a, now := busyApp(t, "Thinking", 0)
	a.spinnerScan = false
	if got := activityText(a); !strings.HasPrefix(got, "Thinking…  0ms") {
		t.Fatalf("default line %q", got)
	}
	a.onEvent(agent.StepEnd{Usage: provider.Usage{CompletionTokens: 1000}})
	a.onEvent(agent.TextDelta{Text: strings.Repeat("x", 800)})
	if got := activityText(a); strings.Contains(got, "tokens") {
		t.Fatalf("tokens before 30s: %q", got)
	}
	*now = a.runStart.Add(31 * time.Second)
	if got := activityText(a); !strings.Contains(got, "· ↓ 1.2k tokens · esc to interrupt") {
		t.Fatalf("tokens: %q", got)
	}
	a.onEvent(agent.StepEnd{Usage: provider.Usage{CompletionTokens: 300}})
	if got := activityText(a); !strings.Contains(got, "↓ 1.3k tokens") {
		t.Fatalf("after the call ends its usage replaces the estimate: %q", got)
	}
}

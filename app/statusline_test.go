package app

import (
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/ai"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/tui"
)

func statusApp(t *testing.T, cost *ai.ModelCost) *App {
	t.Helper()
	ref := config.ModelRef{Model: config.Model{ID: "m", Name: "Orca", ContextWindow: 262000, Cost: cost}}
	a := newApp(nil, config.ModelsFile{}, "/work/proj")
	a.gitBranch, a.sessName = "main", "fix"
	setTestModel(a, ref)
	a.ctxTokens = 31000
	a.usage.add(provider.Usage{PromptTokens: 94000, CachedTokens: 80000, CacheWriteTokens: 2000, CompletionTokens: 3400, Cost: 0.1234})
	return a
}

func TestCompactTokens(t *testing.T) {
	for n, want := range map[int]string{0: "0", 950: "950", 1234: "1.2k", 2000: "2k", 12400: "12k", 12500: "13k", 1234567: "1.2M", 12345678: "12M"} {
		if got := compactTokens(n); got != want {
			t.Errorf("compactTokens(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestBuiltinStatusWidths(t *testing.T) {
	price := &ai.ModelCost{Input: 1, Output: 2}
	a := statusApp(t, price)

	rows := func(width int) []string {
		var out []string
		for _, l := range a.builtinStatus(width, width) {
			out = append(out, tui.StripEscapes(l))
		}
		return out
	}
	all := []string{"Orca", "11%", "31.0k/262.0k", "cache 85%", "↑12k", "↓3.4k", "W2k", "$0.123", "/work/proj (main)"}
	hasAll := func(width int) {
		t.Helper()
		s := strings.Join(rows(width), "\n")
		for _, want := range all {
			if !strings.Contains(s, want) {
				t.Errorf("width %d lacks %q: %q", width, want, s)
			}
		}
	}

	// Wide: one row. Narrower: two rows that still hold everything, the
	// usage on the first and where we are at the right of the second.
	if r := rows(160); len(r) != 1 {
		t.Fatalf("width 160 takes one row: %q", r)
	}
	hasAll(160)
	r := rows(90)
	if len(r) != 2 || !strings.HasSuffix(r[0], "$0.123") || !strings.HasSuffix(r[1], "/work/proj (main) · "+fmtBytes(rssBytes.Load())) || tui.VisibleWidth(r[1]) != 90 {
		t.Fatalf("width 90: %q", r)
	}
	hasAll(90)
	// The left items that do not fit the first row start the second.
	r = rows(70)
	if len(r) != 2 || !strings.HasPrefix(r[1], " $0.123 ") || strings.Contains(r[0], "$0.123") {
		t.Fatalf("width 70: %q", r)
	}
	hasAll(70)
	// The session's cache reads are not shown (the hit rate is).
	if s := strings.Join(rows(160), "\n"); strings.Contains(s, "R80k") {
		t.Errorf("cache read total shown: %q", s)
	}

	// Only when two rows cannot hold them are items dropped, least
	// important first.
	order := []string{"W2k", "cache 85%", "↑12k", "$0.123", "proj (main)"}
	prev := len(order)
	for width := 160; width >= 20; width -= 2 {
		r := rows(width)
		if len(r) > 2 {
			t.Fatalf("width %d: more than two rows: %q", width, r)
		}
		for _, l := range r {
			if w := tui.VisibleWidth(l); w > width {
				t.Fatalf("width %d: row is %d wide: %q", width, w, l)
			}
		}
		if !strings.Contains(r[0], "Orca") {
			t.Fatalf("width %d: the model is always first: %q", width, r)
		}
		// Count how many of the items in drop order are still there; it
		// must never grow as the terminal narrows.
		s := strings.Join(r, "\n")
		n := 0
		for _, it := range order {
			if strings.Contains(s, it) {
				n++
			}
		}
		if n > prev {
			t.Fatalf("width %d: an item came back: %q", width, s)
		}
		prev = n
	}
	if s := strings.Join(rows(46), "\n"); strings.Contains(s, "fix") || !strings.Contains(s, "proj") {
		t.Errorf("the name is on the input's rule, never here: %q", s)
	}
	if s := strings.Join(rows(24), "\n"); strings.Contains(s, "proj") || !strings.Contains(s, "11%") {
		t.Errorf("width 24 keeps the bar over the directory: %q", s)
	}
}

// A goal indicator narrows only the first row.
func TestBuiltinStatusFirstRowNarrower(t *testing.T) {
	a := statusApp(t, &ai.ModelCost{Output: 1})
	r := a.builtinStatus(60, 120)
	if len(r) != 2 || tui.VisibleWidth(r[0]) > 60 || tui.VisibleWidth(r[1]) != 120 {
		t.Fatalf("%q", r)
	}
	if s := tui.StripEscapes(strings.Join(r, "\n")); !strings.Contains(s, "$0.123") || !strings.Contains(s, "/work/proj") {
		t.Fatalf("both rows hold everything: %q", s)
	}
}

// Wide characters are measured by their columns.
func TestBuiltinStatusCJK(t *testing.T) {
	a := statusApp(t, nil)
	a.sessName = "버그수정"
	a.cwd = "/work/프로젝트"
	for _, width := range []int{120, 80, 60, 40, 30} {
		for _, l := range a.builtinStatus(width, width) {
			if w := tui.VisibleWidth(l); w > width {
				t.Fatalf("width %d: row is %d wide: %q", width, w, tui.StripEscapes(l))
			}
		}
	}
	if s := tui.StripEscapes(strings.Join(a.builtinStatus(80, 80), "\n")); strings.Contains(s, "버그수정") || !strings.Contains(s, "프로젝트") {
		t.Fatalf("width 80 shows the directory, not the name (it is on the input's rule): %q", s)
	}
}

func TestStatusSubscriptionCostIsEstimate(t *testing.T) {
	a := statusApp(t, &ai.ModelCost{Output: 5})
	if s := tui.StripEscapes(strings.Join(a.builtinStatus(160, 160), "\n")); !strings.Contains(s, " $0.123") {
		t.Errorf("pay-per-use cost: %q", s)
	}
	ref := config.ModelRef{ProviderName: "opencode-go", Provider: config.Provider{Subscription: true},
		Model: config.Model{ID: "m", Name: "Orca", ContextWindow: 262000, Cost: &ai.ModelCost{Output: 5}}}
	setTestModel(a, ref)
	if s := tui.StripEscapes(strings.Join(a.builtinStatus(160, 160), "\n")); !strings.Contains(s, "≈$0.123") {
		t.Errorf("subscription cost is an estimate: %q", s)
	}
}

func TestStatusCostOnlyWithPrices(t *testing.T) {
	a := statusApp(t, nil)
	a.usage.cost = 0
	if s := tui.StripEscapes(strings.Join(a.builtinStatus(160, 160), "\n")); strings.Contains(s, "$") {
		t.Errorf("a model without prices shows no cost: %q", s)
	}
	a = statusApp(t, &ai.ModelCost{Output: 5})
	if s := tui.StripEscapes(strings.Join(a.builtinStatus(160, 160), "\n")); !strings.Contains(s, "$0.123") {
		t.Errorf("a priced model shows the cost: %q", s)
	}
}

func TestStatusNoUsageYet(t *testing.T) {
	a := statusApp(t, nil)
	a.usage = usageStats{}
	s := tui.StripEscapes(strings.Join(a.builtinStatus(100, 100), "\n"))
	for _, no := range []string{"↑", "↓", "R0", "W0", "$"} {
		if strings.Contains(s, no) {
			t.Errorf("fresh session shows %q: %q", no, s)
		}
	}
}

// The model's name carries its provider only when another provider offers
// a model of the same name.
func TestStatusModelNameShowsProviderOnCollision(t *testing.T) {
	luna := config.Model{ID: "luna", Name: "GPT-6 Luna", ContextWindow: 262000}
	models := config.ModelsFile{Providers: map[string]config.Provider{
		"openai":      {Models: []config.Model{luna}},
		"opencode-go": {Models: []config.Model{luna, {ID: "kimi", Name: "Kimi"}}},
	}}
	first := func(a *App) string { return tui.StripEscapes(a.builtinStatus(160, 160)[0]) }
	for _, c := range []struct {
		provider string
		model    config.Model
		want     string
	}{
		{"openai", luna, "◆ GPT-6 Luna · openai"},
		{"opencode-go", luna, "◆ GPT-6 Luna · opencode-go"},
		{"opencode-go", models.Providers["opencode-go"].Models[1], "◆ Kimi"},
	} {
		a := statusApp(t, nil)
		a.models = models
		a.info.Model = c.provider + "/" + c.model.ID
		if got := first(a); !strings.HasPrefix(strings.TrimSpace(got), c.want+" ") {
			t.Errorf("%s/%s: %q, want %q", c.provider, c.model.ID, got, c.want)
		}
		if h := a.headerModel(); h != strings.TrimPrefix(c.want, "◆ ") {
			t.Errorf("%s/%s header: %q", c.provider, c.model.ID, h)
		}
	}

	// The rows are kept between frames, but follow the name when the model
	// (or the provider list) changes.
	a := statusApp(t, nil)
	a.models = models
	a.info.Model = "openai/luna"
	if got := first(a); !strings.Contains(got, "GPT-6 Luna · openai") {
		t.Fatalf("before: %q", got)
	}
	a.models = config.ModelsFile{Providers: map[string]config.Provider{"openai": {Models: []config.Model{luna}}}}
	if got := first(a); !strings.Contains(got, "◆ GPT-6 Luna ") || strings.Contains(got, "openai") {
		t.Fatalf("after the other provider went: %q", got)
	}
}

// setTestModel makes ref the model the runtime reports, in models of its
// own (provider "t" when it has none).
func setTestModel(a *App, ref config.ModelRef) {
	if ref.ProviderName == "" {
		ref.ProviderName = "t"
	}
	p := ref.Provider
	p.Models = []config.Model{ref.Model}
	a.models = config.ModelsFile{Providers: map[string]config.Provider{ref.ProviderName: p}}
	a.info.Model = ref.ProviderName + "/" + ref.Model.ID
	a.info.AutoCompactLimit = agent.AutoCompactLimit(ref.Model)
}

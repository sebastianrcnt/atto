package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/ai"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/tui"
)

func TestContextModeResume(t *testing.T) {
	for _, key := range []string{"ATTO_AGENT", config.EnvLegacyAgent, "ATTO_SESSION_ID"} {
		t.Setenv(key, "")
	}
	a := treeApp(t)
	m := a.model()
	m.Model.ContextWindow, m.Model.MaxTokens = 1050000, 128000
	m.Model.Cost = &ai.ModelCost{Input: 0.1, Tiers: []ai.ModelCostTier{{InputTokensAbove: 272000, Input: 0.2}}}
	a.agent.SetModel(m)
	a.cmdContext("")
	block := a.ui.Body.Children[len(a.ui.Body.Children)-1]
	text := tui.StripEscapes(strings.Join(block.Render(250), "\n"))
	for _, want := range []string{"244.8k", "costs more above 272.0k", "/context long"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	for _, mode := range []string{"long", "normal"} {
		a.cmdContext(mode)
		want := mode == "long"
		if a.agent.LongContext() != want {
			t.Fatal(mode)
		}
		saved, file, err := core.Open(a.sess.Path)
		if err != nil {
			t.Fatal(err)
		}
		file.Close()
		if saved.LongContext != want {
			t.Fatal("persisted", mode)
		}
	}
	a.cmdContext("long")
	path := a.sess.Path
	a.closeSession()
	b := treeApp(t)
	b.resume(path)
	if !b.agent.LongContext() {
		t.Fatal("resume lost long context")
	}
	b.cmdClear("")
	if b.agent.LongContext() {
		t.Fatal("new session kept long context")
	}
}

func TestTierStatus(t *testing.T) {
	for _, key := range []string{"ATTO_AGENT", config.EnvLegacyAgent, "ATTO_SESSION_ID"} {
		t.Setenv(key, "")
	}
	t.Setenv("ATTO_DIR", t.TempDir())
	cost := &ai.ModelCost{Input: 0.1, Tiers: []ai.ModelCostTier{{InputTokensAbove: 272000, Input: 0.2}}}
	a := statusApp(t, cost)
	m := a.model()
	m.Model.ContextWindow = 1050000
	a.agent.SetModel(m)
	a.ctxTokens = 300000
	for _, tc := range []struct {
		tokens int
		marker bool
	}{{272000, false}, {272001, true}, {300000, true}, {200000, false}} {
		a.usage.last.PromptTokens = tc.tokens
		text := tui.StripEscapes(strings.Join(a.builtinStatus(240, 240), "\n"))
		if strings.Contains(text, "×2") != tc.marker || !strings.Contains(text, "28%") {
			t.Fatal(tc, text)
		}
	}
	a.agent.SetLongContext(true)
	text := tui.StripEscapes(strings.Join(a.builtinStatus(240, 240), "\n"))
	if !strings.Contains(text, "28% long") {
		t.Fatal(text)
	}
	a.usage.last.PromptTokens = 300000
	a.usage.lastCost = cost
	a.agent.SetModel(config.ModelRef{Model: config.Model{ID: "free"}})
	if got := a.surcharge(a.model()); got != " ×2" {
		t.Fatal("model switch lost last price", got)
	}
	a.agent.SetLongContext(false)
	if strings.Contains(tui.StripEscapes(strings.Join(a.builtinStatus(240, 240), "\n")), " long") {
		t.Fatal("stale context mode")
	}
}

func TestCompactBlockReason(t *testing.T) {
	d := &details{}
	for _, tc := range []struct{ reason, want string }{
		{agent.ReasonPriceTier, "price tier above 272.0k"},
		{agent.ReasonSetting, "compaction.limits 272.0k"},
		{"", ""},
	} {
		for _, running := range []bool{true, false} {
			b := &compactBlock{d: d, auto: true, running: running, reason: tc.reason, cap: 272000}
			text := tui.StripEscapes(strings.Join(b.Render(200), "\n"))
			if tc.want == "" && strings.Contains(text, "272.0k") || !strings.Contains(text, tc.want) {
				t.Errorf("%q running=%v: %s", tc.reason, running, text)
			}
		}
	}
}

func TestStatusShowsTierTrigger(t *testing.T) {
	for _, key := range []string{"ATTO_AGENT", config.EnvLegacyAgent, "ATTO_SESSION_ID"} {
		t.Setenv(key, "")
	}
	t.Setenv("ATTO_DIR", t.TempDir())
	a := statusApp(t, &ai.ModelCost{Input: 0.1, Tiers: []ai.ModelCostTier{{InputTokensAbove: 272000, Input: 0.2}}})
	m := a.model()
	m.Model.ContextWindow, m.Model.MaxTokens = 1050000, 128000
	a.agent.SetModel(m)
	a.ctxTokens = 100000
	row := func() string { return tui.StripEscapes(strings.Join(a.builtinStatus(240, 240), "\n")) }
	if !strings.Contains(row(), "100.0k/1.1M ⇥244.8k") {
		t.Fatal(row())
	}
	a.agent.SetLongContext(true)
	if strings.Contains(row(), "⇥") {
		t.Fatal("long context has no tier trigger", row())
	}
}

func TestContextWithoutTierData(t *testing.T) {
	for _, cost := range []*ai.ModelCost{nil, {Input: 1}} {
		a := treeApp(t)
		m := a.model()
		m.Model.Cost = cost
		m.Model.ContextWindow = 100000
		a.agent.SetModel(m)
		a.cmdContext("")
		got := tui.StripEscapes(strings.Join(a.ui.Body.Render(200), "\n"))
		if !strings.Contains(got, "No tier cap: no tier data known for this model.") || !strings.Contains(got, "Auto-compacts at") {
			t.Fatal(got)
		}
	}
}

func TestPriceTierSelectionNotices(t *testing.T) {
	a := loadedApp(t)
	writeTestFile(t, config.ModelsPath(), `{"providers":{"t":{"baseUrl":"http://127.0.0.1:9/v1","models":[{"id":"m","contextWindow":100000,"cost":{"input":1}}]}}}`)
	models, err := config.LoadModels()
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := models.Find("t", "m")
	a.agent.SetModel(ref)
	a.models = models
	a.showLoaded() // startup/resume
	a.cmdModel("t/m")
	a.reloadNow(false)
	got := tui.StripEscapes(strings.Join(a.ui.Body.Render(300), "\n"))
	if n := strings.Count(got, "No price-tier cap for t/m:"); n != 3 {
		t.Fatalf("%d notices: %s", n, got)
	}
	// Finding the selected model, even without tiers, silences notices.
	writeTestFile(t, filepath.Join(config.Dir(), "cache", "catalog.json"), `{"t":{"models":{"m":{}}}}`)
	a.cmdModel("t/m")
	a.reloadNow(false)
	got = tui.StripEscapes(strings.Join(a.ui.Body.Render(300), "\n"))
	if n := strings.Count(got, "No price-tier cap for t/m:"); n != 3 {
		t.Fatalf("flat catalog model got a warning: %s", got)
	}
}

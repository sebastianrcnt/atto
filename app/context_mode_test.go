package app

import (
	"encoding/json"
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
	setRuntimeModel(t, a, m)
	a.ui.Do(func() { a.cmdContext("") })
	settle(a)
	block := a.ui.Body.Children[len(a.ui.Body.Children)-1]
	text := tui.StripEscapes(strings.Join(block.Render(250), "\n"))
	for _, want := range []string{"244.8k", "costs more above 272.0k", "/context long"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	for _, mode := range []string{"long", "normal"} {
		a.ui.Do(func() { a.cmdContext(mode) })
		settle(a)
		want := mode == "long"
		if a.info.LongContext != want {
			t.Fatal(mode)
		}
		saved, file, err := core.Open(a.sessPath)
		if err != nil {
			t.Fatal(err)
		}
		file.Close()
		if saved.LongContext != want {
			t.Fatal("persisted", mode)
		}
	}
	a.ui.Do(func() { a.cmdContext("long") })
	settle(a)
	a.shutdown()
	b := startApp(t, a.cwd, Options{Session: a.threadID})
	settle(b)
	if !b.info.LongContext {
		t.Fatal("resume lost long context")
	}
	b.ui.Do(func() { b.cmdClear("") })
	settle(b)
	if b.info.LongContext {
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
	setTestModel(a, m)
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
	setTestLongContext(a, true)
	text := tui.StripEscapes(strings.Join(a.builtinStatus(240, 240), "\n"))
	if !strings.Contains(text, "28% long") {
		t.Fatal(text)
	}
	a.usage.last.PromptTokens = 300000
	a.usage.lastCost = cost
	setTestModel(a, config.ModelRef{Model: config.Model{ID: "free"}})
	if got := a.surcharge(a.model()); got != " ×2" {
		t.Fatal("model switch lost last price", got)
	}
	setTestLongContext(a, false)
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
	setTestModel(a, m)
	a.ctxTokens = 100000
	row := func() string { return tui.StripEscapes(strings.Join(a.builtinStatus(240, 240), "\n")) }
	if !strings.Contains(row(), "100k/1.1M ⇥245k") {
		t.Fatal(row())
	}
	setTestLongContext(a, true)
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
		setRuntimeModel(t, a, m)
		a.ui.Do(func() { a.cmdContext("") })
		settle(a)
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
	a.ui.Do(func() { a.rpcErr("models/reload", nil) })
	a.models = models
	typeLine(a, "/reload")
	settle(a) // startup/resume-equivalent loaded notice
	a.cmdModel("t/m")
	typeLine(a, "/reload")
	settle(a)
	got := tui.StripEscapes(strings.Join(a.ui.Body.Render(300), "\n"))
	if n := strings.Count(got, "No price-tier cap for t/m:"); n != 3 {
		t.Fatalf("%d notices: %s", n, got)
	}
	// Finding the selected model, even without tiers, silences notices.
	writeTestFile(t, filepath.Join(config.Dir(), "cache", "catalog.json"), `{"t":{"models":{"m":{}}}}`)
	a.cmdModel("t/m")
	typeLine(a, "/reload")
	settle(a)
	got = tui.StripEscapes(strings.Join(a.ui.Body.Render(300), "\n"))
	if n := strings.Count(got, "No price-tier cap for t/m:"); n != 3 {
		t.Fatalf("flat catalog model got a warning: %s", got)
	}
}

// setRuntimeModel changes the runtime's model catalog, not an app-owned agent.
func setRuntimeModel(t *testing.T, a *App, ref config.ModelRef) {
	t.Helper()
	if a.conn == nil {
		setTestModel(a, ref)
		return
	}
	if ref.ProviderName == "" {
		ref.ProviderName = "t"
	}
	models := a.models
	if models.Providers == nil {
		models.Providers = map[string]config.Provider{}
	}
	p := models.Providers[ref.ProviderName]
	p.Models = []config.Model{ref.Model}
	models.Providers[ref.ProviderName] = p
	raw, err := json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, config.ModelsPath(), string(raw))
	a.ui.Do(func() {
		a.models = models
		a.rpcErr("models/reload", nil)
		a.rpcErr("thread/setModel", map[string]any{"model": ref.ProviderName + "/" + ref.Model.ID})
	})
	settle(a)
}

func setTestLongContext(a *App, long bool) {
	a.info.LongContext = long
	ref := a.model()
	if long {
		ref.Model.Cost = nil
	}
	a.info.AutoCompactLimit = agent.AutoCompactLimit(ref.Model)
	a.info.AutoCompactCap = 0
	if !long {
		a.info.AutoCompactCap = ref.Model.Cost.ContextPriceBoundary()
	}
}

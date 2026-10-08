package config

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/ai"
)

const fixture = `{"opencode-go":{"models":{
 "glm-5.3":{"id":"glm-5.3","name":"GLM-5.3","tool_call":true,"reasoning":true,"limit":{"context":1000000,"output":131072}},
 "kimi-k2.6":{"id":"kimi-k2.6","name":"Kimi K2.6","tool_call":true,"reasoning":true,"modalities":{"input":["text","image","video"],"output":["text"]},"limit":{"context":262144,"output":65536}},
 "qwen3.8-flash":{"id":"qwen3.8-flash","tool_call":true,"reasoning":true,"limit":{"context":1000000,"output":131072},"provider":{"npm":"@ai-sdk/anthropic"}},
 "old":{"id":"old","tool_call":true,"status":"deprecated","limit":{"context":1000,"output":100}},
 "notools":{"id":"notools","tool_call":false,"limit":{"context":1000,"output":100}}
}}}`

func TestCatalogAndMerge(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ATTO_DIR", dir)
	t.Setenv("OPENCODE_API_KEY", "")
	os.MkdirAll(filepath.Join(dir, "cache"), 0o755)
	os.WriteFile(filepath.Join(dir, "cache", "catalog.json"), []byte(fixture), 0o644)

	// No key: preset hidden.
	m, err := LoadModels()
	if err != nil || len(m.Providers) != 0 {
		t.Fatalf("providers without key: %v %v", m.Providers, err)
	}

	if err := SetAPIKey("opencode-go", "k-123"); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(AuthPath()); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("auth.json mode %v", st.Mode())
	}
	os.WriteFile(ModelsPath(), []byte(`{"providers":{"opencode-go":{"models":[{"id":"glm-5.3","name":"My GLM","efforts":["high"]}]}}}`), 0o644)
	m, err = LoadModels()
	if err != nil {
		t.Fatal(err)
	}
	refs := m.List()
	if len(refs) != 2 {
		t.Fatalf("want glm-5.3 and kimi-k2.6, got %+v", refs)
	}
	glm, ok := m.Find("", "opencode-go/glm-5.3")
	if !ok || glm.Model.Name != "My GLM" || glm.APIKey != "k-123" || len(glm.Provider.Headers) != 0 {
		t.Fatalf("override/key/header: %+v", glm)
	}
	kimi, _ := m.Find("opencode-go", "kimi-k2.6")
	if !kimi.Model.Images() || strings.Join(kimi.Model.Input, ",") != "text,image" || glm.Model.Images() {
		t.Fatalf("modalities: kimi %v, glm %v", kimi.Model.Input, glm.Model.Input)
	}
	body := kimi.RequestBody()
	if _, has := body["reasoning_effort"]; has || body["thinking"] == nil || len(kimi.Model.Levels()) != 2 {
		t.Fatalf("kimi quirk: %+v %v", kimi.Model, body)
	}
}

func TestCatalogEffortOff(t *testing.T) {
	m, _ := catalogModel(catalogProvider{name: "opencode-go"}, "deepseek-v4.1-flash", modelsDevModel{ToolCall: true, Reasoning: true})
	if *m.EffortMap["off"] != "none" || m.Levels()[0] != "off" {
		t.Fatalf("deepseek off: %+v", m)
	}
	g, _ := catalogModel(catalogProvider{name: "opencode-go"}, "glm-5.3", modelsDevModel{ToolCall: true, Reasoning: true})
	for _, e := range g.Levels() {
		if e == "off" {
			t.Fatalf("glm-5.3 cannot disable thinking: %v", g.Efforts)
		}
	}
}

func TestEffortMapLikePi(t *testing.T) {
	ds, _ := catalogModel(catalogProvider{name: "opencode-go"}, "deepseek-v4-pro", modelsDevModel{ToolCall: true, Reasoning: true})
	if got := ds.Levels(); strings.Join(got, ",") != "off,high,max" {
		t.Fatalf("deepseek-v4-pro levels %v", got)
	}
	fl, _ := catalogModel(catalogProvider{name: "opencode-go"}, "glm-5.3-flash", modelsDevModel{ToolCall: true, Reasoning: true})
	if fl.Levels()[0] != "off" {
		t.Fatalf("glm-5.3-flash should keep off: %v", fl.Levels())
	}
	// A user override changes one level and keeps the rest.
	user := Model{ID: "deepseek-v4-pro", EffortMap: map[string]*string{"off": nil, "xhigh": new("max")}}
	m := mergeModel(ds, user)
	if got := strings.Join(m.Levels(), ","); got != "high,xhigh,max" {
		t.Fatalf("merged levels %s", got)
	}
	if *m.EffortMap["xhigh"] != "max" {
		t.Fatalf("wire %v", m.EffortMap)
	}
}

// costFixture has models.dev's cost fields: dollars per million tokens,
// tiers above a context size, and the older context_over_200k.
const costFixture = `{
"openai":{"models":{
 "gpt-5.4":{"id":"gpt-5.4","tool_call":true,"limit":{"context":1050000,"output":128000},
  "cost":{"input":2.5,"output":15,"cache_read":0.25,"tiers":[{"input":5,"output":22.5,"tier":{"type":"context","size":272000}}],"context_over_200k":{"input":5,"output":22.5,"cache_read":0.5}}},
 "gpt-4.1":{"id":"gpt-4.1","tool_call":true,"limit":{"context":1000000,"output":32768},"cost":{"input":2,"output":8,"cache_read":0.5}}
}},
"opencode":{"models":{
 "glm-5":{"id":"glm-5","tool_call":true,"limit":{"context":200000,"output":100},"cost":{"input":1,"output":3.2,"cache_read":0.2,"cache_write":0}}
}},
"opencode-go":{"models":{
 "qwen3.6-plus":{"id":"qwen3.6-plus","tool_call":true,"limit":{"context":1000000,"output":100},
  "cost":{"input":0.5,"output":3,"cache_read":0.05,"cache_write":0.625,"context_over_200k":{"input":2,"output":6}}},
 "nocost":{"id":"nocost","tool_call":true,"limit":{"context":1000,"output":100}}
}}}`

func TestCatalogCost(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ATTO_DIR", dir)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENCODE_API_KEY", "k")
	os.MkdirAll(filepath.Join(dir, "cache"), 0o755)
	os.WriteFile(filepath.Join(dir, "cache", "catalog.json"), []byte(costFixture), 0o644)

	cat := CatalogProviders()
	find := func(p, id string) Model {
		t.Helper()
		for _, m := range cat[p].Models {
			if m.ID == id {
				return m
			}
		}
		t.Fatalf("%s/%s missing: %+v", p, id, cat[p].Models)
		return Model{}
	}
	want := ai.ModelCost{
		Input: 2.5, Output: 15, CacheRead: 0.25,
		// The tier lists no cache read: it keeps the base price.
		Tiers: []ai.ModelCostTier{{InputTokensAbove: 272000, Input: 5, Output: 22.5, CacheRead: 0.25}},
	}
	if c := find("openai", "gpt-5.4").Cost; c == nil || !reflect.DeepEqual(*c, want) {
		t.Fatalf("openai gpt-5.4 cost %+v", c)
	}
	// ChatGPT models are priced as OpenAI's API.
	if c := find("openai-codex", "gpt-5.4").Cost; c == nil || !reflect.DeepEqual(*c, want) {
		t.Fatalf("codex gpt-5.4 cost %+v", c)
	}
	if c := find("opencode", "glm-5").Cost; c == nil || c.Input != 1 || c.Output != 3.2 || c.CacheRead != 0.2 || c.Tiers != nil {
		t.Fatalf("zen glm-5 cost %+v", c)
	}
	q := find("opencode-go", "qwen3.6-plus").Cost
	if q == nil || len(q.Tiers) != 1 || q.Tiers[0] != (ai.ModelCostTier{InputTokensAbove: 200000, Input: 2, Output: 6, CacheRead: 0.05, CacheWrite: 0.625}) {
		t.Fatalf("context_over_200k becomes a tier: %+v", q)
	}
	if c := find("opencode-go", "nocost").Cost; c != nil {
		t.Fatalf("a model without prices has no cost: %+v", c)
	}
	for p, sub := range map[string]bool{"openai": false, "openai-codex": true, "opencode": false, "opencode-go": true} {
		if cat[p].Subscription != sub {
			t.Errorf("%s subscription = %v", p, cat[p].Subscription)
		}
	}

	// The tier applies above its size.
	am := (ModelRef{ProviderName: "openai", Provider: cat["openai"], Model: find("openai", "gpt-5.4")}).AIModel()
	u := ai.Usage{Input: 300000, Output: 1000}
	if got := ai.CalculateCost(&am, &u).Total; math.Abs(got-(300000*5+1000*22.5)/1e6) > 1e-9 {
		t.Fatalf("tiered cost %v", got)
	}

	// models.json wins: a cost replaces the catalog's, a modelOverrides
	// entry changes the prices it names.
	os.WriteFile(ModelsPath(), []byte(`{"providers":{
	 "opencode":{"models":[{"id":"glm-5","cost":{"input":9,"output":9}}]},
	 "opencode-go":{"modelOverrides":{"qwen3.6-plus":{"cost":{"output":7}}}}}}`), 0o644)
	m, err := LoadModels()
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := m.Find("opencode", "glm-5"); r.Model.Cost == nil || !reflect.DeepEqual(*r.Model.Cost, ai.ModelCost{Input: 9, Output: 9}) {
		t.Fatalf("models.json cost: %+v", r.Model.Cost)
	}
	r, _ := m.Find("opencode-go", "qwen3.6-plus")
	if c := r.Model.Cost; c == nil || c.Output != 7 || c.Input != 0.5 || len(c.Tiers) != 1 || !r.Provider.Subscription {
		t.Fatalf("override cost: %+v %+v", c, r.Provider)
	}
}

func TestPriceTierNotice(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(catalogPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	flat := &ai.ModelCost{Input: 1}
	tiered := &ai.ModelCost{Input: 1, Tiers: []ai.ModelCostTier{{InputTokensAbove: 200000, Input: 2}}}
	for _, tc := range []struct {
		name, catalog, provider, id string
		cost                        *ai.ModelCost
		want                        string
	}{
		{"missing cache", "", "openai", "flat", flat, "could not be loaded"},
		{"invalid cache", "{", "openai", "flat", flat, "could not be loaded"},
		{"null cache", "null", "openai", "flat", flat, "could not be loaded"},
		{"known flat", `{"openai":{"models":{"flat":{}}}}`, "openai", "flat", flat, ""},
		{"subscription alias", `{"openai":{"models":{"flat":{}}}}`, "openai-codex", "flat", flat, ""},
		{"unknown model", `{"openai":{"models":{"flat":{}}}}`, "openai", "new", flat, "not in the cached"},
		{"unknown provider", `{}`, "custom", "flat", flat, "not in the cached"},
		{"explicit tiers", "", "custom", "tiered", tiered, ""},
		{"unpriced", "", "local", "m", nil, ""},
		{"free", "", "local", "m", &ai.ModelCost{}, ""},
		{"output priced", "", "custom", "m", &ai.ModelCost{Output: 1}, "could not be loaded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.catalog == "" {
				_ = os.Remove(catalogPath())
			} else if err := os.WriteFile(catalogPath(), []byte(tc.catalog), 0o644); err != nil {
				t.Fatal(err)
			}
			m := ModelRef{ProviderName: tc.provider, Model: Model{ID: tc.id, Cost: tc.cost}}
			got := PriceTierNotice(m)
			if tc.want == "" && got != "" || !strings.Contains(got, tc.want) {
				t.Fatalf("notice %q, want %q", got, tc.want)
			}
		})
	}
}

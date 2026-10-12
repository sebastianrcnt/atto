package agent

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/ai"
	"github.com/sebastianrcnt/atto/auth"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/provider"
)

// The catalog as models.dev lists the GPT-6 models: a 922k input window
// and a 272k context tier.
const subscriptionCatalog = `{"openai":{"models":{
 "gpt-6.1-sol":{"id":"gpt-6.1-sol","name":"GPT-6.1 Sol","tool_call":true,"reasoning":true,"limit":{"context":1050000,"input":922000,"output":128000},
  "cost":{"input":2,"output":10,"tiers":[{"input":4,"output":15,"tier":{"type":"context","size":272000}}]}},
 "gpt-6-luna":{"id":"gpt-6-luna","name":"GPT-6 Luna","tool_call":true,"reasoning":true,"limit":{"context":1050000,"input":922000,"output":128000},
  "cost":{"input":2,"output":10,"tiers":[{"input":4,"output":15,"tier":{"type":"context","size":272000}}]}}
}}}`

// subscriptionModels loads the catalog with an API key, or with a fake
// Sign in with ChatGPT credential (never a real one).
func subscriptionModels(t *testing.T, login bool) config.ModelsFile {
	t.Helper()
	for _, key := range []string{"ATTO_AGENT", config.EnvLegacyAgent, "ATTO_SESSION_ID", "ATTO_TOOL_CALL_ID", "OPENAI_API_KEY"} {
		t.Setenv(key, "")
	}
	dir := t.TempDir()
	t.Setenv("ATTO_DIR", dir)
	os.MkdirAll(filepath.Join(dir, "cache"), 0o755)
	if err := os.WriteFile(filepath.Join(dir, "cache", "catalog.json"), []byte(subscriptionCatalog), 0o644); err != nil {
		t.Fatal(err)
	}
	if login {
		err := config.SetOAuth("openai", auth.Credential{Access: "fake", Refresh: "fake", Expires: time.Now().Add(time.Hour).UnixMilli()})
		if err != nil {
			t.Fatal(err)
		}
	} else {
		t.Setenv("OPENAI_API_KEY", "sk-fake")
	}
	m, err := config.LoadModels()
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSubscriptionContextCompaction(t *testing.T) {
	for _, tc := range []struct {
		name   string
		login  bool
		model  string
		sub    bool
		reason string
		cap    int
		limit  int
		x      float64 // input price multiplier at 300k
		cost   float64 // 300k input, 1k output
	}{
		// API key: the 272k tier caps at 90% of it.
		{"sol api key", false, "gpt-6.1-sol", false, ReasonPriceTier, 272000, 244800, 2, 1.215},
		// ChatGPT login: no long-context fee up to the 500k subscription context.
		{"sol login", true, "gpt-6.1-sol", true, ReasonSubscription, 500000, 450000, 1, 0.61},
		// Only 6.1 Sol's fee was waived: Luna keeps its tier on the login too.
		{"luna login", true, "gpt-6-luna", true, ReasonPriceTier, 272000, 244800, 2, 1.215},
		{"luna api key", false, "gpt-6-luna", false, ReasonPriceTier, 272000, 244800, 2, 1.215},
	} {
		t.Run(tc.name, func(t *testing.T) {
			models := subscriptionModels(t, tc.login)
			ref, ok := models.Find("openai", tc.model)
			if !ok {
				t.Fatal("model missing")
			}
			if ref.Provider.Subscription != tc.sub {
				t.Fatalf("subscription %v", ref.Provider.Subscription)
			}
			// The window stays the catalog's: only the cap and trigger move.
			if ref.Model.ContextWindow != 922000 {
				t.Fatalf("window %d", ref.Model.ContextWindow)
			}
			if x := ref.Model.Cost.InputMultiplier(300000); x != tc.x {
				t.Fatalf("input multiplier at 300k: %v", x)
			}
			usage := ai.Usage{Input: 300000, Output: 1000}
			am := ref.AIModel()
			if got := ai.CalculateCost(&am, &usage).Total; math.Abs(got-tc.cost) > 1e-9 {
				t.Fatalf("cost %v, want %v", got, tc.cost)
			}
			if got := AutoCompactLimit(ref); got != tc.limit {
				t.Fatalf("AutoCompactLimit %d", got)
			}
			a := New(ref, "", t.TempDir())
			if limit, cap, reason := a.CompactionPlan(); limit != tc.limit || cap != tc.cap || reason != tc.reason {
				t.Fatalf("plan %d %d %q", limit, cap, reason)
			}
			a.messages = []provider.Message{{Role: "user", Content: "hello"}}
			a.LastUsage.PromptTokens = tc.limit
			if !a.needsCompact() {
				t.Fatal("trigger not reached")
			}
			a.LastUsage.PromptTokens = tc.limit - 1
			if a.needsCompact() {
				t.Fatal("compacts early")
			}

			// settings.json and /context long still take precedence.
			a.SetCompaction(&config.Compaction{Limits: map[string]int{"openai/" + tc.model: 200000}})
			if limit, cap, reason := a.CompactionPlan(); limit != 180000 || cap != 200000 || reason != ReasonSetting {
				t.Fatalf("setting: %d %d %q", limit, cap, reason)
			}
			a.SetCompaction(&config.Compaction{Limits: map[string]int{"openai/" + tc.model: 0}})
			if limit, cap, reason := a.CompactionPlan(); limit != 829800 || cap != 0 || reason != "" {
				t.Fatalf("setting off: %d %d %q", limit, cap, reason)
			}
			a.SetCompaction(nil)
			a.SetLongContext(true)
			if limit, cap, _ := a.CompactionPlan(); limit != 829800 || cap != 0 {
				t.Fatalf("long: %d %d", limit, cap)
			}
		})
	}
}

// The legacy openai-codex backend is a subscription too: the table applies
// when it offers the model (models.json can add it).
func TestSubscriptionContextCodex(t *testing.T) {
	m := config.Model{ID: "gpt-6.1-sol", ContextWindow: 922000, MaxTokens: 32768,
		Cost: &ai.ModelCost{Tiers: []ai.ModelCostTier{{InputTokensAbove: 272000}}}}
	codex := config.ModelRef{ProviderName: "openai-codex", Provider: config.Provider{Subscription: true}, Model: m}
	if got := AutoCompactLimit(codex); got != 450000 {
		t.Fatal("openai-codex", got)
	}
	// Other subscriptions (OpenCode Go) keep the price tier.
	other := config.ModelRef{ProviderName: "opencode-go", Provider: config.Provider{Subscription: true}, Model: m}
	if got := AutoCompactLimit(other); got != 244800 {
		t.Fatal("opencode-go", got)
	}
}

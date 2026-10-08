package agent

import (
	"testing"

	"github.com/sebastianrcnt/atto/ai"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/provider"
)

func TestCompactionLimits(t *testing.T) {
	for _, key := range []string{"ATTO_AGENT", "ATTO_SUBAGENT", "ATTO_SESSION_ID"} {
		t.Setenv(key, "")
	}
	t.Setenv("ATTO_DIR", t.TempDir())
	m := config.Model{ID: "luna", ContextWindow: 1050000, MaxTokens: 128000}
	if got := AutoCompactLimit(m); got != 922000 {
		t.Fatal(got)
	}
	m.Cost = &ai.ModelCost{Tiers: []ai.ModelCostTier{{InputTokensAbove: 500000}, {InputTokensAbove: 0}, {InputTokensAbove: 272000}}}
	if got := AutoCompactLimit(m); got != 244800 {
		t.Fatal(got)
	}
	a := New(config.ModelRef{ProviderName: "test", Model: m}, "", t.TempDir())
	a.messages = []provider.Message{{Role: "user", Content: "hello"}}
	a.LastUsage.PromptTokens = 244800
	if !a.needsCompact() {
		t.Fatal("tier must trigger compaction")
	}
	a.SetLongContext(true)
	if got, cap := a.CompactionLimit(); got != 922000 || cap != 0 || a.needsCompact() {
		t.Fatal(got, cap)
	}
	a.SetLongContext(false)
	for _, tc := range []struct{ cap, want int }{{0, 922000}, {1050000, 922000}, {2000000, 922000}, {100000, 90000}, {1000000, 900000}, {-1, 244800}} {
		a.SetCompaction(&config.Compaction{Limits: map[string]int{"test/luna": tc.cap}})
		if got, _ := a.CompactionLimit(); got != tc.want {
			t.Errorf("cap %d: %d, want %d", tc.cap, got, tc.want)
		}
	}
	a.SetCompaction(&config.Compaction{Limits: map[string]int{"other/luna": 0}})
	if got, _ := a.CompactionLimit(); got != 244800 {
		t.Fatal("provider/model key", got)
	}
	m.ContextWindow, m.MaxTokens = 200000, 100000
	if got := AutoCompactLimit(m); got != 100000 {
		t.Fatal("never raise window limit", got)
	}
	m.ContextWindow = 0
	if got := AutoCompactLimit(m); got != 0 {
		t.Fatal(got)
	}
}

func TestCompactionReason(t *testing.T) {
	for _, key := range []string{"ATTO_AGENT", "ATTO_SUBAGENT", "ATTO_SESSION_ID"} {
		t.Setenv(key, "")
	}
	t.Setenv("ATTO_DIR", t.TempDir())
	m := config.Model{ID: "luna", ContextWindow: 1050000, MaxTokens: 128000,
		Cost: &ai.ModelCost{Tiers: []ai.ModelCostTier{{InputTokensAbove: 272000}}}}
	a := New(config.ModelRef{ProviderName: "test", Model: m}, "", t.TempDir())
	for _, tc := range []struct {
		name   string
		limits map[string]int
		long   bool
		reason string
		cap    int
	}{
		{"tier", nil, false, ReasonPriceTier, 272000},
		{"setting", map[string]int{"test/luna": 200000}, false, ReasonSetting, 200000},
		{"setting off", map[string]int{"test/luna": 0}, false, "", 0},
		{"long", nil, true, "", 0},
		// the output room already compacts earlier than the cap would
		{"cap above output room", map[string]int{"test/luna": 1040000}, false, "", 0},
	} {
		a.SetCompaction(&config.Compaction{Limits: tc.limits})
		a.SetLongContext(tc.long)
		if _, cap, reason := a.compactionPlan(); reason != tc.reason || cap != tc.cap && tc.reason != "" {
			t.Errorf("%s: reason %q cap %d, want %q %d", tc.name, reason, cap, tc.reason, tc.cap)
		}
	}
}

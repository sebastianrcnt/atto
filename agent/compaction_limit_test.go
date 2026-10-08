package agent

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/ai"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/shell"
)

func TestCompactionLimits(t *testing.T) {
	for _, key := range []string{"ATTO_AGENT", config.EnvLegacyAgent, "ATTO_SESSION_ID"} {
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
	for _, key := range []string{"ATTO_AGENT", config.EnvLegacyAgent, "ATTO_SESSION_ID"} {
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

const bigUsage = `{"choices":[],"usage":{"prompt_tokens":950,"completion_tokens":10}}`

func smallWindowAgent(url string) *Agent {
	return New(config.ModelRef{ProviderName: "t", Provider: config.Provider{BaseURL: url},
		Model: config.Model{ID: "m", ContextWindow: 1000}}, "", os.TempDir())
}

// A reply that takes the context past the limit is followed by a
// compaction even when no tool ran: a steer sends the turn round again.
func TestCompactsBeforeTheRequestAfterASteer(t *testing.T) {
	srv, seen := fakeServer(t, append(text("first"), bigUsage), text("NOTES"), text("second"))
	a := smallWindowAgent(srv.URL)
	a.Steer("one more thing")
	compactions := 0
	err := a.Run(context.Background(), "go", func(ev any) {
		if _, ok := ev.(CompactStart); ok {
			compactions++
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if reqs := seen(); compactions != 1 || len(reqs) != 3 {
		t.Fatalf("%d compactions, %d requests", compactions, len(reqs))
	}
}

// Same for a Continue of a conversation already over the limit.
func TestContinueCompactsFirst(t *testing.T) {
	srv, seen := fakeServer(t, text("NOTES"), text("done"))
	a := smallWindowAgent(srv.URL)
	a.Restore([]session.Entry{
		{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "go"}},
		{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: "ok"}, Usage: &provider.Usage{PromptTokens: 950}},
		{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "more"}},
	})
	if err := a.Continue(context.Background(), func(any) {}); err != nil {
		t.Fatal(err)
	}
	if n := len(seen()); n != 2 {
		t.Fatalf("%d requests, want the compaction and the answer", n)
	}
}

// The message about to be added counts toward the limit, and it reaches the
// model whole: the compaction comes before it.
func TestCompactsBeforeALargeNewMessage(t *testing.T) {
	srv, seen := fakeServer(t,
		append(text("a"), `{"choices":[],"usage":{"prompt_tokens":500,"completion_tokens":10}}`),
		text("NOTES"), text("b"))
	a := smallWindowAgent(srv.URL)
	compactions := 0
	emit := func(ev any) {
		if _, ok := ev.(CompactStart); ok {
			compactions++
		}
	}
	if err := a.Run(context.Background(), "hi", emit); err != nil {
		t.Fatal(err)
	}
	if compactions != 0 {
		t.Fatal("compacted a small conversation")
	}
	paste := strings.Repeat("x", 1700) // ~425 tokens: 510 + 425 is past the 900 limit
	if err := a.Run(context.Background(), paste, emit); err != nil {
		t.Fatal(err)
	}
	reqs := seen()
	if compactions != 1 || len(reqs) != 3 {
		t.Fatalf("%d compactions, %d requests", compactions, len(reqs))
	}
	last := reqs[2][len(reqs[2])-1]["content"].(string)
	if !strings.HasSuffix(last, paste) {
		t.Fatalf("the message was cut: %.80q", last)
	}
}

func TestCompactsAfterToolRoundsWithoutUsage(t *testing.T) {
	command := "printf '%16000s' '' | tr ' ' x"
	if shell.Default().Kind == shell.PowerShell {
		command = "[Console]::Write(('x' * 16000))"
	}
	srv, seen := fakeServer(t, toolCall(command), toolCall(command), toolCall(command), text("NOTES"), text("done"))
	a := newTestAgent(srv.URL)
	a.model.Model.ContextWindow = 12000
	a.system, a.sinceUsage = "system", len("system")
	steps, compactions, retries := 0, 0, 0
	err := a.Run(context.Background(), "go", func(ev any) {
		switch ev.(type) {
		case StepEnd:
			steps++
		case CompactStart:
			compactions++
			if steps != 3 {
				t.Errorf("compaction after %d steps, want 3", steps)
			}
		case StreamRetry:
			retries++
		}
	})
	if err != nil || compactions != 1 || retries != 0 || len(seen()) != 5 {
		t.Fatalf("err %v, compactions %d, retries %d, requests %d", err, compactions, retries, len(seen()))
	}
}

func TestMissingUsageKeepsTheLastReportedEstimate(t *testing.T) {
	for _, usage := range []provider.Usage{{}, {PromptTokens: 100}, {CompletionTokens: 100}} {
		srv, _ := fakeServer(t, text("no usage"),
			append(text("reported usage"), `{"choices":[],"usage":{"prompt_tokens":200,"completion_tokens":10}}`))
		a := newTestAgent(srv.URL)
		a.LastUsage = usage
		a.sinceUsage = 40
		var recorded []session.Entry
		a.Record = func(e session.Entry) { recorded = append(recorded, e) }
		if err := a.Run(context.Background(), "go", func(any) {}); err != nil {
			t.Fatal(err)
		}
		want := usage.PromptTokens + usage.CompletionTokens + a.sinceUsage/4
		if a.LastUsage != usage || a.sinceUsage <= 40 || a.ContextTokens() != want {
			t.Fatalf("usage %+v, appended %d, context %d, want %d", a.LastUsage, a.sinceUsage, a.ContextTokens(), want)
		}
		restored := newTestAgent(srv.URL)
		entries := []session.Entry{{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: ""}, Usage: &usage},
			{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: strings.Repeat("x", 40)}}}
		restored.Restore(append(entries, recorded...))
		if restored.LastUsage != a.LastUsage || restored.ContextTokens() != a.ContextTokens() {
			t.Fatalf("restored usage %+v, context %d; live context %d", restored.LastUsage, restored.ContextTokens(), a.ContextTokens())
		}
		if err := a.Run(context.Background(), "again", func(any) {}); err != nil {
			t.Fatal(err)
		}
		if a.ContextTokens() != 210 || a.sinceUsage != 0 {
			t.Fatalf("reported usage did not reset estimate: %d, appended %d", a.ContextTokens(), a.sinceUsage)
		}
	}
}

func TestDenseTextEstimate(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		dense      bool
	}{
		{"prose", strings.Repeat("This is ordinary prose about the task. ", 20), false},
		{"short JSON", `{"value":123}`, false},
		{"JSON", strings.Repeat(`{"value":123,"ok":true},`, 20), true},
		{"logs", strings.Repeat("2026-10-08T12:34:56Z error=123 path=/usr/bin/test\n", 20), true},
		{"base64", strings.Repeat("YWJjZGVmMDEyMzQ1", 20), true},
		{"minified code", strings.Repeat("x=(a+b);f(x);", 20), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := len(tc.text)
			if tc.dense {
				want = (want*4 + 2) / 3
			}
			if got := estimateChars(tc.text); got != want {
				t.Fatalf("estimated characters %d, want %d", got, want)
			}
			a := &Agent{LastUsage: provider.Usage{PromptTokens: 100}}
			a.appendMessage(provider.Message{Role: "tool", Content: tc.text}, session.Entry{})
			if got := a.ContextTokens(); got != 100+want/4 {
				t.Fatalf("context %d, want %d", got, 100+want/4)
			}
			restored := &Agent{}
			restored.Restore([]session.Entry{
				{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant"}, Usage: &a.LastUsage},
				{Type: session.TypeMessage, Message: &provider.Message{Role: "tool", Content: tc.text}},
			})
			if restored.ContextTokens() != a.ContextTokens() {
				t.Fatal("restoring changed the estimate")
			}
		})
	}
}

func TestDenseToolResultsTriggerCompaction(t *testing.T) {
	a := &Agent{model: config.ModelRef{Model: config.Model{ContextWindow: 10000}},
		LastUsage: provider.Usage{PromptTokens: 6000}}
	for range 2 {
		a.appendMessage(provider.Message{Role: "tool", Content: strings.Repeat(`{"key":123},`, 450)}, session.Entry{})
	}
	if !a.needsCompact() {
		t.Fatalf("dense results did not trigger compaction: %d tokens", a.ContextTokens())
	}
	// At four bytes per token these same results would still be below 90%.
	if 6000+2*len(strings.Repeat(`{"key":123},`, 450))/4 >= 9000 {
		t.Fatal("test results would trigger even without the denser estimate")
	}
}

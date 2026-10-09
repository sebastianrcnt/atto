package server

import (
	"testing"

	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/ui"
)

func TestUIStatusUsesLatestResponseAndNativeSlotRoles(t *testing.T) {
	h := newHarness(t)
	th, _ := h.s.thread(h.id)
	if err := th.call(func() error {
		th.total = Usage{InputTokens: 70000, CachedInputTokens: 67900, OutputTokens: 107,
			LastInputTokens: 0, LastCachedInputTokens: 0, Last: &provider.Usage{PromptTokens: 70000, CachedTokens: 67900}}
		th.jobCount, th.timerCount = 1, 2
		want := map[string]string{"cache": "cache 97%", "tokens": "↑2.1k ↓107", "jobs": "● 1 job running (/jobs)", "timers": "⏱ 2 timers (/timers)"}
		for id, text := range want {
			n := th.statusTree(id)
			if n == nil || ui.PlainText(*n) != text {
				t.Fatalf("%s: %#v, want %s", id, n, text)
			}
			if err := ui.Validate(ui.Status, *n); err != nil {
				t.Fatal(err)
			}
		}
		if th.statusTree("activity") != nil || th.statusTree("memory") != nil {
			t.Fatal("worker heap/activity leaked into status")
		}
		th.customStatusConfigured = true
		th.customStatusLines = []string{"first", "second"}
		if n := th.statusTree("custom"); n == nil || ui.PlainText(*n) != "first\nsecond" {
			t.Fatal("custom lines collapsed")
		}
		for _, id := range []string{"model", "effort", "context", "contextSize", "path", "branch"} {
			if th.statusTree(id) != nil {
				t.Fatal("native slot leaks into custom status", id)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

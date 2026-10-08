package server

import (
	"encoding/json"
	"github.com/sebastianrcnt/atto/config"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/hooks/hooktest"
)

// A Stop hook that blocks once keeps the thread's turn going and shows as
// a hook notification; closing the server runs SessionEnd.
func TestServerStopAndSessionEndHooks(t *testing.T) {
	work := setup(t)
	log := filepath.Join(t.TempDir(), "end.log")
	settings := map[string]any{"hooks": map[string]any{
		"Stop": []any{map[string]any{"hooks": []any{map[string]any{"type": "command",
			"command": hooktest.StopOnce("run the tests")}}}},
		"SessionEnd": []any{map[string]any{"hooks": []any{map[string]any{"type": "command",
			"command": hooktest.LogStdinNoNewline(log)}}}},
	}}
	raw, _ := json.Marshal(settings)
	if err := os.MkdirAll(filepath.Join(work, ".atto"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".atto", "settings.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	projectHooks, err := config.ProjectHooks(work)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range projectHooks {
		if err := config.SetHookApproval(h, true); err != nil {
			t.Fatal(err)
		}
	}
	s := New("test", work)
	var mu sync.Mutex
	var hookMsgs []string
	completed := make(chan string, 4)
	s.Notify = func(method string, p map[string]any) {
		switch method {
		case "hook":
			mu.Lock()
			hookMsgs = append(hookMsgs, p["event"].(string)+": "+p["message"].(string))
			mu.Unlock()
		case "turn/completed":
			completed <- p["status"].(string)
		}
	}
	th := call(t, s, "thread/start", map[string]any{})
	id := th["threadId"].(string)
	call(t, s, "turn/start", map[string]any{"threadId": id, "input": "hello"})
	select {
	case st := <-completed:
		if st != "completed" {
			t.Fatalf("status %s", st)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("turn did not finish")
	}
	mu.Lock()
	got := strings.Join(hookMsgs, "|")
	mu.Unlock()
	if got != "Stop: run the tests" {
		t.Fatalf("hook notifications %q", got)
	}
	// The fake model alternates tool call and answer: a second round means
	// the Stop hook's reason sent the model back to work.
	r := call(t, s, "thread/read", map[string]any{"threadId": id})
	if n := strings.Count(itemTypes(r), "commandExecution"); n != 2 {
		t.Fatalf("turn should have continued: %s", itemTypes(r))
	}

	s.Close()
	b, _ := os.ReadFile(log)
	var in map[string]any
	if err := json.Unmarshal(b, &in); err != nil {
		t.Fatalf("SessionEnd log %q: %v", b, err)
	}
	if in["hook_event_name"] != "SessionEnd" || in["reason"] != "other" || in["session_id"] != id {
		t.Fatalf("SessionEnd input %v", in)
	}
}

func itemTypes(r map[string]any) string {
	items, _ := r["items"].([]any)
	var out []string
	for _, it := range items {
		out = append(out, it.(map[string]any)["type"].(string))
	}
	return strings.Join(out, ",")
}

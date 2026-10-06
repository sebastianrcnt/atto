package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/hooks/hooktest"
)

// Non-ASCII text survives the trip into a hook's stdin and out of its stdout.
func TestHookUTF8(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "in.json")
	r := New(cmd("UserPromptSubmit", "", hooktest.WriteStdinThenEchoText(out, "안녕 세계 héllo")), dir)
	o := r.UserPromptSubmit(context.Background(), "테스트 ✓ café")
	if o.Context != "안녕 세계 héllo" {
		t.Fatalf("hook output garbled: %q (%+v)", o.Context, o)
	}
	if got := readLines(t, out); len(got) != 1 || got[0]["prompt"] != "테스트 ✓ café" {
		t.Fatalf("hook input garbled: %v", got)
	}
}

// A timeout returns promptly even when the hook left a child holding its output.
func TestHookTimeoutWithChildProcess(t *testing.T) {
	cfg := map[string][]config.HookMatcher{"SessionEnd": {{Hooks: []config.HookSpec{{Type: "command", Command: hooktest.SpawnSleeper(), Timeout: 1}}}}}
	r := New(cfg, t.TempDir())
	start := time.Now()
	n := r.SessionEnd(context.Background(), "exit")
	if time.Since(start) > 10*time.Second || len(n) != 1 || !strings.Contains(n[0], "timed out") {
		t.Fatalf("hook should time out quickly: %v after %s", n, time.Since(start))
	}
}

// Tests for Stop continuation, SessionEnd and Notification.

func logHook(event, matcher, log string) map[string][]config.HookMatcher {
	return cmd(event, matcher, hooktest.LogStdin(log))
}

func readLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, _ := os.ReadFile(path)
	var out []map[string]any
	for l := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		if l = strings.TrimSpace(l); l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("%q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

func TestSessionEndInputAndMatcher(t *testing.T) {
	dir := t.TempDir()
	all, clear := filepath.Join(dir, "all"), filepath.Join(dir, "clear")
	cfg := logHook("SessionEnd", "", all)
	cfg["SessionEnd"] = append(cfg["SessionEnd"], logHook("SessionEnd", "clear|resume", clear)["SessionEnd"]...)
	r := New(cfg, dir)
	r.SetSession("s1", "/t.jsonl")
	for _, reason := range []string{"clear", "exit", "resume", "other"} {
		r.SessionEnd(context.Background(), reason)
	}
	got := readLines(t, all)
	if len(got) != 4 {
		t.Fatalf("unmatched hook should run for every reason: %v", got)
	}
	if got[1]["reason"] != "exit" || got[1]["hook_event_name"] != "SessionEnd" || got[1]["session_id"] != "s1" ||
		got[1]["transcript_path"] != "/t.jsonl" || got[1]["cwd"] != dir {
		t.Fatalf("input %v", got[1])
	}
	only := readLines(t, clear)
	if len(only) != 2 || only[0]["reason"] != "clear" || only[1]["reason"] != "resume" {
		t.Fatalf("matcher should select reasons: %v", only)
	}
}

func TestSessionEndCannotBlockAndNilRunner(t *testing.T) {
	r := New(cmd("SessionEnd", "", hooktest.BlockedByDecisionAndExit2()), t.TempDir())
	if n := r.SessionEnd(context.Background(), "exit"); len(n) != 0 {
		t.Fatalf("blocking output is ignored, got notices %v", n)
	}
	r = New(cmd("SessionEnd", "", hooktest.Exit1()), t.TempDir())
	if n := r.SessionEnd(context.Background(), "exit"); len(n) != 1 {
		t.Fatalf("errors are notices: %v", n)
	}
	var none *Runner
	none.SessionEnd(context.Background(), "exit")
	none.Notification(context.Background(), "idle_prompt", "x")
}

func TestSessionEndTimeout(t *testing.T) {
	cfg := map[string][]config.HookMatcher{"SessionEnd": {{Hooks: []config.HookSpec{{Type: "command", Command: hooktest.Sleep30(), Timeout: 1}}}}}
	r := New(cfg, t.TempDir())
	start := time.Now()
	n := r.SessionEnd(context.Background(), "exit")
	if time.Since(start) > 10*time.Second || len(n) != 1 || !strings.Contains(n[0], "timed out") {
		t.Fatalf("hook should time out quickly: %v after %s", n, time.Since(start))
	}
}

func TestNotificationInputAndMatcher(t *testing.T) {
	dir := t.TempDir()
	idle, other := filepath.Join(dir, "idle"), filepath.Join(dir, "other")
	cfg := logHook("Notification", "idle_prompt", idle)
	cfg["Notification"] = append(cfg["Notification"], logHook("Notification", "goal_blocked", other)["Notification"]...)
	r := New(cfg, dir)
	r.Notification(context.Background(), "idle_prompt", "waiting")
	r.Notification(context.Background(), "goal_blocked", "stuck")
	got := readLines(t, idle)
	if len(got) != 1 || got[0]["message"] != "waiting" || got[0]["notification_type"] != "idle_prompt" || got[0]["hook_event_name"] != "Notification" {
		t.Fatalf("idle %v", got)
	}
	if got = readLines(t, other); len(got) != 1 || got[0]["message"] != "stuck" {
		t.Fatalf("other %v", got)
	}
}

// modelServer answers each request with the next reply (the last repeats)
// and records the messages it was sent.
func modelServer(t *testing.T, replies ...string) (url string, requests func() [][]map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var reqs [][]map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		i := min(len(reqs), len(replies)-1)
		reqs = append(reqs, body.Messages)
		mu.Unlock()
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", replies[i])
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() [][]map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([][]map[string]any(nil), reqs...)
	}
}

const sayDone = `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`

func newAgent(url string, cfg map[string][]config.HookMatcher, cwd string) *agent.Agent {
	a := agent.New(config.ModelRef{Provider: config.Provider{BaseURL: url}, Model: config.Model{ID: "m"}}, "", cwd)
	a.Hooks = New(cfg, cwd)
	return a
}

// A Stop hook that always blocks keeps the turn going up to the cap, with
// stop_hook_active true after the first block, then the turn ends.
func TestStopHookCap(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "stop")
	url, requests := modelServer(t, sayDone)
	cfg := cmd("Stop", "", hooktest.LogStdinThenBlock(log))
	a := newAgent(url, cfg, dir)
	var notices []agent.HookNotice
	if err := a.Run(context.Background(), "go", func(ev any) {
		if n, ok := ev.(agent.HookNotice); ok {
			notices = append(notices, n)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if n := len(requests()); n != agent.MaxStopBlocks+1 {
		t.Fatalf("expected %d model calls, got %d", agent.MaxStopBlocks+1, n)
	}
	calls := readLines(t, log)
	if len(calls) != agent.MaxStopBlocks+1 || calls[0]["stop_hook_active"] != false || calls[1]["stop_hook_active"] != true ||
		calls[0]["hook_event_name"] != "Stop" {
		t.Fatalf("stop inputs: %v", calls)
	}
	last := notices[len(notices)-1]
	if last.Blocked || !strings.Contains(last.Message, "stopping anyway") {
		t.Fatalf("cap notice missing: %+v", notices)
	}
	blocked := 0
	for _, n := range notices {
		if n.Blocked && n.Message == "again" {
			blocked++
		}
	}
	if blocked != agent.MaxStopBlocks {
		t.Fatalf("blocked notices %d: %+v", blocked, notices)
	}
}

// A hook that honours stop_hook_active blocks once; exit code 2 works too.
func TestStopHookExit2ThenAllows(t *testing.T) {
	dir := t.TempDir()
	url, requests := modelServer(t, sayDone)
	cfg := cmd("Stop", "", hooktest.StopOnce("run the linter"))
	a := newAgent(url, cfg, dir)
	if err := a.Run(context.Background(), "go", func(any) {}); err != nil {
		t.Fatal(err)
	}
	reqs := requests()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 model calls, got %d", len(reqs))
	}
	if last := reqs[1][len(reqs[1])-1]; last["role"] != "user" || last["content"] != "[Stop hook] run the linter" {
		t.Fatalf("reason should reach the model as a user message: %v", last)
	}
}

// An interrupted turn does not run Stop hooks.
func TestNoStopHookOnInterrupt(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "stop")
	url, _ := modelServer(t, sayDone)
	a := newAgent(url, logHook("Stop", "", log), dir)
	ctx, cancel := context.WithCancel(context.Background())
	err := a.Run(ctx, "go", func(ev any) {
		if _, ok := ev.(agent.StepEnd); ok {
			cancel() // the user presses Esc as the model finishes
		}
	})
	if err == nil {
		t.Fatal("an interrupted turn returns its error")
	}
	if got := readLines(t, log); len(got) != 0 {
		t.Fatalf("Stop ran after an interrupt: %v", got)
	}
}

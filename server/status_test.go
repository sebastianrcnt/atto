package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/subagent"
)

// notes records a server's notifications.
type notes struct {
	mu   sync.Mutex
	list []msg
}

func record(s *Server) *notes {
	n := &notes{}
	s.Notify = func(method string, params map[string]any) {
		raw, _ := json.Marshal(params)
		var p map[string]any
		json.Unmarshal(raw, &p)
		n.mu.Lock()
		n.list = append(n.list, msg{Method: method, Params: p})
		n.mu.Unlock()
	}
	return n
}

// wait returns the first notification method since from that pred
// accepts, and where to look next.
func (n *notes) wait(t *testing.T, from int, method string, pred func(map[string]any) bool) (msg, int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		n.mu.Lock()
		for i := from; i < len(n.list); i++ {
			if m := n.list[i]; m.Method == method && (pred == nil || pred(m.Params)) {
				n.mu.Unlock()
				return m, i + 1
			}
		}
		n.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no %s", method)
	return msg{}, 0
}

// What the status line shows: the model, the session's usage (after each
// response, and again on resume), and the running turn.
func TestUsageAndTurnInfo(t *testing.T) {
	work := setup(t)
	s := New("test", work)
	t.Cleanup(s.Close)
	n := record(s)
	info := call(t, s, "thread/start", map[string]any{})
	id := info["threadId"].(string)
	if info["modelName"] != "m" || info["autoCompactLimit"].(float64) != 900 || info["priced"] != nil {
		t.Fatalf("model info %v", info)
	}
	call(t, s, "turn/start", map[string]any{"threadId": id, "input": "hello"})
	started, _ := n.wait(t, 0, "turn/started", nil)
	if started.Params["startedAt"].(float64) < float64(time.Now().Add(-time.Minute).UnixMilli()) {
		t.Fatalf("turn/started %v", started.Params)
	}
	first, i := n.wait(t, 0, "thread/usage", nil)
	if step := first.Params["step"].(map[string]any); step["inputTokens"].(float64) != 100 || step["outputTokens"].(float64) != 5 {
		t.Fatalf("first step %v", first.Params)
	}
	second, _ := n.wait(t, i, "thread/usage", nil)
	u := second.Params["usage"].(map[string]any)
	if u["inputTokens"].(float64) != 220 || u["cachedInputTokens"].(float64) != 100 || u["outputTokens"].(float64) != 7 || u["lastCachedInputTokens"].(float64) != 100 {
		t.Fatalf("totals %v", u)
	}
	n.wait(t, 0, "turn/completed", nil)
	read := call(t, s, "thread/read", map[string]any{"threadId": id})
	if read["turn"] != nil || read["usage"].(map[string]any)["outputTokens"].(float64) != 7 {
		t.Fatalf("read after the turn %v", read)
	}

	// A new server has the totals from the session file.
	s2 := New("test", work)
	t.Cleanup(s2.Close)
	r := call(t, s2, "thread/resume", map[string]any{"threadId": id})
	if u := r["usage"].(map[string]any); u["inputTokens"].(float64) != 220 || u["outputTokens"].(float64) != 7 {
		t.Fatalf("resumed totals %v", u)
	}
}

// A model name that two providers share shows the provider, in thread info
// and in models/list; other names stay.
func TestModelNameShowsProviderOnCollision(t *testing.T) {
	work := setup(t)
	url := fakeModel(t)
	models := `{"providers":{` +
		`"fake":{"baseUrl":"` + url + `","models":[{"id":"luna","name":"GPT-6 Luna","contextWindow":1000},{"id":"m","contextWindow":1000}]},` +
		`"other":{"baseUrl":"` + url + `","models":[{"id":"luna","name":"GPT-6 Luna","contextWindow":1000}]}}}`
	if err := os.WriteFile(config.ModelsPath(), []byte(models), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New("test", work)
	t.Cleanup(s.Close)
	info := call(t, s, "thread/start", map[string]any{"model": "fake/luna"})
	if info["modelName"] != "GPT-6 Luna · fake" || info["model"] != "fake/luna" {
		t.Fatalf("colliding name: %v", info)
	}
	id := info["threadId"].(string)
	if got := call(t, s, "thread/setModel", map[string]any{"threadId": id, "model": "other/luna"}); got["modelName"] != "GPT-6 Luna · other" {
		t.Fatalf("after setModel: %v", got)
	}
	if got := call(t, s, "thread/setModel", map[string]any{"threadId": id, "model": "fake/m"}); got["modelName"] != "m" {
		t.Fatalf("a name that is alone: %v", got)
	}
	names := map[string]string{}
	for _, m := range call(t, s, "models/list", nil)["models"].([]any) {
		m := m.(map[string]any)
		names[m["id"].(string)] = m["name"].(string)
	}
	want := map[string]string{"fake/luna": "GPT-6 Luna · fake", "other/luna": "GPT-6 Luna · other", "fake/m": "m"}
	if len(names) != len(want) {
		t.Fatalf("models/list names %v", names)
	}
	for id, name := range want {
		if names[id] != name {
			t.Errorf("models/list %s: %q, want %q", id, names[id], name)
		}
	}
}

// Steers are pending until the turn takes them, and can be taken back
// before.
func TestPendingSteers(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ATTO_DIR", dir)
	url := slowModel(t, 40, 20*time.Millisecond)
	os.WriteFile(filepath.Join(dir, "models.json"), []byte(`{"providers":{"fake":{"baseUrl":"`+url+`","models":[{"id":"m","contextWindow":100000}]}}}`), 0o644)
	s := New("test", t.TempDir())
	t.Cleanup(s.Close)
	n := record(s)
	id := call(t, s, "thread/start", map[string]any{})["threadId"].(string)
	call(t, s, "turn/start", map[string]any{"threadId": id, "input": "hi"})
	call(t, s, "turn/steer", map[string]any{"threadId": id, "input": "first"})
	call(t, s, "turn/steer", map[string]any{"threadId": id, "input": "second"})
	read := call(t, s, "thread/read", map[string]any{"threadId": id})
	if p := read["pending"].(map[string]any)["steers"].([]any); len(p) != 2 || p[1] != "second" {
		t.Fatalf("pending %v", read["pending"])
	}
	if read["turn"] == nil {
		t.Fatal("no turn info while busy")
	}
	call(t, s, "turn/unsteer", map[string]any{"threadId": id, "input": "second"})
	pend := func(want string) func(map[string]any) bool {
		return func(p map[string]any) bool {
			var got []string
			for _, x := range p["pending"].(map[string]any)["steers"].([]any) {
				got = append(got, x.(string))
			}
			return strings.Join(got, ",") == want
		}
	}
	_, i := n.wait(t, 0, "turn/pending", pend("first"))
	// The model stops, takes "first" and goes on.
	n.wait(t, i, "turn/pending", pend(""))
	n.wait(t, 0, "turn/completed", nil)
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "turn/unsteer", "params": map[string]any{"threadId": id, "input": "first"}})
	if resp := s.Handle(context.Background(), b); resp.Error == nil || !strings.Contains(resp.Error.Message, "no longer pending") {
		t.Fatalf("unsteer of a taken steer: %+v", resp)
	}
	var users []string
	for _, it := range call(t, s, "thread/read", map[string]any{"threadId": id})["items"].([]any) {
		if m := it.(map[string]any); m["type"] == ItemUser {
			users = append(users, m["text"].(string))
		}
	}
	if strings.Join(users, ",") != "hi,first" {
		t.Fatalf("user messages %v", users)
	}
}

// Jobs and subagents come from the session's files.
func TestJobsAndSubagents(t *testing.T) {
	work := setup(t)
	s := New("test", work)
	t.Cleanup(s.Close)
	id := call(t, s, "thread/start", map[string]any{})["threadId"].(string)

	// A job that exited, as its supervisor leaves it.
	dir := filepath.Join(jobs.Root(id), "1")
	os.MkdirAll(dir, 0o755)
	code := 2
	start := time.Now().Add(-time.Minute)
	end := start.Add(5 * time.Second)
	raw, _ := json.Marshal(jobs.Job{ID: 1, Session: id, Name: "build", Command: "make", Status: jobs.Exited, ExitCode: &code, Started: start, Ended: &end})
	os.WriteFile(filepath.Join(dir, "job.json"), raw, 0o644)
	os.WriteFile(filepath.Join(dir, "output.log"), []byte("one\ntwo\nthree\n"), 0o644)
	list := call(t, s, "job/list", map[string]any{"threadId": id})["jobs"].([]any)
	if len(list) != 1 {
		t.Fatalf("job/list %v", list)
	}
	j := list[0].(map[string]any)
	if j["label"] != "build" || j["status"] != "exited" || j["exitCode"].(float64) != 2 || j["runtimeMs"].(float64) != 5000 {
		t.Fatalf("job %v", j)
	}
	if out := call(t, s, "job/output", map[string]any{"threadId": id, "job": 1, "lines": 2})["output"]; out != "two\nthree" {
		t.Fatalf("job/output %q", out)
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "job/stop", "params": map[string]any{"threadId": id, "job": 1}})
	if resp := s.Handle(context.Background(), b); resp.Error == nil || !strings.Contains(resp.Error.Message, "not running") {
		t.Fatalf("job/stop of an exited job: %+v", resp)
	}

	// A subagent whose turn is done, with its own session.
	w := session.NewSubagent(work, id)
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "look"}})
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: "found it"}})
	w.Close()
	st := subagent.State{Name: "scout", Parent: id, Session: w.ID, Preset: "general", Model: "fake/m", Cwd: work, Task: "look", Created: time.Now(), Turns: 1, Prompt: "look"}
	if err := subagent.Create(st); err != nil {
		t.Fatal(err)
	}
	subagent.SaveTurn(id, "scout", subagent.Turn{N: 1, Status: subagent.Done, Started: start, Ended: end, PromptTokens: 50, OutputTokens: 9})
	subs := call(t, s, "subagent/list", map[string]any{"threadId": id})["subagents"].([]any)
	if len(subs) != 1 {
		t.Fatalf("subagent/list %v", subs)
	}
	// Its turn ran with no job left: it stands as recorded.
	sa := subs[0].(map[string]any)
	if sa["name"] != "scout" || sa["threadId"] != w.ID || sa["turn"].(float64) != 1 || sa["durationMs"].(float64) != 5000 || sa["outputTokens"].(float64) != 9 {
		t.Fatalf("subagent %v", sa)
	}
	r := call(t, s, "subagent/read", map[string]any{"threadId": id, "name": "scout"})
	if r["message"] != "found it" || itemTexts(r) != "look;found it;" {
		t.Fatalf("subagent/read %v", r)
	}
}

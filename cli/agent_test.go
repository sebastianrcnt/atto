package cli

import (
	"encoding/json"
	"errors"
	"io"
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
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/subagent"
)

// agentServer is a fake model; answer gives the stream for the n-th
// request (from 1) and its body.
func agentServer(t *testing.T, answer func(n int, body string) string) (bodies func() []string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, string(b))
		n := len(got)
		mu.Unlock()
		io.WriteString(w, answer(n, string(b)))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	t.Setenv(config.EnvDir, dir)
	t.Setenv(config.EnvAgent, "")
	t.Setenv(config.EnvSubagent, "")
	t.Setenv("ATTO_SESSION_ID", "")
	models := `{"providers":{"fake":{"baseUrl":"` + srv.URL + `","models":[` +
		`{"id":"m","contextWindow":10000,"efforts":["low","medium","high"]},` +
		`{"id":"small","contextWindow":10000,"efforts":["low"]}]}}}`
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(models), 0o644); err != nil {
		t.Fatal(err)
	}
	return func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), got...) }
}

func textAnswer(text string) string {
	return `data: {"choices":[{"delta":{"content":` + quoteJSON(text) + `},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":40}}}` + "\n\ndata: [DONE]\n\n"
}

func toolAnswer(command string) string {
	args := quoteJSON(`{"command":` + quoteJSON(command) + `,"description":"run it"}`)
	return `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"bash","arguments":` + args + `}}]},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n\n"
}

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func enableSubagents(t *testing.T, extra string) {
	t.Helper()
	if err := os.WriteFile(config.SettingsPath(), []byte(`{"subagents":{"enabled":true`+extra+`}}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runAgent(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out strings.Builder
	err := RunAgent(args, &out)
	return out.String(), err
}

func TestSubagentModel(t *testing.T) {
	agentServer(t, func(int, string) string { return "" })
	models, err := config.LoadModels()
	if err != nil {
		t.Fatal(err)
	}
	gen := subagent.General
	var none config.Settings
	set := config.Settings{Subagents: &config.SubagentSettings{Model: "fake/small", Effort: "low"}}
	for _, c := range []struct {
		name     string
		settings config.Settings
		preset   subagent.Preset
		pm, pe   string
		model    string
		effort   string
		err      string
	}{
		{"parent's model and effort", none, gen, "fake/m", "high", "fake/m", "high", ""},
		{"settings over the parent", set, gen, "fake/m", "high", "fake/small", "low", ""},
		{"preset over settings", set, subagent.Preset{Name: "p", Model: "fake/m", Effort: "medium"}, "fake/small", "low", "fake/m", "medium", ""},
		{"an inherited effort the model lacks gives way", none, subagent.Preset{Name: "p", Model: "fake/small"}, "fake/m", "high", "fake/small", "low", ""},
		{"a preset's unknown model", none, subagent.Preset{Name: "p", Model: "fake/nope"}, "", "", "", "", "preset p: unknown model"},
		{"a preset's effort the model lacks", none, subagent.Preset{Name: "p", Model: "fake/small", Effort: "high"}, "", "", "", "", "preset p: small has no effort"},
	} {
		ref, effort, err := subagentModel(models, c.settings, c.preset, c.pm, c.pe)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: err %v", c.name, err)
			}
			continue
		}
		if err != nil || ref.String() != c.model || effort != c.effort {
			t.Errorf("%s: %s %s %v", c.name, ref.String(), effort, err)
		}
	}
}

func TestAgentRefusals(t *testing.T) {
	agentServer(t, func(int, string) string { return "" })
	t.Chdir(t.TempDir())
	// Off by default.
	if _, err := runAgent(t, "start", "a", "general", "task", "-session", "p1"); err == nil || !strings.Contains(err.Error(), `"enabled": true`) {
		t.Fatalf("off: %v", err)
	}
	enableSubagents(t, "")
	for _, c := range []struct {
		args []string
		err  string
	}{
		{[]string{"start", "Bad_Name", "general", "x"}, "lowercase"},
		{[]string{"start", "a", "nope", "x"}, `no preset "nope" (presets: general`},
		{[]string{"start", "a", "general"}, "usage"},
		{[]string{"next", "a", "more"}, "no such subagent"},
		{[]string{"wait-any"}, "no subagent is running"},
	} {
		if _, err := runAgent(t, append(c.args, "-session", "p1")...); err == nil || !strings.Contains(err.Error(), c.err) {
			t.Errorf("%v: %v", c.args, err)
		}
	}
	// A subagent can't start or steer subagents.
	t.Setenv(config.EnvSubagent, "1")
	for _, sub := range []string{"start", "next", "steer"} {
		if _, err := runAgent(t, sub, "a", "general", "x", "-session", "p1"); err == nil || !strings.Contains(err.Error(), config.EnvSubagent) {
			t.Errorf("%s from a subagent: %v", sub, err)
		}
	}
	if err := RunAgentTurn([]string{"-session", "p1", "a", "1"}, io.Discard); err == nil {
		t.Error("a subagent ran a subagent turn")
	}
	if out, err := runAgent(t, "list", "-session", "p1"); err != nil || out != "no subagents\n" {
		t.Errorf("list from a subagent: %q %v", out, err)
	}
}

// A subagent turn takes the parent's messages from its inbox at step
// boundaries, and one that arrives as the turn ends gets a turn of its own.
func TestSubagentTurnDeliversSteers(t *testing.T) {
	var id string
	push := func(text string) {
		_ = events.Push(id, events.Event{Source: "parent", Text: "Message from the parent agent: " + text})
	}
	bodies := agentServer(t, func(n int, body string) string {
		switch n {
		case 1:
			push("also check the tests")
			return toolAnswer("true")
		case 2:
			push("one more thing")
			return textAnswer("first done")
		}
		return textAnswer("all done")
	})
	cwd := t.TempDir()
	t.Chdir(cwd)
	w := session.NewSubagent(cwd, "p1")
	w.Append(session.Entry{Type: session.TypeName, Name: "a"})
	w.Close()
	id = w.ID
	push("before you start")
	quiet(t)

	var res printResult
	err := RunPrint(PrintOptions{Prompt: "the task", Resume: id, Format: "text", Subagent: &agent.Subagent{Name: "a", Preset: "general"}, done: func(r printResult) { res = r }})
	if err != nil {
		t.Fatal(err)
	}
	b := bodies()
	if len(b) != 3 {
		t.Fatalf("%d requests", len(b))
	}
	if !strings.Contains(b[0], "the task") || !strings.Contains(b[0], "before you start") || !strings.Contains(b[0], `You are subagent \"a\"`) {
		t.Fatalf("first request: %s", b[0])
	}
	if !strings.Contains(b[1], "[atto event] Message from the parent agent: also check the tests") {
		t.Fatalf("the steer reaches the running turn: %s", b[1])
	}
	if !strings.Contains(b[2], "one more thing") {
		t.Fatalf("a late message gets a turn: %s", b[2])
	}
	if res.Result != "all done" || res.Usage.OutputTokens != 14 || res.Usage.InputTokens != 200 {
		t.Fatalf("result %+v", res)
	}
}

func TestAgentStartWaitReport(t *testing.T) {
	bodies := agentServer(t, func(n int, body string) string {
		if n == 1 {
			return textAnswer("found 3 bugs")
		}
		return textAnswer("fixed them")
	})
	cwd := t.TempDir()
	t.Chdir(cwd)
	enableSubagents(t, `,"maxConcurrent":1`)
	if err := os.MkdirAll(filepath.Join(cwd, ".atto", "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".atto", "agents", "hunter.md"), []byte("---\ndescription: finds bugs\nmodel: fake/small\n---\nHunt bugs."), 0o644); err != nil {
		t.Fatal(err)
	}
	const parent = "p1"

	out, err := runAgent(t, "start", "bugs", "hunter", "find", "bugs", "-session", parent)
	if err != nil || !strings.Contains(out, "subagent bugs started") || !strings.Contains(out, "fake/small · low") {
		t.Fatalf("start: %q %v", out, err)
	}
	if _, err := runAgent(t, "start", "bugs", "general", "again", "-session", parent); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatalf("a second start: %v", err)
	}
	out, err = runAgent(t, "wait", "bugs", "-timeout", "30s", "-session", parent)
	if err != nil || !strings.Contains(out, "subagent bugs · turn 1 done") || !strings.Contains(out, "found 3 bugs") ||
		!strings.Contains(out, "tokens 100 in (40 cached), 7 out") {
		t.Fatalf("wait: %q %v", out, err)
	}
	b := bodies()
	if len(b) != 1 || !strings.Contains(b[0], "Hunt bugs.") || !strings.Contains(b[0], "find bugs") || !strings.Contains(b[0], `"model":"small"`) {
		t.Fatalf("request %v", b)
	}
	// The parent hears about it.
	evs := events.Drain(parent)
	if len(evs) != 1 || evs[0].Source != "agent" || !strings.Contains(evs[0].Text, "Subagent bugs finished turn 1") ||
		!strings.Contains(evs[0].Text, "atto agent report bugs") {
		t.Fatalf("events %+v", evs)
	}
	// Idle: steer is refused, next runs another turn in the same session.
	if _, err := runAgent(t, "steer", "bugs", "hurry", "-session", parent); err == nil || !strings.Contains(err.Error(), "atto agent next") {
		t.Fatalf("steer while idle: %v", err)
	}
	if _, err := runAgent(t, "next", "bugs", "fix", "them", "-session", parent); err != nil {
		t.Fatal(err)
	}
	out, err = runAgent(t, "wait-any", "-timeout", "30s", "-session", parent)
	if err != nil || !strings.Contains(out, "turn 2 done") || !strings.Contains(out, "fixed them") {
		t.Fatalf("wait-any: %q %v", out, err)
	}
	if b = bodies(); len(b) != 2 || !strings.Contains(b[1], "found 3 bugs") {
		t.Fatalf("the second turn continues the session: %v", b)
	}
	out, _ = runAgent(t, "list", "-session", parent)
	if !strings.Contains(out, "bugs") || !strings.Contains(out, "hunter") || !strings.Contains(out, "done") || !strings.Contains(out, "find bugs") {
		t.Fatalf("list %q", out)
	}
	if l, _ := session.List("", false); len(l) != 0 {
		t.Fatalf("the subagent's session is listed: %+v", l)
	}
	out, err = runAgent(t, "report", "bugs", "-session", parent)
	if err != nil || !strings.Contains(out, "fixed them") {
		t.Fatalf("report %q %v", out, err)
	}
}

func TestAgentWaitTimeout(t *testing.T) {
	release := make(chan struct{})
	agentServer(t, func(int, string) string {
		<-release
		return textAnswer("late")
	})
	defer close(release)
	t.Chdir(t.TempDir())
	enableSubagents(t, "")
	if _, err := runAgent(t, "start", "slow", "general", "x", "-session", "p1"); err != nil {
		t.Fatal(err)
	}
	out, err := runAgent(t, "wait", "slow", "-timeout", "300ms", "-session", "p1")
	var code ExitCode
	if !errors.As(err, &code) || code != 124 || !strings.Contains(out, "still running") {
		t.Fatalf("wait: %q %v", out, err)
	}
	if out, _ := runAgent(t, "list", "-session", "p1"); !strings.Contains(out, "running") {
		t.Fatalf("list %q", out)
	}
	out, err = runAgent(t, "stop", "slow", "-session", "p1")
	if err != nil || !strings.Contains(out, "stopped") {
		t.Fatalf("stop: %q %v", out, err)
	}
	st, _ := subagent.Load("p1", "slow")
	if s := st.Latest().Status; s != subagent.Stopped {
		t.Fatalf("status %s", s)
	}
}

func TestAgentQueueBeyondLimit(t *testing.T) {
	release := make(chan struct{})
	agentServer(t, func(int, string) string {
		<-release
		return textAnswer("ok")
	})
	t.Chdir(t.TempDir())
	enableSubagents(t, `,"maxConcurrent":1`)
	for _, n := range []string{"one", "two"} {
		if _, err := runAgent(t, "start", n, "general", "x", "-session", "p1"); err != nil {
			t.Fatal(err)
		}
	}
	// The second waits for the first's slot.
	var out string
	for range 100 {
		out, _ = runAgent(t, "list", "-session", "p1")
		if strings.Contains(out, "running") && strings.Contains(out, "queued") {
			break
		}
		sleepBriefly()
	}
	if !strings.Contains(out, "running") || !strings.Contains(out, "queued") {
		t.Fatalf("list %q", out)
	}
	close(release)
	for _, n := range []string{"one", "two"} {
		if out, err := runAgent(t, "wait", n, "-timeout", "30s", "-session", "p1"); err != nil || !strings.Contains(out, "done") {
			t.Fatalf("%s: %q %v", n, out, err)
		}
	}
}

func sleepBriefly() { time.Sleep(50 * time.Millisecond) }

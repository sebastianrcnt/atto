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
	"github.com/sebastianrcnt/atto/tui"
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
		{"a preset's unknown model", none, subagent.Preset{Name: "p", Model: "fake/nope"}, "", "", "", "", "role p: unknown model"},
		{"a preset's effort the model lacks", none, subagent.Preset{Name: "p", Model: "fake/small", Effort: "high"}, "", "", "", "", "role p: small has no effort"},
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
		{[]string{"spawn", "Bad_Name", "x"}, "lowercase"},
		{[]string{"spawn", "a", "x", "-role", "nope"}, `no preset "nope" (presets: general`},
		{[]string{"spawn", "a"}, "give the agent its task"},
		{[]string{"task", "a", "more"}, "no such subagent"},
		{[]string{"send", "/root/zz", "hi"}, "no such subagent"},
		{[]string{"send", "..", "hi"}, "no parent agent"},
		{[]string{"wait"}, "no subagent is running"},
		{[]string{"task", "/root", "x"}, "not an agent anyone started"},
	} {
		if _, err := runAgent(t, append(c.args, "-session", "p1")...); err == nil || !strings.Contains(err.Error(), c.err) {
			t.Errorf("%v: %v", c.args, err)
		}
	}
	// Agents nest only as deep as the settings allow: by default an agent
	// (here p1's agent "a") may not start agents of its own.
	if _, err := runAgent(t, "spawn", "a", "the task", "-session", "p1"); err != nil {
		t.Fatal(err)
	}
	a, _ := subagent.Load("p1", "a")
	if _, err := runAgent(t, "spawn", "b", "deeper", "-session", a.Session); err == nil || !strings.Contains(err.Error(), "maxDepth") {
		t.Fatalf("spawn below the depth: %v", err)
	}
	enableSubagents(t, `,"maxDepth":2`)
	if out, err := runAgent(t, "spawn", "b", "deeper", "-session", a.Session); err != nil || !strings.Contains(out, "agent /root/a/b started") {
		t.Fatalf("nested spawn: %q %v", out, err)
	}
	// Seen from the nested agent: its parent, the root and itself.
	b, _ := subagent.Load(a.Session, "b")
	if out, err := runAgent(t, "send", "..", "found it", "-session", b.Session); err != nil || !strings.Contains(out, "sent to /root/a") {
		t.Fatalf("send to the parent: %q %v", out, err)
	}
	if out, err := runAgent(t, "send", "/root", "hello root", "-session", b.Session); err != nil || !strings.Contains(out, "sent to /root:") {
		t.Fatalf("send to the root: %q %v", out, err)
	}
	if evs := events.Drain("p1"); len(evs) == 0 || !strings.Contains(evs[len(evs)-1].Text, "From: /root/a/b\nTo: /root\n") || !evs[len(evs)-1].Quiet {
		t.Fatalf("root inbox %+v", evs)
	}
	if out, _ := runAgent(t, "list", "-session", "p1"); !strings.Contains(out, "/root/a ") || !strings.Contains(out, "/root/a/b") {
		t.Fatalf("list shows the tree: %q", out)
	}
	if _, err := runAgent(t, "send", "b", "x", "-session", b.Session); err == nil {
		t.Fatal("an agent sent to a child it doesn't have")
	}
	if err := RunAgentTurn([]string{"-session", "p1", "zz", "1"}, io.Discard); err == nil {
		t.Error("ran a turn of an agent that doesn't exist")
	}
	// Let both turns end before the temp dirs go.
	_, _ = runAgent(t, "wait", "b", "-timeout", "30s", "-session", a.Session)
	_, _ = runAgent(t, "wait", "a", "-timeout", "30s", "-session", "p1")
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
	if !strings.Contains(b[0], "the task") || !strings.Contains(b[0], "before you start") || !strings.Contains(b[0], `You are agent `) {
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
	if err != nil || !strings.Contains(out, "agent /root/bugs started") || !strings.Contains(out, "fake/small · low") {
		t.Fatalf("start: %q %v", out, err)
	}
	if _, err := runAgent(t, "start", "bugs", "general", "again", "-session", parent); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatalf("a second start: %v", err)
	}
	out, err = runAgent(t, "wait", "bugs", "-timeout", "30s", "-session", parent)
	if err != nil || !strings.Contains(out, "agent /root/bugs · turn 1 done") || !strings.Contains(out, "found 3 bugs") ||
		!strings.Contains(out, "tokens 100 in (40 cached), 7 out") {
		t.Fatalf("wait: %q %v", out, err)
	}
	b := bodies()
	if len(b) != 1 || !strings.Contains(b[0], "Hunt bugs.") || !strings.Contains(b[0], "find bugs") || !strings.Contains(b[0], `"model":"small"`) {
		t.Fatalf("request %v", b)
	}
	// The parent hears about it: its final answer, which the wait took
	// from the inbox as it printed the report.
	if evs := events.Drain(parent); len(evs) != 0 {
		t.Fatalf("the final answer waits in the inbox after wait printed it: %+v", evs)
	}
	st1, _ := subagent.Load(parent, "bugs")
	ev := turnEvent(st1, st1.Latest())
	if ev.Source != "agent" || !strings.Contains(ev.Text, "Message Type: FINAL_ANSWER\nFrom: /root/bugs\nTo: /root") ||
		!strings.Contains(ev.Text, "Turn 1 finished") || !strings.Contains(ev.Text, "found 3 bugs") || ev.Quiet {
		t.Fatalf("event %+v", ev)
	}
	// Idle: a message waits quietly for its next turn; a task runs one in
	// the same session, and the message goes with it.
	if out, err := runAgent(t, "send", "bugs", "use", "go", "vet", "-session", parent); err != nil || !strings.Contains(out, "with its next turn") {
		t.Fatalf("send while idle: %q %v", out, err)
	}
	if evs := events.Drain(st0(t, parent).Session); len(evs) != 1 || !evs[0].Quiet || !strings.Contains(evs[0].Text, "Message Type: MESSAGE\nFrom: /root\nTo: /root/bugs") {
		t.Fatalf("message %+v", evs)
	} else {
		events.Requeue(st0(t, parent).Session, evs)
	}
	if _, err := runAgent(t, "task", "bugs", "fix", "them", "-session", parent); err != nil {
		t.Fatal(err)
	}
	out, err = runAgent(t, "wait-any", "-timeout", "30s", "-session", parent)
	if err != nil || !strings.Contains(out, "turn 2 done") || !strings.Contains(out, "fixed them") {
		t.Fatalf("wait-any: %q %v", out, err)
	}
	if b = bodies(); len(b) != 2 || !strings.Contains(b[1], "found 3 bugs") || !strings.Contains(b[1], "Message Type: NEW_TASK") || !strings.Contains(b[1], "use go vet") {
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
	// Removed: gone from the list, its session archived, its name free.
	st, _ := subagent.Load(parent, "bugs")
	out, err = runAgent(t, "close", "bugs", "-session", parent)
	if err != nil || !strings.Contains(out, "closed agent /root/bugs") {
		t.Fatalf("rm: %q %v", out, err)
	}
	if l := subagent.List(parent); len(l) != 0 {
		t.Fatalf("listed after rm: %+v", l)
	}
	if p, err := session.Find(st.Session); err != nil || !strings.HasPrefix(p, config.ArchivedDir()) {
		t.Fatalf("the session is not archived: %s %v", p, err)
	}
	if _, err := runAgent(t, "rm", "bugs", "-session", parent); err == nil {
		t.Fatal("rm of a removed subagent")
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
	if _, err := runAgent(t, "rm", "slow", "-session", "p1"); err == nil || !strings.Contains(err.Error(), "interrupt it first") {
		t.Fatalf("rm while running: %v", err)
	}
	if out, _ := runAgent(t, "rm", "-done", "-session", "p1"); !strings.Contains(out, "no finished agents") {
		t.Fatalf("rm -done while running: %q", out)
	}
	out, err = runAgent(t, "stop", "slow", "-session", "p1")
	if err != nil || !strings.Contains(out, "interrupted") {
		t.Fatalf("stop: %q %v", out, err)
	}
	st, _ := subagent.Load("p1", "slow")
	if s := st.Latest().Status; s != subagent.Stopped {
		t.Fatalf("status %s", s)
	}
	if out, err := runAgent(t, "rm", "-done", "-session", "p1"); err != nil || !strings.Contains(out, "closed agent /root/slow") || len(subagent.List("p1")) != 0 {
		t.Fatalf("rm -done: %q %v", out, err)
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

func TestAgentExternalParent(t *testing.T) {
	bodies := agentServer(t, func(int, string) string { return textAnswer("done") })
	root := t.TempDir()
	t.Chdir(root)
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	enableSubagents(t, "")
	out, err := runAgent(t, "list")
	if err != nil || !strings.HasPrefix(out, "external parent created: ") {
		t.Fatalf("create: %q %v", out, err)
	}
	path, _, _ := externalParentPath()
	b, _ := os.ReadFile(path)
	parent := strings.TrimSpace(string(b))
	hpath, _ := session.Find(parent)
	h, entries, err := session.Load(hpath)
	if err != nil || !h.External || len(entries) != 1 || entries[0].Name != "atto agent (external)" || len(bodies()) != 0 {
		t.Fatalf("parent: %+v %+v %v", h, entries, err)
	}
	if _, ok := session.Latest(""); ok {
		t.Fatal("external parent selected by continue")
	}
	list, _ := session.List("", false)
	if len(list) != 1 || list[0].Name != "atto agent (external)" {
		t.Fatalf("list: %+v", list)
	}
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(child)
	if out, err := runAgent(t, "list"); err != nil || out != "no agents\n" {
		t.Fatalf("reuse: %q %v", out, err)
	}
	if out, err := runAgent(t, "list", "-session", "explicit"); err != nil || out != "no agents\n" {
		t.Fatalf("explicit: %q %v", out, err)
	}
	if _, err := runAgent(t, "start", "a", "general", "task", "-m", "fake/m", "-effort", "high"); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{"wait", "wait-any", "report"} {
		args := []string{cmd, "a", "-json", "-timeout", "30s"}
		out, err := runAgent(t, args...)
		var r struct {
			Name, Status, Session, Model, Message string
			Turn                                  int
			Duration                              float64
			Tokens                                struct{ In, Cached, Out int }
		}
		if err != nil || json.Unmarshal([]byte(out), &r) != nil || r.Name != "a" || r.Status != "done" || r.Turn != 1 || r.Session == "" || r.Model != "fake/m" || r.Message != "done" || r.Duration <= 0 || r.Tokens.In != 100 || r.Tokens.Cached != 40 || r.Tokens.Out != 7 {
			t.Fatalf("%s: %q %v", cmd, out, err)
		}
	}
	st, _ := subagent.Load(parent, "a")
	if st.Effort != "high" {
		t.Fatalf("effort: %s", st.Effort)
	}
	if _, err := runAgent(t, "rm", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("mapping retained: %v", err)
	}
	if p, err := session.Find(parent); err != nil || !isArchived(p) {
		t.Fatalf("parent not archived: %s %v", p, err)
	}
	if out, err := runAgent(t, "list"); err != nil || !strings.Contains(out, "external parent created:") || strings.Contains(out, parent) {
		t.Fatalf("new parent: %q %v", out, err)
	}
}

func TestAgentExternalModelFlags(t *testing.T) {
	agentServer(t, func(int, string) string { return textAnswer("ok") })
	t.Chdir(t.TempDir())
	enableSubagents(t, "")
	for _, env := range []string{"ATTO_SESSION_ID", config.EnvAgent, config.EnvSubagent} {
		t.Setenv(env, "inside")
		for _, flags := range [][]string{{"-m", "fake/m"}, {"-effort", "high"}, {"-m", ""}} {
			args := append([]string{"start", "a", "general", "task", "-session", "explicit"}, flags...)
			if _, err := runAgent(t, args...); err == nil || !strings.Contains(err.Error(), "external callers only") {
				t.Fatalf("%s %v: %v", env, flags, err)
			}
		}
		t.Setenv(env, "")
	}
	for _, flags := range [][]string{{"-m", "fake/nope"}, {"-m", "fake/small", "-effort", "high"}} {
		if _, err := runAgent(t, append([]string{"start", "a", "general", "task", "-session", "explicit"}, flags...)...); err == nil {
			t.Fatalf("accepted invalid flags %v", flags)
		}
	}
	if _, err := runAgent(t, "start", "a", "general", "task", "-session", "explicit", "-m", "fake/small", "-effort", "low"); err != nil {
		t.Fatal(err)
	}
	if _, err := runAgent(t, "wait", "a", "-session", "explicit", "-timeout", "30s"); err != nil {
		t.Fatal(err)
	}
	st, _ := subagent.Load("explicit", "a")
	if st.Model != "fake/small" || st.Effort != "low" {
		t.Fatalf("state: %+v", st)
	}
}

func TestAgentListTaskSummary(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	for _, c := range []struct {
		name, task, want string
	}{
		{"short", "first line\nsecond line", "first line …"},
		{"long", strings.Repeat("a", 80) + "\nhidden", strings.Repeat("a", 59) + "…"},
		{"unicode", strings.Repeat("é", 80), strings.Repeat("é", 59) + "…"},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := subagent.State{Name: c.name, Parent: c.name, Session: "s1", Task: c.task}
			if err := subagent.Create(st); err != nil {
				t.Fatal(err)
			}
			var out strings.Builder
			if err := agentList(&out, c.name); err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(tui.StripEscapes(out.String())), "\n")
			if len(lines) != 2 || !strings.HasSuffix(lines[1], "  "+c.want) {
				t.Fatalf("list: %q, want task %q", out.String(), c.want)
			}
		})
	}
}

func st0(t *testing.T, parent string) subagent.State {
	t.Helper()
	l := subagent.List(parent)
	if len(l) == 0 {
		t.Fatal("no agent")
	}
	return l[0]
}

// A quiet message (atto agent send) that arrives as an agent's turn ends
// waits in its inbox for its next turn instead of starting one.
func TestAgentTurnLeavesQuietMessages(t *testing.T) {
	var id string
	bodies := agentServer(t, func(n int, body string) string {
		if n == 1 {
			_ = events.Push(id, events.Event{Source: "agent", Quiet: true, Text: subagent.Envelope(subagent.Message, "/root", "/root/a", "fyi")})
		}
		return textAnswer("done")
	})
	cwd := t.TempDir()
	t.Chdir(cwd)
	w := session.NewSubagent(cwd, "p1")
	w.Append(session.Entry{Type: session.TypeName, Name: "a"})
	w.Close()
	id = w.ID
	quiet(t)
	if err := RunPrint(PrintOptions{Prompt: "the task", Resume: id, Format: "text", Subagent: &agent.Subagent{Name: "a", Preset: "general"}}); err != nil {
		t.Fatal(err)
	}
	if b := bodies(); len(b) != 1 {
		t.Fatalf("%d requests: a quiet message started a turn", len(b))
	}
	if evs := events.Drain(id); len(evs) != 1 || !evs[0].Quiet || !strings.Contains(evs[0].Text, "fyi") {
		t.Fatalf("inbox %+v", evs)
	}
}

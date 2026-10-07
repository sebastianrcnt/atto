package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/hooks/hooktest"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/provider/providertest"
	"github.com/sebastianrcnt/atto/session"
)

// harnessOn is newHarness on a thread that method (thread/start or
// thread/resume) opens with params: for tests that write settings,
// extensions or sessions first.
func harnessOn(t *testing.T, s *Server, m *providertest.Model, method string, params map[string]any) *harness {
	t.Helper()
	h := &harness{t: t, s: s, m: m, ev: make(chan Notification, 10000)}
	h.c = h.connect()
	var th ThreadInfo
	if err := h.c.Call(context.Background(), method, params, &th); err != nil {
		t.Fatal(err)
	}
	h.id = th.ID
	return h
}

// noticeWith waits for a notice whose text or title contains s.
func (h *harness) noticeWith(s string) map[string]any {
	h.t.Helper()
	return itemOf(h.wait("item/completed", func(p map[string]any) bool {
		it := itemOf(p)
		return it["type"] == ItemNotice && strings.Contains(fmt.Sprint(it["title"], "\n", it["text"]), s)
	}))
}

// savedSession writes a session in cwd of the messages, closed.
func savedSession(t *testing.T, cwd string, msgs ...provider.Message) *session.Writer {
	t.Helper()
	w := session.New(cwd)
	for _, m := range msgs {
		w.Append(session.Entry{Type: session.TypeMessage, Message: &m})
	}
	w.Close()
	return w
}

func entryWith(t *testing.T, path, content string) string {
	t.Helper()
	_, entries, err := session.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Message != nil && e.Message.Content == content {
			return e.ID
		}
	}
	t.Fatalf("no entry %q", content)
	return ""
}

func msgs(pairs ...string) []provider.Message {
	var out []provider.Message
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, provider.Message{Role: pairs[i], Content: pairs[i+1]})
	}
	return out
}

// Going back to a user message moves the branch, gives its text to the
// client that asked, and a fresh runtime resumes on the new branch; the
// old one stays in the file. A non-user entry becomes the leaf.
func TestNavigateMovesTheBranch(t *testing.T) {
	s, m := testServer(t, providertest.Reply{Text: "ok"})
	w := savedSession(t, s.Cwd, msgs("user", "u1", "assistant", "a1", "user", "u2", "assistant", "a2")...)
	h := harnessOn(t, s, m, "thread/resume", map[string]any{"threadId": w.ID})
	u2 := entryWith(t, w.Path, "u2")

	h.call("thread/navigate", map[string]any{"entryId": u2})
	h.wait("thread/branchChanged", nil)
	if rec := h.wait("input/recovered", nil); rec["text"] != "u2" || rec["ifEmpty"] != true {
		t.Fatalf("recovered %v", rec)
	}
	if got := itemTexts(h.call("thread/read", nil)); got != "u1;a1;" {
		t.Fatalf("after going back %q", got)
	}

	h.call("thread/navigate", map[string]any{"entryId": entryWith(t, w.Path, "a2")})
	h.wait("thread/branchChanged", nil)
	if got := itemTexts(h.call("thread/read", nil)); got != "u1;a1;u2;a2;" {
		t.Fatalf("a non-user entry is the leaf: %q", got)
	}
	h.call("thread/navigate", map[string]any{"entryId": u2})
	h.wait("thread/branchChanged", nil)

	// Labels are entries of the session.
	h.call("thread/setLabel", map[string]any{"entryId": u2, "label": "ok"})
	h.call("thread/close", nil)

	s2 := New("test", s.Cwd)
	t.Cleanup(s2.Close)
	if got := itemTexts(call(t, s2, "thread/resume", map[string]any{"threadId": w.ID})); got != "u1;a1;" {
		t.Fatalf("resumed %q", got)
	}
	_, entries, _ := session.Load(w.Path)
	var off []string
	for _, it := range session.Items(entries) {
		if it.OffBranch {
			off = append(off, it.Text)
		}
	}
	if strings.Join(off, ",") != "u2,a2" {
		t.Fatalf("off-branch %v", off)
	}
	var label session.Entry
	for _, e := range entries {
		if e.Type == session.TypeLabel {
			label = e
		}
	}
	if label.Label != "ok" || label.TargetID != u2 {
		t.Fatalf("label %+v", label)
	}
}

// Going back to a message with images gives the images back too.
func TestNavigateRecoversImages(t *testing.T) {
	s, m := testServer(t, providertest.Reply{Text: "ok"})
	var b bytes.Buffer
	png.Encode(&b, image.NewGray(image.Rect(0, 0, 3, 2)))
	file := filepath.Join(t.TempDir(), "x.png")
	os.WriteFile(file, b.Bytes(), 0o644)
	im, err := images.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := images.Save(im); err != nil {
		t.Fatal(err)
	}
	w := session.New(s.Cwd)
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "look [image 1: 3x2 PNG]", Images: []provider.Image{im}}})
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: "a1"}})
	w.Close()
	h := harnessOn(t, s, m, "thread/resume", map[string]any{"threadId": w.ID})
	h.call("thread/navigate", map[string]any{"entryId": entryWith(t, w.Path, "look [image 1: 3x2 PNG]")})
	rec := h.wait("input/recovered", nil)
	imgs, _ := rec["images"].([]any)
	if rec["text"] != "look [image 1: 3x2 PNG]" || len(imgs) != 1 || imgs[0].(map[string]any)["file"] != im.File {
		t.Fatalf("recovered %v", rec)
	}
}

// Navigating while a turn runs interrupts it and moves once it has
// stopped; its pending steer goes back to its client.
func TestNavigateWhileBusyWaitsForTurn(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	s, m := testServer(t, providertest.Reply{Text: "never", Gate: gate})
	w := savedSession(t, s.Cwd, msgs("user", "u1", "assistant", "a1")...)
	h := harnessOn(t, s, m, "thread/resume", map[string]any{"threadId": w.ID})
	h.call("input/submit", map[string]any{"input": "go on"})
	h.m.Started(5 * time.Second)
	if r := h.call("input/submit", map[string]any{"input": "steer"}); r["status"] != StatusSteered {
		t.Fatalf("steer %v", r)
	}
	h.call("thread/navigate", map[string]any{"entryId": entryWith(t, w.Path, "u1")})
	// The steer comes back at once, before the turn has stopped.
	h.wait("input/recovered", func(p map[string]any) bool { return p["text"] == "steer" })
	if c := h.completed(); c["status"] != "interrupted" {
		t.Fatalf("turn %v", c)
	}
	h.wait("thread/branchChanged", nil)
	if r := h.call("thread/read", nil); itemTexts(r) != "" || r["busy"] != false {
		t.Fatalf("after the move: %q busy %v", itemTexts(r), r["busy"])
	}
}

// A new objective over an unfinished goal asks first (a prompt every
// client sees); /goal shows the goal.
func TestGoalReplaceAsksFirst(t *testing.T) {
	h := newHarness(t, providertest.Reply{Text: "working", Words: 2, Delay: 30 * time.Millisecond})
	h.call("goal/set", map[string]any{"input": "ship the feature"})
	h.wait("item/started", func(p map[string]any) bool { return itemOf(p)["type"] == ItemGoal })
	h.call("goal/pause", nil)
	h.completed()

	h.call("input/submit", map[string]any{"input": "/goal write the docs"})
	open := h.wait("prompt/open", nil)["prompt"].(map[string]any)
	if open["title"] != "Replace goal?" || !strings.Contains(fmt.Sprint(open["subtitle"]), "write the docs") || open["origin"] != "goal" {
		t.Fatalf("prompt %v", open)
	}
	if g := h.call("goal/read", nil)["goal"].(map[string]any); g["objective"] != "ship the feature" {
		t.Fatalf("replaced before the answer: %v", g)
	}
	h.call("prompt/answer", map[string]any{"id": open["id"], "index": 0})
	h.wait("item/started", func(p map[string]any) bool { return itemOf(p)["type"] == ItemGoal })
	if g := h.call("goal/read", nil)["goal"].(map[string]any); g["objective"] != "write the docs" {
		t.Fatalf("goal %v", g)
	}
	h.call("goal/pause", nil)
	h.completed()
	h.call("input/submit", map[string]any{"input": "/goal"})
	h.noticeWith("write the docs")
}

// An extension's dialogs are prompts the client answers; its notify
// reaches the client.
func TestExtensionDialogsArePrompts(t *testing.T) {
	s, m := testServer(t, providertest.Reply{Text: "ok"})
	dir := os.Getenv("ATTO_DIR")
	os.MkdirAll(filepath.Join(dir, "extensions"), 0o755)
	os.WriteFile(filepath.Join(dir, "extensions", "demo.ts"), []byte(`export default function (atto: any) {
  atto.registerCommand("demo", {
    description: "Demo things",
    handler: async (args: string, ctx: any) => {
      const pick = await ctx.ui.select("Pick one", ["red", "green"]);
      const ok = await ctx.ui.confirm("Sure?");
      const name = await ctx.ui.input("Name?");
      ctx.ui.notify("picked " + pick + " " + ok + " " + name + " " + args + " " + ctx.hasUI, "warning");
    },
  });
}`), 0o644)
	h := harnessOn(t, s, m, "thread/start", map[string]any{})
	if r := h.call("input/submit", map[string]any{"input": "/demo now"}); r["status"] != StatusDone {
		t.Fatalf("submit %v", r)
	}
	answer := func(title string, ans map[string]any) {
		t.Helper()
		p := h.wait("prompt/open", nil)["prompt"].(map[string]any)
		if p["title"] != title || p["origin"] != "extension" {
			t.Fatalf("prompt %v, want %q", p, title)
		}
		ans["id"] = p["id"]
		h.call("prompt/answer", ans)
	}
	answer("Pick one", map[string]any{"index": 1})
	answer("Sure?", map[string]any{"index": 0})
	answer("Name?", map[string]any{"text": "bob"})
	n := h.wait("extension/notify", nil)
	if n["extension"] != "demo" || n["message"] != "picked green true bob now true" || n["level"] != "warning" {
		t.Fatalf("notify %v", n)
	}
	if n := len(h.m.Requests()); n != 0 {
		t.Fatalf("a command reached the model: %d requests", n)
	}
}

// Leaving a session for a new one (/clear) runs SessionEnd with the
// client's reason.
func TestDetachReasonReachesSessionEnd(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("hook commands are bash here")
	}
	s, m := testServer(t, providertest.Reply{Text: "ok"})
	log := filepath.Join(t.TempDir(), "end.log")
	settings, _ := json.Marshal(map[string]any{"hooks": map[string]any{"SessionEnd": []any{map[string]any{"hooks": []any{
		map[string]any{"type": "command", "command": hooktest.LogStdin(log)}}}}}})
	os.WriteFile(filepath.Join(os.Getenv("ATTO_DIR"), "settings.json"), settings, 0o644)
	s.Retire = true
	h := harnessOn(t, s, m, "thread/start", map[string]any{})
	h.call("input/submit", map[string]any{"input": "hello"})
	h.completed()
	if r := h.call("thread/detach", map[string]any{"reason": "clear"}); r["closed"] != true {
		t.Fatalf("detach %v", r)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), `"reason":"clear"`) || !strings.Contains(string(b), h.id) {
		t.Fatalf("SessionEnd: %s", b)
	}
}

// "Run in background" stops the turn, hands the session to a background
// run and closes the thread without ending it.
func TestHandoffSpawnsBackgroundRun(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	h := newHarness(t, providertest.Reply{Text: "never", Gate: gate})
	spawned := make(chan string, 1)
	old := Spawn
	Spawn = func(id, path, cwd string) (string, error) { spawned <- id; return "/tmp/x.log", nil }
	t.Cleanup(func() { Spawn = old })
	h.call("input/submit", map[string]any{"input": "long task"})
	h.m.Started(5 * time.Second)
	h.call("thread/handoff", nil)
	select {
	case id := <-spawned:
		if id != h.id {
			t.Fatalf("spawned %s", id)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no background run")
	}
	if p := h.wait("thread/handedOff", nil); !strings.Contains(fmt.Sprint(p["line"]), "Running in background") {
		t.Fatalf("handedOff %v", p)
	}
	h.wait("thread/closed", func(p map[string]any) bool { return p["handoff"] == true })
	if h.s.Loaded(h.id) {
		t.Fatal("the thread stayed loaded")
	}
}

// remote/start serves the session to a browser: its input reaches the
// same runtime, marked as the web client's; remote/stop ends it.
func TestRemoteGatewayServesTheSession(t *testing.T) {
	h := newHarness(t, providertest.Reply{Text: "hi web"})
	ln, err := net.Listen("tcp", "127.0.0.1:0") // a free port: 0 means remote.port or 7879
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	g := h.call("remote/start", map[string]any{"host": "127.0.0.1", "port": port})
	addr, token := g["addr"].(string), g["token"].(string)
	post := func(method string, params map[string]any) (map[string]any, error) {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		req, _ := http.NewRequest("POST", "http://"+addr+"/rpc", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		var r struct {
			Result map[string]any `json:"result"`
			Error  *struct{ Message string }
		}
		json.NewDecoder(resp.Body).Decode(&r)
		if r.Error != nil {
			return nil, fmt.Errorf("%s", r.Error.Message)
		}
		return r.Result, nil
	}
	if r, err := post("initialize", nil); err != nil || r["live"] != true || r["threadId"] != h.id {
		t.Fatalf("initialize %v %v", r, err)
	}
	if _, err := post("turn/start", map[string]any{"input": "hello from the phone"}); err != nil {
		t.Fatal(err)
	}
	h.wait("item/started", func(p map[string]any) bool {
		it := itemOf(p)
		return it["type"] == ItemUser && it["text"] == "hello from the phone" && it["clientId"] == "remote"
	})
	h.completed()
	if !strings.Contains(itemTexts(h.call("thread/read", nil)), "hi web") {
		t.Fatal("no answer")
	}
	h.call("remote/stop", nil)
	if _, err := post("initialize", nil); err == nil {
		t.Fatal("the gateway still answers")
	}
}

// Without any model a thread starts anyway; input goes back to the
// client with a hint.
func TestInputWithoutModelsIsGivenBack(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ATTO_DIR", dir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENCODE_API_KEY", "")
	s := New("test", t.TempDir())
	t.Cleanup(s.Close)
	h := harnessOn(t, s, nil, "thread/start", map[string]any{})
	h.call("input/submit", map[string]any{"input": "hello there"})
	if rec := h.wait("input/recovered", nil); rec["text"] != "hello there" {
		t.Fatalf("recovered %v", rec)
	}
	h.noticeWith("No model is set up yet")
	if r := h.call("thread/read", nil); r["busy"] != false {
		t.Fatalf("a turn started: %v", r)
	}
}

// thread/reload (/reload) reloads at once and reports what changed in a
// loaded notice; the model is not told.
func TestReloadMethodReportsChanges(t *testing.T) {
	s, m := testServer(t, providertest.Reply{Text: "ok"})
	os.WriteFile(filepath.Join(s.Cwd, "AGENTS.md"), []byte("Project rules."), 0o644)
	h := harnessOn(t, s, m, "thread/start", map[string]any{})
	h.call("thread/reload", nil)
	it := itemOf(h.wait("item/completed", func(p map[string]any) bool { return itemOf(p)["reloaded"] == true }))
	if it["level"] != "loaded" || it["changes"] != nil || !strings.Contains(fmt.Sprint(it["note"]), "unchanged") {
		t.Fatalf("unchanged reload %v", it)
	}
	os.WriteFile(filepath.Join(s.Cwd, "AGENTS.md"), []byte("Project rules, revised."), 0o644)
	h.call("thread/reload", nil)
	r := h.wait("thread/reloaded", func(p map[string]any) bool { return p["promptChanged"] == true })
	if !strings.Contains(fmt.Sprint(r["changes"]), "AGENTS") {
		t.Fatalf("reloaded %v", r)
	}
	time.Sleep(700 * time.Millisecond) // an inbox tick
	if n := len(h.m.Requests()); n != 0 {
		t.Fatalf("/reload told the model: %d requests", n)
	}
}

// `atto reload` during a turn applies at the next step boundary: the
// request in flight keeps its prompt, the next has the new one and the
// report.
func TestAttoReloadBetweenSteps(t *testing.T) {
	gate := make(chan struct{})
	s, m := testServer(t, providertest.Reply{Command: "echo hi", Description: "Say hi", Gate: gate}, providertest.Reply{Text: "ok"})
	os.WriteFile(filepath.Join(s.Cwd, "AGENTS.md"), []byte("Project rules."), 0o644)
	h := harnessOn(t, s, m, "thread/start", map[string]any{})
	h.call("input/submit", map[string]any{"input": "edit AGENTS.md and reload"})
	h.m.Started(5 * time.Second)
	os.WriteFile(filepath.Join(s.Cwd, "AGENTS.md"), []byte("Rules the agent just wrote."), 0o644)
	if err := events.RequestReload(h.id); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond) // the inbox tick
	close(gate)
	h.completed()
	reqs := h.m.Requests()
	if len(reqs) != 2 || strings.Contains(reqs[0], "Rules the agent just wrote.") || !strings.Contains(reqs[1], "Rules the agent just wrote.") {
		t.Fatalf("%d requests; the second must carry the new prompt", len(reqs))
	}
	if !strings.Contains(reqs[1], "Reload applied. changed AGENTS file") {
		t.Fatalf("the model is not told:\n%s", reqs[1])
	}
}

// The built-in /diff shows the working tree's changes as an extText item
// the model never sees.
func TestDiffCommandIsDisplayOnly(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	s, m := testServer(t, providertest.Reply{Text: "first"}, providertest.Reply{Text: "second"})
	cwd := s.Cwd
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "core.autocrlf=false"}, args...)...)
		cmd.Dir = cwd
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	os.WriteFile(filepath.Join(cwd, "main.go"), []byte("package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n"), 0o644)
	os.WriteFile(filepath.Join(cwd, "util.go"), []byte("package main\n"), 0o644)
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "init")
	os.WriteFile(filepath.Join(cwd, "main.go"), []byte("package main\n\nfunc main() {\n\tprintln(\"hello\")\n\tprintln(\"bye\")\n}\n"), 0o644)
	os.WriteFile(filepath.Join(cwd, "util.go"), []byte("package main\n\nfunc util() {}\n"), 0o644)
	git("add", "util.go")
	h := harnessOn(t, s, m, "thread/start", map[string]any{})
	h.call("input/submit", map[string]any{"input": "q1"})
	h.completed()

	diff := func(arg string) map[string]any {
		t.Helper()
		h.call("input/submit", map[string]any{"input": strings.TrimSpace("/diff " + arg)})
		return itemOf(h.wait("item/completed", func(p map[string]any) bool { return itemOf(p)["type"] == ItemExtText }))
	}
	if it := diff("--staged"); !strings.HasPrefix(fmt.Sprint(it["text"]), "1 file changed, +2 -0\n") || strings.Contains(fmt.Sprint(it["text"]), "main.go") {
		t.Fatalf("--staged %v", it)
	}
	if it := diff("main.go"); it["title"] != "git diff main.go" || !strings.HasPrefix(fmt.Sprint(it["text"]), "1 file changed, +2 -1") {
		t.Fatalf("path %v", it)
	}
	if it := diff(""); it["ext"] != "diff" || it["lang"] != "diff" || !strings.Contains(fmt.Sprint(it["text"]), "2 files changed") {
		t.Fatalf("diff %v", it)
	}
	h.call("input/submit", map[string]any{"input": "q2"})
	h.completed()
	reqs := h.m.Requests()
	if len(reqs) != 2 || !strings.Contains(reqs[1], `"q1"`) || !strings.Contains(reqs[1], `"q2"`) {
		t.Fatalf("requests %v", reqs)
	}
	for _, bad := range []string{"diff --git", "files changed", "git diff", "util.go"} {
		if strings.Contains(reqs[1], bad) {
			t.Errorf("the request mentions %q", bad)
		}
	}
}

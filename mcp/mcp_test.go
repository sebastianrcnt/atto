package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/mcp/mcptest"
)

func TestMain(m *testing.M) {
	mcptest.ServeIfRequested()
	os.Exit(m.Run())
}

// env sets up an empty atto dir and a project root, and returns the
// project root.
func env(t *testing.T) string {
	t.Helper()
	t.Setenv("ATTO_DIR", t.TempDir())
	return t.TempDir()
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeEntry is the JSON of a stdio server entry that runs the fake server.
func fakeEntry(extraEnv map[string]string) string {
	cmd, env := mcptest.Command()
	maps.Copy(env, extraEnv)
	c := ServerConfig{Command: cmd, Env: env}
	data, _ := json.Marshal(c)
	return string(data)
}

func manager(t *testing.T, root string) *Manager {
	t.Helper()
	m := New(Options{Cwd: root, Root: root})
	t.Cleanup(func() { m.Close() })
	return m
}

func callText(t *testing.T, b Backend, server, tool string, args string) Result {
	t.Helper()
	var raw json.RawMessage
	if args != "" {
		raw = json.RawMessage(args)
	}
	res, err := b.Call(context.Background(), server, tool, raw)
	if err != nil {
		t.Fatalf("call %s %s: %v", server, tool, err)
	}
	return res
}

func infoOf(t *testing.T, b Backend, name string) Info {
	t.Helper()
	infos, err := b.Servers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range infos {
		if in.Name == name {
			return in
		}
	}
	t.Fatalf("no server %q in %+v", name, infos)
	return Info{}
}

func TestLoadMergesScopesByPrecedence(t *testing.T) {
	root := env(t)
	writeFile(t, Path(ScopeUser, root), `{"mcpServers": {
		"a": {"command": "user-a"}, "b": {"command": "user-b"}, "c": {"command": "user-c"}}}`)
	writeFile(t, Path(ScopeProject, root), `{"mcpServers": {
		"b": {"command": "project-b"}, "c": {"command": "project-c"}, "d": {"type": "http", "url": "http://d"}}}`)
	writeFile(t, Path(ScopeLocal, root), `{"mcpServers": {"c": {"command": "local-c", "args": ["x"]}}}`)

	servers, issues := Load(root)
	if len(issues) != 0 {
		t.Fatalf("issues: %v", issues)
	}
	got := map[string]string{}
	var names []string
	for _, s := range servers {
		got[s.Name] = s.Scope + ":" + s.Config.Target()
		names = append(names, s.Name)
	}
	want := map[string]string{"a": "user:user-a", "b": "project:project-b", "c": "local:local-c x", "d": "project:http://d"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if strings.Join(names, ",") != "a,b,c,d" {
		t.Errorf("order = %v", names)
	}
}

func TestLoadReportsBrokenFilesAndKeepsTheRest(t *testing.T) {
	root := env(t)
	writeFile(t, Path(ScopeUser, root), `{not json`)
	writeFile(t, Path(ScopeProject, root), `{"mcpServers": {"ok": {"command": "x"}, "bad": "nope"}}`)
	servers, issues := Load(root)
	if len(servers) != 1 || servers[0].Name != "ok" {
		t.Fatalf("servers = %+v", servers)
	}
	if len(issues) != 2 {
		t.Fatalf("issues = %v", issues)
	}
}

func TestExpand(t *testing.T) {
	vars := map[string]string{"TOKEN": "abc", "EMPTY": ""}
	lookup := func(k string) (string, bool) { v, ok := vars[k]; return v, ok }
	for _, c := range []struct {
		in, out string
		missing []string
	}{
		{"Bearer ${TOKEN}", "Bearer abc", nil},
		{"${NOPE:-fallback}", "fallback", nil},
		{"${EMPTY:-fallback}", "fallback", nil},
		{"${TOKEN:-fallback}", "abc", nil},
		{"${NOPE}", "", []string{"NOPE"}},
		{"a ${NOPE} b ${NOPE} ${OTHER}", "a  b  ", []string{"NOPE", "NOPE", "OTHER"}},
		{"$TOKEN and ${", "$TOKEN and ${", nil},
		{"${EMPTY}", "", nil},
	} {
		out, missing := Expand(c.in, lookup)
		if out != c.out || strings.Join(missing, ",") != strings.Join(c.missing, ",") {
			t.Errorf("Expand(%q) = %q, %v; want %q, %v", c.in, out, missing, c.out, c.missing)
		}
	}
	c := ServerConfig{Command: "${CMD:-run}", Args: []string{"--t=${TOKEN}"}, Env: map[string]string{"K": "${NOPE}"}, Headers: map[string]string{"A": "${TOKEN}"}}
	ex, missing := c.Expanded(lookup)
	if ex.Command != "run" || ex.Args[0] != "--t=abc" || ex.Headers["A"] != "abc" || len(missing) != 1 || missing[0] != "NOPE" {
		t.Errorf("expanded = %+v, %v", ex, missing)
	}
	if c.Command != "${CMD:-run}" {
		t.Error("Expanded changed the original")
	}
}

func TestValidate(t *testing.T) {
	for _, c := range []struct {
		c  ServerConfig
		ok bool
	}{
		{ServerConfig{Command: "x"}, true},
		{ServerConfig{Type: "stdio", Command: "x"}, true},
		{ServerConfig{URL: "http://x"}, true},
		{ServerConfig{Type: "sse", URL: "http://x"}, true},
		{ServerConfig{}, false},
		{ServerConfig{Type: "http"}, false},
		{ServerConfig{Command: "x", URL: "http://x"}, false},
		{ServerConfig{Type: "carrier-pigeon", Command: "x"}, false},
	} {
		if err := c.c.Validate(); (err == nil) != c.ok {
			t.Errorf("Validate(%+v) = %v", c.c, err)
		}
	}
}

func TestPutAndRemoveKeepOtherContent(t *testing.T) {
	root := env(t)
	path := Path(ScopeProject, root)
	writeFile(t, path, `{"other": {"keep": true}, "mcpServers": {"old": {"command": "old", "futureField": 1}}}`)
	if err := Put(ScopeProject, root, "new", ServerConfig{Command: "n", Args: []string{"a"}, Env: map[string]string{"K": "v"}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil || top["other"] == nil {
		t.Fatalf("file = %s (%v)", data, err)
	}
	servers, _ := Load(root)
	if len(servers) != 2 || servers[0].Name != "new" || servers[1].Name != "old" {
		t.Fatalf("servers = %+v", servers)
	}
	if !strings.Contains(string(data), "futureField") {
		t.Errorf("an unknown field of another server was dropped: %s", data)
	}
	ok, err := Remove(ScopeProject, root, "old")
	if !ok || err != nil {
		t.Fatalf("remove = %v, %v", ok, err)
	}
	if ok, _ := Remove(ScopeProject, root, "old"); ok {
		t.Error("removed twice")
	}
	if err := Put(ScopeLocal, root, "bad", ServerConfig{}); err == nil {
		t.Error("an invalid entry was written")
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v; the file may hold tokens", st.Mode().Perm())
		}
	}
}

func TestProjectServersNeedApprovalAndAHashChangeNeedsItAgain(t *testing.T) {
	root := env(t)
	file := Path(ScopeProject, root)
	writeFile(t, file, `{"mcpServers": {"p": {"command": "one"}, "q": {"command": "two"}}}`)
	writeFile(t, Path(ScopeUser, root), `{"mcpServers": {"u": {"command": "user"}}}`)
	writeFile(t, Path(ScopeLocal, root), `{"mcpServers": {"l": {"command": "local"}}}`)

	m := manager(t, root)
	status := func(name string) string { return infoOf(t, m, name).Status }
	if status("p") != NeedsApproval || status("q") != NeedsApproval {
		t.Fatalf("project servers: %s, %s", status("p"), status("q"))
	}
	if status("u") != NotStarted || status("l") != NotStarted {
		t.Fatalf("user and local servers need no approval: %s, %s", status("u"), status("l"))
	}
	if _, err := m.Tools(context.Background(), "p"); err == nil || !strings.Contains(err.Error(), "atto mcp approve p") {
		t.Fatalf("unapproved start: %v", err)
	}
	var ae *ApprovalError
	if _, err := m.Call(context.Background(), "p", "x", nil); !errors.As(err, &ae) {
		t.Fatalf("want an ApprovalError, got %v", err)
	}

	if err := m.Approve("p"); err != nil {
		t.Fatal(err)
	}
	if status("p") != NotStarted || status("q") != NeedsApproval {
		t.Fatalf("after approving p: %s, %s", status("p"), status("q"))
	}
	if err := m.Approve("u"); !errors.Is(err, ErrNotProject) {
		t.Fatalf("approving a user server: %v", err)
	}
	if err := m.Approve("nope"); err == nil {
		t.Fatal("approved a server that does not exist")
	}

	// The entry changes: approval is for what was approved.
	writeFile(t, file, `{"mcpServers": {"p": {"command": "one", "args": ["--evil"]}, "q": {"command": "two"}}}`)
	m.Reload()
	if status("p") != NeedsApproval {
		t.Fatalf("p after its entry changed: %s", status("p"))
	}
	// Changing only the environment's values does not: the hash is of the entry as written.
	if err := m.Approve("p"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WHATEVER", "1")
	m.Reload()
	if status("p") != NotStarted {
		t.Fatalf("p = %s", status("p"))
	}

	// Deny sticks until the entry changes or it is approved.
	if err := m.Deny("q"); err != nil {
		t.Fatal(err)
	}
	if status("q") != DeniedStatus {
		t.Fatalf("q = %s", status("q"))
	}
	if _, err := m.Tools(context.Background(), "q"); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("start of a denied server: %v", err)
	}
	if err := m.Approve("q"); err != nil || status("q") != NotStarted {
		t.Fatalf("approving a denied server: %v %s", err, status("q"))
	}

	// "Allow all" covers the file, including servers added later.
	writeFile(t, file, `{"mcpServers": {"p": {"command": "one"}, "r": {"command": "new"}}}`)
	m.Reload()
	if status("r") != NeedsApproval {
		t.Fatalf("r = %s", status("r"))
	}
	if err := m.ApproveAll("r"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, file, `{"mcpServers": {"p": {"command": "changed"}, "r": {"command": "new"}, "s": {"command": "later"}}}`)
	m.Reload()
	for _, n := range []string{"p", "r", "s"} {
		if status(n) != NotStarted {
			t.Errorf("%s = %s after allow-all", n, status(n))
		}
	}
	// Another project's file is not covered.
	other := t.TempDir()
	writeFile(t, Path(ScopeProject, other), `{"mcpServers": {"p": {"command": "changed"}}}`)
	m2 := manager(t, other)
	if infoOf(t, m2, "p").Status != NeedsApproval {
		t.Error("approval leaked to another project")
	}
}

func TestStartsLazilyAndKeepsTheServerAcrossCalls(t *testing.T) {
	root := env(t)
	writeFile(t, Path(ScopeUser, root), `{"mcpServers": {"fake": `+fakeEntry(nil)+`}}`)
	m := manager(t, root)

	in := infoOf(t, m, "fake")
	if in.Status != NotStarted || in.Tools != -1 || in.Transport != TransportStdio {
		t.Fatalf("before use: %+v", in)
	}
	pid1 := callText(t, m, "fake", "pid", "").Text
	for want := 1; want <= 3; want++ {
		if got := callText(t, m, "fake", "count", `{}`).Text; got != string(rune('0'+want)) {
			t.Fatalf("count call %d returned %q: the server was restarted", want, got)
		}
	}
	if pid2 := callText(t, m, "fake", "pid", "").Text; pid1 != pid2 {
		t.Fatalf("server pid changed: %s -> %s", pid1, pid2)
	}
	in = infoOf(t, m, "fake")
	if in.Status != Running || in.Tools != 6 {
		t.Fatalf("after use: %+v", in)
	}
}

func TestToolsAndResultFormatting(t *testing.T) {
	root := env(t)
	writeFile(t, Path(ScopeUser, root), `{"mcpServers": {"fake": `+fakeEntry(nil)+`}}`)
	m := manager(t, root)

	tools, err := m.Tools(context.Background(), "fake")
	if err != nil {
		t.Fatal(err)
	}
	var echo *ToolInfo
	for i := range tools {
		if tools[i].Name == "echo" {
			echo = &tools[i]
		}
	}
	if echo == nil || echo.Server != "fake" || !strings.HasPrefix(echo.Description, "Echo the text back.") {
		t.Fatalf("tools = %+v", tools)
	}
	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(echo.InputSchema, &schema); err != nil || schema.Properties["text"] == nil {
		t.Fatalf("schema = %s (%v)", echo.InputSchema, err)
	}

	if got := callText(t, m, "fake", "echo", `{"text": "héllo"}`); got.Text != "héllo" || got.IsError {
		t.Fatalf("echo = %+v", got)
	}
	img := callText(t, m, "fake", "image", "")
	if img.Text != "caption\n[image: image/png, 16 bytes]" || len(img.Content) != 2 || img.Content[1].Type != "image" {
		t.Fatalf("image = %+v", img)
	}
	if res := callText(t, m, "fake", "fail", ""); !res.IsError || res.Text != "it broke" {
		t.Fatalf("fail = %+v", res)
	}

	if _, err := m.Call(context.Background(), "fake", "nope", nil); err == nil {
		t.Error("a call to an unknown tool succeeded")
	}
	if _, err := m.Call(context.Background(), "fake", "echo", json.RawMessage(`[1]`)); err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Errorf("non-object arguments: %v", err)
	}
	if _, err := m.Call(context.Background(), "ghost", "echo", nil); err == nil || !strings.Contains(err.Error(), "configured: fake") {
		t.Errorf("unknown server: %v", err)
	}
}

func TestFailedStartIsReportedAndRetried(t *testing.T) {
	root := env(t)
	missing := filepath.Join(t.TempDir(), "no-such-binary")
	writeFile(t, Path(ScopeUser, root), `{"mcpServers": {"broken": {"command": "`+filepath.ToSlash(missing)+`"}, "needsenv": {"command": "x", "args": ["${ATTO_TEST_UNSET_VAR}"]}}}`)
	m := manager(t, root)
	if _, err := m.Tools(context.Background(), "broken"); err == nil || !strings.Contains(err.Error(), "failed to start") {
		t.Fatalf("start: %v", err)
	}
	in := infoOf(t, m, "broken")
	if in.Status != Failed || in.Error == "" {
		t.Fatalf("status = %+v", in)
	}
	if _, err := m.Tools(context.Background(), "needsenv"); err == nil || !strings.Contains(err.Error(), "ATTO_TEST_UNSET_VAR") {
		t.Fatalf("unset variable: %v", err)
	}
	// Fixing it (the file now exists as a real server) is picked up by the next use.
	writeFile(t, Path(ScopeUser, root), `{"mcpServers": {"broken": `+fakeEntry(nil)+`}}`)
	m.Reload()
	if got := callText(t, m, "broken", "echo", `{"text":"ok"}`).Text; got != "ok" {
		t.Fatalf("after the fix: %q", got)
	}
}

func TestEnvAndExpansionReachTheServer(t *testing.T) {
	root := env(t)
	t.Setenv("ATTO_TEST_SECRET", "s3cret")
	writeFile(t, Path(ScopeUser, root), `{"mcpServers": {"fake": `+fakeEntry(map[string]string{"GREETING": "hi-${ATTO_TEST_SECRET}-${ATTO_TEST_NOPE:-dflt}"})+`}}`)
	m := manager(t, root)
	if got := callText(t, m, "fake", "env", `{"name": "GREETING"}`).Text; got != "hi-s3cret-dflt" {
		t.Fatalf("env = %q", got)
	}
}

func TestHTTPServerWithHeaders(t *testing.T) {
	root := env(t)
	var auth atomic.Value
	url := mcptest.HTTP(t, func(h http.Header) {
		if v := h.Get("Authorization"); v != "" {
			auth.Store(v)
		}
	})
	t.Setenv("ATTO_TEST_TOKEN", "tok123")
	cfg, _ := json.Marshal(ServerConfig{Type: "http", URL: url, Headers: map[string]string{"Authorization": "Bearer ${ATTO_TEST_TOKEN}"}})
	writeFile(t, Path(ScopeLocal, root), `{"mcpServers": {"remote": `+string(cfg)+`}}`)
	m := manager(t, root)
	if got := callText(t, m, "remote", "echo", `{"text": "over http"}`).Text; got != "over http" {
		t.Fatalf("echo = %q", got)
	}
	if got, _ := auth.Load().(string); got != "Bearer tok123" {
		t.Fatalf("Authorization = %q", got)
	}
	if in := infoOf(t, m, "remote"); in.Status != Running || in.Transport != TransportHTTP {
		t.Fatalf("info = %+v", in)
	}
}

func TestReloadKeepsUnchangedServersAndRestartsChangedOnes(t *testing.T) {
	root := env(t)
	path := Path(ScopeUser, root)
	writeFile(t, path, `{"mcpServers": {"keep": `+fakeEntry(nil)+`, "change": `+fakeEntry(nil)+`, "gone": `+fakeEntry(nil)+`}}`)
	m := manager(t, root)
	pids := map[string]string{}
	for _, n := range []string{"keep", "change", "gone"} {
		pids[n] = callText(t, m, n, "pid", "").Text
		callText(t, m, n, "count", "")
	}
	writeFile(t, path, `{"mcpServers": {"keep": `+fakeEntry(nil)+`, "change": `+fakeEntry(map[string]string{"X": "1"})+`, "added": `+fakeEntry(nil)+`}}`)
	m.Reload()
	if got := strings.Join(m.Names(), ","); got != "added,change,keep" {
		t.Fatalf("names = %s", got)
	}
	if infoOf(t, m, "keep").Status != Running {
		t.Error("keep was stopped by a reload that did not change it")
	}
	if got := callText(t, m, "keep", "pid", "").Text; got != pids["keep"] {
		t.Errorf("keep restarted: %s -> %s", pids["keep"], got)
	}
	if got := callText(t, m, "keep", "count", "").Text; got != "2" {
		t.Errorf("keep's state was lost: count = %s", got)
	}
	if in := infoOf(t, m, "change"); in.Status != NotStarted {
		t.Errorf("change = %+v: a changed server restarts on next use", in)
	}
	if got := callText(t, m, "change", "count", "").Text; got != "1" {
		t.Errorf("change did not restart: count = %s", got)
	}
	if _, err := m.Call(context.Background(), "gone", "pid", nil); err == nil {
		t.Error("a removed server still answers")
	}
}

func TestServerExitIsNoticedAndRestarted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("kills the server by pid")
	}
	root := env(t)
	writeFile(t, Path(ScopeUser, root), `{"mcpServers": {"fake": `+fakeEntry(nil)+`}}`)
	m := manager(t, root)
	pid := callText(t, m, "fake", "pid", "").Text
	var p int
	for _, c := range pid {
		p = p*10 + int(c-'0')
	}
	proc, _ := os.FindProcess(p)
	_ = proc.Kill()
	deadline := time.Now().Add(5 * time.Second)
	for infoOf(t, m, "fake").Status == Running && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if in := infoOf(t, m, "fake"); in.Status != Failed || !strings.Contains(in.Error, "exited") {
		t.Fatalf("after the server died: %+v", in)
	}
	if got := callText(t, m, "fake", "count", "").Text; got != "1" {
		t.Fatalf("count after restart = %s", got)
	}
}

func TestSessionEndpointSharesTheRunningServer(t *testing.T) {
	root := env(t)
	writeFile(t, Path(ScopeUser, root), `{"mcpServers": {"fake": `+fakeEntry(nil)+`}}`)
	m := manager(t, root)
	m.SetSession("sess-1")

	epPath := filepath.Join(os.Getenv("ATTO_DIR"), "mcp", "sess-1.json")
	if _, err := os.Stat(epPath); err != nil {
		t.Fatalf("no endpoint file: %v", err)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(epPath); st.Mode().Perm() != 0o600 {
			t.Errorf("endpoint file mode = %v", st.Mode().Perm())
		}
	}
	remote, err := Dial("sess-1")
	if err != nil {
		t.Fatal(err)
	}
	// Calls through the socket and in the session reach the same process.
	if got := callText(t, remote, "fake", "count", "").Text; got != "1" {
		t.Fatalf("remote count = %s", got)
	}
	if got := callText(t, m, "fake", "count", "").Text; got != "2" {
		t.Fatalf("local count = %s: the socket started a second server", got)
	}
	if got := callText(t, remote, "fake", "count", "").Text; got != "3" {
		t.Fatalf("remote count = %s", got)
	}
	infos, err := remote.Servers(context.Background())
	if err != nil || len(infos) != 1 || infos[0].Status != Running || infos[0].Tools != 6 {
		t.Fatalf("remote servers = %+v, %v", infos, err)
	}
	tools, problems, err := remote.AllTools(context.Background())
	if err != nil || len(problems) != 0 || len(tools) != 6 {
		t.Fatalf("remote all tools = %d, %v, %v", len(tools), problems, err)
	}
	if _, err := remote.Call(context.Background(), "fake", "nope", nil); err == nil {
		t.Error("a failed call did not fail")
	}
	if _, err := remote.Tools(context.Background(), "ghost"); err == nil || !strings.Contains(err.Error(), "no MCP server") {
		t.Errorf("unknown server over the socket: %v", err)
	}

	// The session moves (/clear, /resume): the endpoint follows the ID.
	m.SetSession("sess-2")
	if _, err := os.Stat(epPath); err == nil {
		t.Error("the old session's endpoint file is still there")
	}
	if b, err := Dial("sess-2"); err != nil {
		t.Fatal(err)
	} else if got := callText(t, b, "fake", "count", "").Text; got != "4" {
		t.Fatalf("count in the new session = %s", got)
	}
	if _, err := Dial("sess-1"); err == nil {
		t.Error("the old session id still dials")
	}

	m.Close()
	if _, err := Dial("sess-2"); err == nil {
		t.Error("the endpoint outlived the manager")
	}
}

func TestSessionEndpointRejectsABadToken(t *testing.T) {
	root := env(t)
	writeFile(t, Path(ScopeUser, root), `{"mcpServers": {"fake": `+fakeEntry(nil)+`}}`)
	m := manager(t, root)
	m.SetSession("s")
	data, err := os.ReadFile(filepath.Join(os.Getenv("ATTO_DIR"), "mcp", "s.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ep endpoint
	if err := json.Unmarshal(data, &ep); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", "wrong", ep.Token + "x"} {
		r := &remote{endpoint{Network: ep.Network, Addr: ep.Addr, Token: token}}
		if _, err := r.Call(context.Background(), "fake", "count", nil); err == nil || !strings.Contains(err.Error(), "bad token") {
			t.Errorf("token %q: %v", token, err)
		}
	}
	// Garbage on the socket does not hurt it.
	c, err := net.Dial(ep.Network, ep.Addr)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte("not json\n"))
	c.Close()
	r := &remote{ep}
	if got := callText(t, r, "fake", "count", "").Text; got != "1" {
		t.Fatalf("count = %s", got)
	}
	// No token was ever used to start a server.
	if got := callText(t, m, "fake", "count", "").Text; got != "2" {
		t.Fatalf("count = %s", got)
	}
}

func TestSessionWithoutServersLeavesNoEndpoint(t *testing.T) {
	root := env(t)
	m := manager(t, root)
	m.SetSession("empty")
	if _, err := os.Stat(filepath.Join(os.Getenv("ATTO_DIR"), "mcp")); err == nil {
		t.Error("a session without MCP servers left files behind")
	}
	// Configuring one and reloading publishes it.
	writeFile(t, Path(ScopeLocal, root), `{"mcpServers": {"fake": `+fakeEntry(nil)+`}}`)
	m.Reload()
	if _, err := Dial("empty"); err != nil {
		t.Fatalf("after configuring a server: %v", err)
	}
}

func TestLongAttoDirFallsBackToAShortSocketPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket paths are as long as Windows paths")
	}
	long := filepath.Join(t.TempDir(), strings.Repeat("d", 60), strings.Repeat("e", 60))
	if err := os.MkdirAll(long, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATTO_DIR", long)
	root := t.TempDir()
	writeFile(t, Path(ScopeLocal, root), `{"mcpServers": {"fake": `+fakeEntry(nil)+`}}`)
	m := manager(t, root)
	m.SetSession("long")
	b, err := Dial("long")
	if err != nil {
		t.Fatal(err)
	}
	if got := callText(t, b, "fake", "echo", `{"text":"x"}`).Text; got != "x" {
		t.Fatalf("echo = %q", got)
	}
}

func TestConnectUsesTheSessionOrFallsBack(t *testing.T) {
	root := env(t)
	writeFile(t, Path(ScopeUser, root), `{"mcpServers": {"fake": `+fakeEntry(nil)+`}}`)
	o := Options{Cwd: root, Root: root}

	// No session id (a normal terminal): a manager for this command.
	b, inSession, note := Connect("", o)
	if inSession || note != "" {
		t.Fatalf("no session: %v %q", inSession, note)
	}
	for range 2 {
		if got := callText(t, b, "fake", "count", "").Text; got != "1" {
			t.Fatalf("fallback count = %s: every one-shot call starts the server", got)
		}
		b.Close()
		b, _, _ = Connect("", o)
	}
	b.Close()

	// A session id with no endpoint (the session never configured MCP, or ended).
	b, inSession, _ = Connect("ghost", o)
	if inSession {
		t.Fatal("connected to a session that does not exist")
	}
	b.Close()

	// A live session.
	m := manager(t, root)
	m.SetSession("live")
	callText(t, m, "fake", "count", "")
	b, inSession, note = Connect("live", o)
	if !inSession || note != "" {
		t.Fatalf("live session: %v %q", inSession, note)
	}
	if got := callText(t, b, "fake", "count", "").Text; got != "2" {
		t.Fatalf("session count = %s", got)
	}

	// A stale endpoint: the file is there, nothing listens.
	m.mu.Lock()
	ipc := m.ipc
	m.mu.Unlock()
	_ = ipc.ln.Close()
	b, inSession, note = Connect("live", o)
	if inSession || note == "" {
		t.Fatalf("stale endpoint: %v %q", inSession, note)
	}
	b.Close()
}

func TestPromptNamesAreSortedAndIndependentOfApproval(t *testing.T) {
	root := env(t)
	writeFile(t, Path(ScopeUser, root), `{"mcpServers": {"zeta": {"command": "z"}, "alpha": {"command": "a"}}}`)
	writeFile(t, Path(ScopeProject, root), `{"mcpServers": {"mid": {"command": "m"}}}`)
	m := manager(t, root)
	want := "alpha,mid,zeta"
	if got := strings.Join(m.PromptServers(), ","); got != want {
		t.Fatalf("names = %s", got)
	}
	if err := m.Approve("mid"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(m.PromptServers(), ","); got != want {
		t.Fatalf("names changed with an approval: %s", got)
	}
}

func TestLocalScopeLivesOutsideTheRepository(t *testing.T) {
	root := env(t)
	local := Path(ScopeLocal, root)
	if strings.HasPrefix(local, root) || !strings.HasPrefix(local, os.Getenv("ATTO_DIR")) {
		t.Fatalf("local path %s", local)
	}
	if other := Path(ScopeLocal, t.TempDir()); other == local {
		t.Fatal("two projects share a local file")
	}
	if Path(ScopeLocal, root) != local {
		t.Fatal("not stable")
	}
	writeFile(t, filepath.Join(root, ".atto", "mcp.json"), `{"mcpServers": {"evil": {"command": "x"}}}`)
	if servers, _ := Load(root); len(servers) != 0 {
		t.Fatalf("the repository's file was read: %+v", servers)
	}
	if Ignored(root) == "" {
		t.Fatal("not reported")
	}
	if err := Put(ScopeLocal, root, "mine", ServerConfig{Command: "x"}); err != nil {
		t.Fatal(err)
	}
	if servers, _ := Load(root); len(servers) != 1 || servers[0].Scope != ScopeLocal || servers[0].Path != local {
		t.Fatalf("servers = %+v", servers)
	}
}

func TestRevokeProjectApproval(t *testing.T) {
	root := env(t)
	writeFile(t, Path(ScopeProject, root), `{"mcpServers":{"one":{"command":"one"},"two":{"command":"two"}}}`)
	servers, _ := Load(root)
	one, two := servers[0], servers[1]
	if err := Approve(one); err != nil {
		t.Fatal(err)
	}
	if err := Revoke(one); err != nil {
		t.Fatal(err)
	}
	if ApprovalOf(one) != Pending {
		t.Fatal("revoked approval still allows the server")
	}
	if err := Deny(one); err != nil {
		t.Fatal(err)
	}
	if err := Revoke(one); err != nil {
		t.Fatal(err)
	}
	if ApprovalOf(one) != Pending {
		t.Fatal("revocation did not forget denial")
	}
	if err := ApproveAll(one); err != nil {
		t.Fatal(err)
	}
	if err := Revoke(one); err != nil {
		t.Fatal(err)
	}
	if ApprovalOf(one) != Pending || ApprovalOf(two) != Approved {
		t.Fatal("revocation of a legacy wildcard must preserve other current servers")
	}
	two.Config.Command = "changed"
	if ApprovalOf(two) != Pending {
		t.Fatal("legacy wildcard was not converted to content approvals")
	}
}

func TestReloadStopsRevokedProjectServer(t *testing.T) {
	root := env(t)
	writeFile(t, Path(ScopeProject, root), `{"mcpServers":{"fake":`+fakeEntry(nil)+`}}`)
	m := manager(t, root)
	if err := m.Approve("fake"); err != nil {
		t.Fatal(err)
	}
	callText(t, m, "fake", "pid", "")
	if infoOf(t, m, "fake").Status != Running {
		t.Fatal("approved server did not start")
	}
	s, _ := m.Server("fake")
	if err := Revoke(s); err != nil {
		t.Fatal(err)
	}
	m.Reload()
	if infoOf(t, m, "fake").Status != NeedsApproval {
		t.Fatal("reload retained the revoked server's running connection")
	}
	if _, err := m.Tools(context.Background(), "fake"); err == nil {
		t.Fatal("revoked server could still be used")
	}
}

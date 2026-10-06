package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/mcp"
	"github.com/sebastianrcnt/atto/mcp/mcptest"
)

// The test binary also serves the processes atto agent starts: job
// supervisors (_supervise) and subagent turns (_agent-turn).
func TestMain(m *testing.M) {
	mcptest.ServeIfRequested()
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "_supervise":
			if jobs.Supervise(os.Args[2]) != nil {
				os.Exit(1)
			}
			os.Exit(0)
		case "view": // TestPrintView runs it as "atto view"
			if err := RunView(os.Args[2:], os.Stdout); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		case "_agent-turn":
			if err := RunAgentTurn(os.Args[2:], io.Discard); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

// mcpProject sets up an empty atto dir and a project (a git root) that is
// the working directory, outside any agent.
func mcpProject(t *testing.T) string {
	t.Helper()
	t.Setenv("ATTO_DIR", t.TempDir())
	t.Setenv(config.EnvAgent, "")
	t.Setenv("ATTO_SESSION_ID", "")
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	return root
}

func runMCP(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out strings.Builder
	err := RunMCP(args, &out)
	return out.String(), err
}

// addFake adds the fake stdio server under name in scope, as a user would.
func addFake(t *testing.T, name, scope string) {
	t.Helper()
	cmd, env := mcptest.Command()
	args := []string{"add", name, "-scope", scope}
	for k, v := range env {
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, "--", cmd)
	if out, err := runMCP(t, args...); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
}

func TestMCPAddListRemove(t *testing.T) {
	root := mcpProject(t)

	out, err := runMCP(t, "list")
	if err != nil || !strings.Contains(out, "No MCP servers") {
		t.Fatalf("empty list: %q %v", out, err)
	}

	// Flags before or after the name, repeated -e and -H, a command with its own flags.
	if out, err := runMCP(t, "add", "gh", "-scope", "user", "-e", "A=1", "-e", "B=two=2", "--", "npx", "-y", "server-github"); err != nil || !strings.Contains(out, "Added gh") {
		t.Fatalf("add: %q %v", out, err)
	}
	if out, err := runMCP(t, "add", "-scope", "project", "-H", "Authorization: Bearer ${TOKEN}", "-H", "X-A:b", "-url", "https://example.invalid/mcp", "rem"); err != nil || !strings.Contains(out, "atto mcp approve rem") {
		t.Fatalf("add remote: %q %v", out, err)
	}
	if out, err := runMCP(t, "add", "loc", "--", "echo"); err != nil || !strings.Contains(out, "projects") {
		t.Fatalf("default scope is local: %q %v", out, err)
	}

	servers, issues := mcp.Load(root)
	if len(issues) != 0 || len(servers) != 3 {
		t.Fatalf("servers = %+v %v", servers, issues)
	}
	byName := map[string]mcp.Server{}
	for _, s := range servers {
		byName[s.Name] = s
	}
	gh := byName["gh"]
	if gh.Scope != "user" || gh.Config.Command != "npx" || len(gh.Config.Args) != 2 || gh.Config.Env["B"] != "two=2" {
		t.Errorf("gh = %+v", gh)
	}
	rem := byName["rem"]
	if rem.Scope != "project" || rem.Config.Transport() != "http" || rem.Config.Headers["Authorization"] != "Bearer ${TOKEN}" || rem.Config.Headers["X-A"] != "b" {
		t.Errorf("rem = %+v", rem)
	}
	if byName["loc"].Scope != "local" {
		t.Errorf("loc = %+v", byName["loc"])
	}

	out, err = runMCP(t, "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"gh", "user", "stdio", "not started", "npx -y server-github", "rem", "project", "http", "needs approval: atto mcp approve rem", "loc", "local"} {
		if !strings.Contains(out, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}
	out, err = runMCP(t, "list", "-json")
	var infos []mcp.Info
	if err != nil || json.Unmarshal([]byte(out), &infos) != nil || len(infos) != 3 || infos[0].Tools != -1 {
		t.Fatalf("list -json: %q %v", out, err)
	}

	// Errors: duplicate in the same scope, bad flags and values.
	for _, args := range [][]string{
		{"add", "gh", "-scope", "user", "--", "x"},
		{"add", "x", "-scope", "everywhere", "--", "x"},
		{"add", "x"},
		{"add", "x", "-url", "http://a", "--", "cmd"},
		{"add", "x", "-url", "http://a", "-e", "K=V"},
		{"add", "x", "-H", "A: b", "--", "cmd"},
		{"add", "x", "-url", "http://a", "-H", "nocolon"},
		{"add", "x", "-e", "nokey", "--", "cmd"},
		{"add", "a b", "--", "cmd"},
	} {
		if out, err := runMCP(t, args...); err == nil {
			t.Errorf("%v succeeded: %q", args, out)
		}
	}

	if out, err := runMCP(t, "remove", "gh"); err != nil || !strings.Contains(out, "Removed gh") {
		t.Fatalf("remove: %q %v", out, err)
	}
	if _, err := runMCP(t, "remove", "gh"); err == nil {
		t.Error("removed twice")
	}
	// Defined in two scopes: the scope must be named.
	addFake(t, "dup", "user")
	addFake(t, "dup", "local")
	if _, err := runMCP(t, "remove", "dup"); err == nil || !strings.Contains(err.Error(), "-scope") {
		t.Errorf("remove of an ambiguous name: %v", err)
	}
	if _, err := runMCP(t, "remove", "dup", "-scope", "local"); err != nil {
		t.Fatal(err)
	}
	if s, _ := mcp.Load(root); len(s) != 3 { // rem, loc, dup(user)
		t.Errorf("servers after remove = %+v", s)
	}
}

func TestMCPToolsAndCall(t *testing.T) {
	mcpProject(t)
	addFake(t, "fake", "user")

	out, err := runMCP(t, "tools")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "fake  echo") || !strings.Contains(out, "Echo the text back.") || strings.Contains(out, "Second line") {
		t.Errorf("tools (one line each):\n%s", out)
	}
	out, err = runMCP(t, "tools", "fake")
	if err != nil || strings.Count(out, "\n") != 6 {
		t.Errorf("tools fake: %q %v", out, err)
	}
	out, err = runMCP(t, "tools", "fake", "echo")
	var def struct {
		Name        string
		Description string
		InputSchema struct {
			Required   []string
			Properties map[string]any
		}
	}
	if err != nil || json.Unmarshal([]byte(out), &def) != nil || def.Name != "echo" || def.InputSchema.Properties["text"] == nil || len(def.InputSchema.Required) != 1 {
		t.Errorf("tools fake echo: %q %v", out, err)
	}
	if _, err := runMCP(t, "tools", "fake", "nope"); err == nil || !strings.Contains(err.Error(), "echo") {
		t.Errorf("unknown tool: %v", err)
	}
	if _, err := runMCP(t, "tools", "ghost"); err == nil {
		t.Error("tools of an unknown server")
	}
	out, err = runMCP(t, "tools", "-json")
	var tools []mcp.ToolInfo
	if err != nil || json.Unmarshal([]byte(out), &tools) != nil || len(tools) != 6 {
		t.Errorf("tools -json: %q %v", out, err)
	}

	if out, err := runMCP(t, "call", "fake", "echo", `{"text": "hi there"}`); err != nil || out != "hi there\n" {
		t.Errorf("call: %q %v", out, err)
	}
	if out, err := runMCP(t, "call", "fake", "image"); err != nil || out != "caption\n[image: image/png, 16 bytes]\n" {
		t.Errorf("call image: %q %v", out, err)
	}
	// A tool that reports an error: its text is printed and the exit is non-zero, silently.
	out, err = runMCP(t, "call", "fake", "fail", "{}")
	if !errors.Is(err, ErrSilent) || out != "it broke\n" {
		t.Errorf("call fail: %q %v", out, err)
	}
	for _, args := range [][]string{
		{"call"}, {"call", "fake"}, {"call", "fake", "echo", "[1]"}, {"call", "fake", "echo", "not json"},
		{"call", "fake", "echo", "{}", "extra"}, {"call", "fake", "nope"}, {"call", "ghost", "echo"},
	} {
		if out, err := runMCP(t, args...); err == nil {
			t.Errorf("%v succeeded: %q", args, out)
		}
	}

	// Arguments from stdin.
	in, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { in.Close() }) // Windows cannot remove an open file
	_, _ = in.WriteString(`{"text": "from stdin"}`)
	_, _ = in.Seek(0, 0)
	old := os.Stdin
	os.Stdin = in
	defer func() { os.Stdin = old }()
	if out, err := runMCP(t, "call", "fake", "echo", "-"); err != nil || out != "from stdin\n" {
		t.Errorf("call -: %q %v", out, err)
	}
}

func TestMCPFromTheAgentShellSharesTheSessionsServer(t *testing.T) {
	root := mcpProject(t)
	addFake(t, "fake", "user")

	// The session's side: a manager with the session's ID. The agent's side:
	// ATTO_AGENT and ATTO_SESSION_ID, as core.Env sets them.
	m := mcp.New(mcp.Options{Cwd: root, Root: root})
	defer m.Close()
	m.SetSession("agent-sess")
	t.Setenv(config.EnvAgent, "1")
	t.Setenv("ATTO_SESSION_ID", "agent-sess")

	for want := 1; want <= 3; want++ {
		out, err := runMCP(t, "call", "fake", "count")
		if err != nil || strings.TrimSpace(out) != string(rune('0'+want)) {
			t.Fatalf("call %d from the agent's shell: %q %v: the server was not shared", want, out, err)
		}
	}
	out, err := runMCP(t, "list")
	if err != nil || !strings.Contains(out, "running, 6 tools") {
		t.Errorf("list in the session: %q %v", out, err)
	}
	if out, err := runMCP(t, "tools", "fake", "pid"); err != nil || !strings.Contains(out, `"name": "pid"`) {
		t.Errorf("tools in the session: %q %v", out, err)
	}

	// Without the session (a normal terminal) each call starts the server for itself.
	t.Setenv("ATTO_SESSION_ID", "")
	for i := 0; i < 2; i++ {
		out, err := runMCP(t, "call", "fake", "count")
		if err != nil || strings.TrimSpace(out) != "1" {
			t.Fatalf("one-shot call: %q %v", out, err)
		}
	}
	// A session ID whose session is gone falls back too.
	t.Setenv("ATTO_SESSION_ID", "gone")
	if out, err := runMCP(t, "call", "fake", "count"); err != nil || strings.TrimSpace(out) != "1" {
		t.Fatalf("call for a dead session: %q %v", out, err)
	}
}

func TestMCPApprovalFlow(t *testing.T) {
	root := mcpProject(t)
	cmd, env := mcptest.Command()
	entry, _ := json.Marshal(mcp.ServerConfig{Command: cmd, Env: env})
	file := mcp.Path(mcp.ScopeProject, root)
	if err := os.WriteFile(file, []byte(`{"mcpServers": {"proj": `+string(entry)+`}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Listed but not started, and a call says how to approve.
	out, _ := runMCP(t, "list")
	if !strings.Contains(out, "needs approval") {
		t.Fatalf("list:\n%s", out)
	}
	if _, err := runMCP(t, "call", "proj", "echo", `{"text":"x"}`); err == nil || !strings.Contains(err.Error(), "atto mcp approve proj") {
		t.Fatalf("call of an unapproved server: %v", err)
	}

	// An agent cannot approve.
	t.Setenv(config.EnvAgent, "1")
	if _, err := runMCP(t, "approve", "proj"); err == nil || !strings.Contains(err.Error(), "not from an agent") {
		t.Fatalf("approve inside the agent: %v", err)
	}
	if out, _ := runMCP(t, "list"); !strings.Contains(out, "needs approval") {
		t.Fatalf("a refused approval approved anyway:\n%s", out)
	}
	t.Setenv(config.EnvAgent, "")

	if out, err := runMCP(t, "approve", "proj"); err != nil || !strings.Contains(out, "Approved proj") {
		t.Fatalf("approve: %q %v", out, err)
	}
	if out, err := runMCP(t, "call", "proj", "echo", `{"text":"ok"}`); err != nil || out != "ok\n" {
		t.Fatalf("call after approval: %q %v", out, err)
	}

	// Editing the entry takes the approval away.
	changed, _ := json.Marshal(mcp.ServerConfig{Command: cmd, Args: []string{"-extra"}, Env: env})
	if err := os.WriteFile(file, []byte(`{"mcpServers": {"proj": `+string(changed)+`}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runMCP(t, "call", "proj", "echo", `{"text":"x"}`); err == nil || !strings.Contains(err.Error(), "approve") {
		t.Fatalf("call after the entry changed: %v", err)
	}

	addFake(t, "mine", "user")
	if _, err := runMCP(t, "approve", "mine"); err == nil || !strings.Contains(err.Error(), "need approval") {
		t.Errorf("approve of a user server: %v", err)
	}
	if _, err := runMCP(t, "approve", "ghost"); err == nil {
		t.Error("approve of a server that does not exist")
	}
}

func TestMCPUnknownCommandAndHelp(t *testing.T) {
	mcpProject(t)
	if _, err := runMCP(t, "frobnicate"); err == nil || !strings.Contains(err.Error(), "usage: atto mcp") {
		t.Errorf("unknown command: %v", err)
	}
	if out, err := runMCP(t, "help"); err != nil || !strings.Contains(out, ".mcp.json") {
		t.Errorf("help: %q %v", out, err)
	}
}

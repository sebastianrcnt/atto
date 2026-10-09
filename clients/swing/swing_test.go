package swing_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/provider/providertest"
	"github.com/sebastianrcnt/atto/session"
)

func java21(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"java", "javac", "jar"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " unavailable")
		}
	}
	out, err := exec.Command("javac", "-version").CombinedOutput()
	if err != nil {
		t.Skip("javac unavailable")
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		t.Skip("unknown javac version")
	}
	major, _ := strconv.Atoi(strings.Split(fields[1], ".")[0])
	if major < 21 {
		t.Skip("Swing requires Java 21")
	}
}

func buildJava(t *testing.T, tests bool) (string, string) {
	t.Helper()
	java21(t)
	dir := t.TempDir()
	classes, testClasses := filepath.Join(dir, "classes"), filepath.Join(dir, "tests")
	for _, path := range []string{classes, testClasses} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	compile := func(root, dest, cp string) {
		args := []string{"--release", "21", "-encoding", "UTF-8", "-d", dest}
		if cp != "" {
			args = append(args, "-cp", cp)
		}
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(path, ".java") {
				// Child-process javac reads are invisible to the Go test cache.
				// Read sources here too, so edits invalidate cached Java results.
				if _, err := os.ReadFile(path); err != nil {
					return err
				}
				args = append(args, path)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("javac", args...).CombinedOutput(); err != nil {
			t.Fatalf("javac: %v\n%s", err, out)
		}
	}
	compile("src", classes, "")
	if tests {
		compile("test", testClasses, classes)
	}
	jar := filepath.Join(dir, "atto-swing.jar")
	if out, err := exec.Command("jar", "--create", "--file", jar, "--main-class", "atto.swing.Main", "-C", classes, ".").CombinedOutput(); err != nil {
		t.Fatalf("jar: %v\n%s", err, out)
	}
	return jar, classes + string(os.PathListSeparator) + testClasses
}

func TestJavaCore(t *testing.T) {
	_, cp := buildJava(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "java", "-Djava.awt.headless=true", "-cp", cp, "atto.swing.Tests").CombinedOutput()
	if err != nil {
		t.Fatalf("Java core tests: %v\n%s", err, out)
	}
	t.Log(string(out))
}

func TestSwingNativeProtocol(t *testing.T) {
	jar, _ := buildJava(t, false)
	binary := filepath.Join(t.TempDir(), "atto")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", binary, "../../cmd/atto").CombinedOutput(); err != nil {
		t.Fatalf("build atto: %v\n%s", err, out)
	}
	for _, transport := range []string{"stdio", "unix", "ws", "ws-token"} {
		t.Run(transport, func(t *testing.T) {
			if transport == "unix" && runtime.GOOS == "windows" {
				t.Skip("Unix listeners unavailable on Windows")
			}
			dir := t.TempDir()
			t.Setenv("ATTO_DIR", dir)
			model := providertest.New(t,
				providertest.Reply{Text: "Swing answer", Words: 4, Delay: 100 * time.Millisecond},
				providertest.Reply{Text: "Swing answer after steer"},
				providertest.Reply{Command: "echo hosted-start; sleep 2; echo hosted-end", Description: "Swing hosted interrupt"},
				providertest.Reply{Text: "Swing command finished", Words: 3, Delay: 100 * time.Millisecond})
			model.Install(t, dir)
			if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"model":"fake/m","daemon":false}`), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			env := append(os.Environ(), "ATTO_NO_DAEMON=1", "ATTO_SESSION_ID=", "ATTO_AGENT=", "HOME="+t.TempDir(), "USERPROFILE="+t.TempDir())

			// Wire inventory fixtures: one open and one closed agent under a saved parent.
			parent := session.New(t.TempDir())
			parent.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "Inventory parent"}})
			parent.Close()
			for _, name := range []string{"inventory-agent", "closed-agent"} {
				worker := session.NewManaged(t.TempDir(), func(id string) session.AgentMeta {
					return session.AgentMeta{Version: 1, ParentSessionID: &parent.ID, RootSessionID: parent.ID, Depth: 1, Path: "/root/" + name, Name: name, Role: "review", Origin: session.OriginAgent, Project: "/inventory-project", SpawnedBy: &session.SpawnedBy{Session: &parent.ID, Model: "fake/m", Effort: "high", Turn: 3, ToolCallID: "inventory-call"}}
				})
				worker.Append(session.Entry{Type: session.TypeName, Name: name})
				worker.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "Inventory first task"}})
				worker.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: "Inventory last report"}})
				worker.Close()
				if err := agentstate.Save(agentstate.State{Session: worker.ID, Parent: parent.ID, Name: name, Cwd: t.TempDir(), Task: "Inventory first task", Preset: "review", Model: "fake/m", Branch: "atto/inventory", Origin: session.OriginAgent, Project: "/inventory-project", SpawnedBy: &session.SpawnedBy{Session: &parent.ID, Model: "fake/m", Effort: "high", Turn: 3, ToolCallID: "inventory-call"}, Created: time.Now()}); err != nil {
					t.Fatal(err)
				}
				if name == "closed-agent" {
					dst, err := session.Archive(worker.Path)
					if err != nil {
						t.Fatal(err)
					}
					if err := agentstate.MarkClosed(worker.ID, dst); err != nil {
						t.Fatal(err)
					}
				}
			}
			paging := session.New(t.TempDir())
			for i := range 350 {
				paging.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: fmt.Sprintf("paging-%03d", i) + strings.Repeat(" padding", 1000)}})
			}
			paging.Close()
			args := []string{"-Djava.awt.headless=true", "-jar", jar, "--selftest", "--atto", binary, "--in-process", "--cwd", t.TempDir(), paging.ID}
			if transport != "stdio" {
				address, token := startListener(t, ctx, binary, env, transport)
				args = append(args, "--connect", address)
				if token != "" {
					args = append(args, "--token", token)
				}
			}
			cmd := exec.CommandContext(ctx, "java", args...)
			cmd.Env = env
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("Swing self-test: %v\nstdout: %s\nstderr: %s", err, &stdout, &stderr)
			}
			var result struct {
				SelfTest        string `json:"selftest"`
				PagingItems     int    `json:"pagingItems"`
				InventoryAgents int    `json:"inventoryAgents"`
				ID              string `json:"threadId"`
			}
			if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &result); err != nil || result.SelfTest != "passed" || result.PagingItems != 350 || result.InventoryAgents != 2 {
				t.Fatalf("self-test result: %s (%v)", &stdout, err)
			}
			path, err := session.Find(result.ID)
			if err != nil {
				t.Fatal(err)
			}
			_, entries, err := session.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			var user, answer, name, shell, modelSaved, queued bool
			for _, entry := range entries {
				if entry.Message != nil {
					user = user || strings.Contains(entry.Message.Content, "hello swing")
					answer = answer || strings.Contains(entry.Message.Content, "Swing answer")
				}
				name = name || entry.Type == session.TypeName
				modelSaved = modelSaved || entry.Type == session.TypeModel
				queued = queued || entry.Message != nil && entry.Message.Content == "queued executed"
				shell = shell || entry.Type == session.TypeBashExecution
			}
			if !user || !answer || !name || !shell || !modelSaved || runtime.GOOS != "windows" && !queued {
				t.Fatalf("session file missing persisted input/answer/name/shell: %v %v %v %v", user, answer, name, shell)
			}
			t.Log(stdout.String())
		})
	}
}
func startListener(t *testing.T, ctx context.Context, binary string, env []string, transport string) (string, string) {
	t.Helper()
	address := "ws://127.0.0.1:0"
	if transport == "unix" {
		address = "unix://" + filepath.Join(os.TempDir(), fmt.Sprintf("atto-swing-%d.sock", time.Now().UnixNano()))
	}
	args := []string{"app-server", "--in-process", "--listen", address}
	if transport == "ws-token" {
		args = []string{"serve", "-listen", "127.0.0.1:0"}
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = env
	cmd.Dir = t.TempDir()
	var stream io.ReadCloser
	var err error
	if transport == "ws-token" {
		stream, err = cmd.StdoutPipe()
		cmd.Stderr = io.Discard
	} else {
		stream, err = cmd.StderrPipe()
		cmd.Stdout = io.Discard
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		_ = stream.Close()
	})
	lines := make(chan string, 1)
	go func() {
		scan := bufio.NewScanner(stream)
		for scan.Scan() {
			line := strings.TrimSpace(scan.Text())
			if transport == "ws-token" {
				if link, ok := strings.CutPrefix(line, "web:"); ok {
					lines <- strings.TrimSpace(link)
					return
				}
			} else if _, rest, ok := strings.Cut(line, "listening on "); ok {
				lines <- strings.TrimSpace(rest)
				return
			}
		}
	}()
	select {
	case line := <-lines:
		if transport == "ws-token" {
			base, token, ok := strings.Cut(line, "/#token=")
			if !ok {
				t.Fatalf("serve link: %s", line)
			}
			return strings.Replace(base, "http://", "ws://", 1) + "/ws", token
		}
		return line, ""
	case <-ctx.Done():
		t.Fatal("listener did not start")
		return "", ""
	}
}

// The GUI suite is opt-in because CI may have a JDK but no display server.
// It uses the same native server/provider as the headless integration test.
func TestSwingScreenshots(t *testing.T) {
	if os.Getenv("ATTO_SWING_SCREENSHOTS") != "1" {
		t.Skip("set ATTO_SWING_SCREENSHOTS=1 on a desktop to capture the GUI")
	}
	jar, _ := buildJava(t, false)
	binary := filepath.Join(t.TempDir(), "atto")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", binary, "../../cmd/atto").CombinedOutput(); err != nil {
		t.Fatalf("build atto: %v\n%s", err, out)
	}
	dir := t.TempDir()
	t.Setenv("ATTO_DIR", dir)
	model := providertest.New(t,
		providertest.Reply{Reasoning: "I will inspect the project before proposing a small, testable change.", Command: "printf 'src/cli.go\\nsrc/config.go\\nREADME.md\\n3 tests passed\\n'", Description: "Inspect the workspace", Prompt: 12400, Completion: 96, Cached: 11200},
		providertest.Reply{Text: "## A focused plan\n\nThe workspace is ready. I recommend **three small changes** before shipping:\n\n- Keep configuration loading in one place.\n- Add a regression test for the empty path.\n- Document the default flags in `README.md`.\n\n```go\nfunc Open(path string) error {\n    if path == \"\" {\n        return errors.New(\"path is required\")\n    }\n    return loadConfig(path)\n}\n```\n\nYou can review the [Go error-handling guide](https://go.dev/blog/error-handling-and-go) for the rationale.", Words: 12, Delay: 150 * time.Millisecond, Prompt: 12600, Completion: 240, Cached: 11200})
	model.Install(t, dir)
	// Give the model realistic display metadata while keeping every request local.
	modelsPath := filepath.Join(dir, "models.json")
	data, err := os.ReadFile(modelsPath)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.ReplaceAll(data, []byte(`"id":"m"`), []byte(`"id":"m","name":"Atlas Local","reasoning":true,"efforts":["low","medium","high"]`))
	if err := os.WriteFile(modelsPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"model":"fake/m","daemon":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ext := filepath.Join(dir, "extensions")
	if err := os.MkdirAll(ext, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ext, "desktop-review.ts"), []byte(`export default (atto: any) => {
 atto.registerCommand("desktop-review", {description: "Review a change with native prompts", handler: async (_: string, ctx: any) => {
  const choice = await ctx.ui.select("Review strategy", ["Small, focused commits", "One complete change", "Explore alternatives"]);
  const approved = await ctx.ui.confirm("Run the project checks before committing?");
  const note = await ctx.ui.input("Review note");
  ctx.ui.notify("Review saved: " + choice + " · " + approved + " · " + note);
 }});
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(t.TempDir(), "atlas")
	if err := os.MkdirAll(filepath.Join(workspace, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"README.md", "src/cli.go", "src/config.go"} {
		if err := os.WriteFile(filepath.Join(workspace, file), []byte("// screenshot workspace\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script, err := filepath.Abs("docs/screenshot-script.json")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "java", "-Duser.home="+t.TempDir(), "-jar", jar, "--atto", binary, "--in-process", "--cwd", workspace, "--screenshot-script", script)
	cmd.Env = append(os.Environ(), "ATTO_NO_DAEMON=1", "ATTO_SESSION_ID=", "ATTO_AGENT=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("GUI capture: %v\n%s", err, out)
	}
	t.Log(string(out))
}

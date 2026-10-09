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
			args := []string{"-Djava.awt.headless=true", "-jar", jar, "--selftest", "--atto", binary, "--in-process", "--cwd", t.TempDir()}
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
				SelfTest string `json:"selftest"`
				ID       string `json:"threadId"`
			}
			if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &result); err != nil || result.SelfTest != "passed" {
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

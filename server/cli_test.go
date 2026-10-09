package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/extensions"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/provider/providertest"
)

type commandConn struct {
	io.ReadCloser
	io.WriteCloser
}

func (c commandConn) Close() error { _ = c.WriteCloser.Close(); return c.ReadCloser.Close() }

func TestServerCLIEndToEnd(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "atto")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if output, err := exec.Command("go", "build", "-o", binary, "../cmd/atto").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	for _, transport := range []string{"app-server", "app-server-ws", "app-server-unix", "serve"} {
		t.Run(transport, func(t *testing.T) {
			if transport == "app-server-unix" && runtime.GOOS == "windows" {
				t.Skip("Unix listener")
			}
			dir := t.TempDir()
			t.Setenv("ATTO_DIR", dir)
			model := providertest.New(t, providertest.Reply{Text: "CLI answer"})
			model.Install(t, dir)
			args := []string{transport}
			socket := filepath.Join(os.TempDir(), "atto-cli-"+newInstanceID()+".sock")
			if transport == "app-server-ws" {
				args = []string{"app-server", "--listen", "ws://127.0.0.1:0"}
			}
			if transport == "app-server-unix" {
				args = []string{"app-server", "--listen", "unix://" + socket}
			}
			if transport == "serve" {
				args = append(args, "-listen", "127.0.0.1:0")
			}
			cmd := exec.Command(binary, args...)
			cmd.Dir = t.TempDir()
			cmd.Env = append(os.Environ(), config.EnvAgent+"=", "ATTO_SESSION_ID=", "HOME="+t.TempDir(), "USERPROFILE="+t.TempDir())
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			var diagnostics io.ReadCloser
			if transport == "app-server-ws" || transport == "app-server-unix" {
				cmd.Stderr = nil
				diagnostics, err = cmd.StderrPipe()
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			t.Cleanup(func() {
				_ = stdin.Close()
				if transport != "app-server" {
					_ = cmd.Process.Signal(os.Interrupt)
				}
				select {
				case <-exited:
				case <-time.After(5 * time.Second):
					_ = cmd.Process.Kill()
					<-exited
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var invoke func(string, any, any) error
			var client *Client
			if transport == "app-server" {
				client = NewClient(commandConn{stdout, stdin})
				defer client.Close()
				invoke = func(method string, p, out any) error { return client.Call(ctx, method, p, out) }
			} else if transport == "app-server-ws" || transport == "app-server-unix" {
				lines := make(chan string, 1)
				go func() {
					sc := bufio.NewScanner(diagnostics)
					for sc.Scan() {
						if strings.Contains(sc.Text(), "listening on ") {
							lines <- strings.TrimSpace(strings.SplitN(sc.Text(), "listening on ", 2)[1])
							return
						}
					}
				}()
				var address string
				select {
				case address = <-lines:
				case <-ctx.Done():
					t.Fatal("no transport banner")
				}
				if transport == "app-server-unix" {
					conn, err := net.Dial("unix", strings.TrimPrefix(address, "unix://"))
					if err != nil {
						t.Fatal(err)
					}
					client = NewClient(conn)
					defer client.Close()
					invoke = func(method string, p, out any) error { return client.Call(ctx, method, p, out) }
				} else {
					ws := dialWS(t, strings.Replace(address, "ws://", "http://", 1), nil, 101)
					seq := 0
					invoke = func(method string, p, out any) error {
						seq++
						v := ws.rpc(t, seq, method, p)
						if out == nil {
							return nil
						}
						b, _ := json.Marshal(v)
						return json.Unmarshal(b, out)
					}
				}
			} else {
				links := make(chan string, 1)
				go func() {
					sc := bufio.NewScanner(stdout)
					for sc.Scan() {
						line := strings.TrimSpace(sc.Text())
						if link, ok := strings.CutPrefix(line, "web:"); ok {
							links <- strings.TrimSpace(link)
							return
						}
					}
				}()
				var link string
				select {
				case link = <-links:
				case <-ctx.Done():
					t.Fatal("no serve banner")
				}
				base, token, _ := strings.Cut(link, "/#token=")
				invoke = func(method string, p, out any) error {
					body, _ := json.Marshal(map[string]any{"id": 1, "method": method, "params": p})
					req, _ := http.NewRequestWithContext(ctx, "POST", base+"/rpc", bytes.NewReader(body))
					req.Header.Set("Authorization", "Bearer "+token)
					resp, err := http.DefaultClient.Do(req)
					if err != nil {
						return err
					}
					defer resp.Body.Close()
					var reply rpcReply
					if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
						return err
					}
					if reply.Error != nil {
						return reply.Error
					}
					if out != nil {
						return json.Unmarshal(reply.Result, out)
					}
					return nil
				}
			}
			var init map[string]any
			if err := invoke("initialize", map[string]any{"protocolVersions": []int{2}, "clientInfo": map[string]string{"name": "test"}}, &init); err != nil {
				t.Fatal(err)
			}
			if init["protocolVersion"] != float64(2) || init["serverInstanceId"] == "" {
				t.Fatalf("initialize: %v", init)
			}
			var thread ThreadInfo
			if err := invoke("thread/start", nil, &thread); err != nil {
				t.Fatal(err)
			}
			if err := invoke("turn/start", map[string]any{"threadId": thread.ID, "input": "hello from CLI"}, nil); err != nil {
				t.Fatal(err)
			}
			for {
				if err := invoke("thread/read", map[string]any{"threadId": thread.ID}, &thread); err != nil {
					t.Fatal(err)
				}
				if !thread.Busy {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("CLI turn did not finish")
				case <-time.After(5 * time.Millisecond):
				}
			}
			found := false
			for _, item := range thread.Items {
				found = found || item.Type == ItemAgent && item.Text == "CLI answer"
			}
			if !found || len(model.Requests()) != 1 {
				t.Fatalf("CLI transcript: %+v", thread.Items)
			}
			if transport == "app-server" && runtime.GOOS != "windows" {
				// The actual executable hosts its shell; a user interrupt detaches
				// the command rather than killing it or spawning a replacement.
				model.SetScript(providertest.Reply{Command: "echo hosted-start; sleep 1; echo hosted-end", Description: "Hosted interrupt"})
				if err := invoke("turn/start", map[string]any{"threadId": thread.ID, "input": "hosted command"}, nil); err != nil {
					t.Fatal(err)
				}
				for {
					select {
					case n := <-client.Events():
						if n.Method == "item/delta" && strings.Contains(string(n.Params), "hosted-start") {
							goto interrupt
						}
					case <-ctx.Done():
						t.Fatal("hosted command did not stream")
					}
				}
			interrupt:
				if err := invoke("turn/interrupt", map[string]any{"threadId": thread.ID, "mode": "cancel"}, nil); err != nil {
					t.Fatal(err)
				}
				for {
					if err := invoke("thread/read", map[string]any{"threadId": thread.ID}, &thread); err != nil {
						t.Fatal(err)
					}
					if !thread.Busy {
						break
					}
					if ctx.Err() != nil {
						t.Fatal("interrupt did not finish")
					}
					time.Sleep(5 * time.Millisecond)
				}
				var detached Item
				for _, item := range thread.Items {
					if item.Job > 0 {
						detached = item
					}
				}
				if detached.Job == 0 || detached.Background != "interrupt" || detached.Canceled {
					t.Fatalf("interrupt result: %+v", detached)
				}
				job, err := jobs.Get(thread.ID, detached.Job)
				if err != nil || !job.QuietExit {
					t.Fatalf("detached job: %+v %v", job, err)
				}
				job, why, err := jobs.Wait(thread.ID, detached.Job, 5*time.Second)
				if err != nil || why != "done" || job.Status != jobs.Exited {
					t.Fatalf("job completion: %+v %s %v", job, why, err)
				}
				time.Sleep(600 * time.Millisecond)
				if len(model.Requests()) != 1 {
					t.Fatal("quiet interrupt exit started a model turn")
				}
			}
			if err := invoke("thread/close", map[string]any{"threadId": thread.ID}, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPythonExample(t *testing.T) {
	if !extensions.Supported {
		t.Skip("requires the JS extension engine")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	binary := filepath.Join(t.TempDir(), "atto")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if output, err := exec.Command("go", "build", "-o", binary, "../cmd/atto").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	dir := t.TempDir()
	t.Setenv("ATTO_DIR", dir)
	t.Setenv("ATTO_NO_DAEMON", "1")
	model := providertest.New(t, providertest.Reply{Text: "Python streamed answer", Words: 3, Delay: 5 * time.Millisecond})
	model.Install(t, dir)
	ext := filepath.Join(dir, "extensions")
	if err := os.MkdirAll(ext, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ext, "prompt.ts"), []byte(`export default (atto: any) => {
 atto.on("user_prompt", async (e: any, ctx: any) => {
  const answer = await ctx.ui.input("Example prompt");
  return answer;
 });
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("../examples/clients/stdio.py")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, script, "--atto", binary, "--in-process", "--model", "fake/m", "Hello from Python")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), config.EnvAgent+"=", "ATTO_SESSION_ID=", "HOME="+t.TempDir(), "USERPROFILE="+t.TempDir())
	cmd.Stdin = strings.NewReader("Python prompt answer\n")
	var out, diagnostics bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &diagnostics
	if err := cmd.Run(); err != nil {
		t.Fatalf("Python: %v\nstdout: %s\nstderr: %s", err, &out, &diagnostics)
	}
	if strings.ReplaceAll(out.String(), "\r\n", "\n") != "Python streamed answer\n" || !strings.Contains(diagnostics.String(), "Example prompt") || len(model.Requests()) != 1 || !strings.Contains(model.Requests()[0], "Python prompt answer") {
		t.Fatalf("stdout: %s\nstderr: %s\nrequests: %d", &out, &diagnostics, len(model.Requests()))
	}
}

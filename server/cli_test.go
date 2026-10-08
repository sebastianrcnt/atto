package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
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
	for _, transport := range []string{"app-server", "serve"} {
		t.Run(transport, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("ATTO_DIR", dir)
			model := providertest.New(t, providertest.Reply{Text: "CLI answer"})
			model.Install(t, dir)
			args := []string{transport}
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
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			t.Cleanup(func() {
				_ = stdin.Close()
				if transport == "serve" {
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

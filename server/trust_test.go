package server

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/extensions"
	"github.com/sebastianrcnt/atto/hooks/hooktest"
	"github.com/sebastianrcnt/atto/trust"
)

func TestStdioAndConnectionLeaveUnapprovedProjectCodeOffAndWarn(t *testing.T) {
	for _, transport := range []string{"stdio", "connection"} {
		t.Run(transport, func(t *testing.T) {
			work := setup(t)
			userLog, projectLog := filepath.Join(work, "user.log"), filepath.Join(work, "project.log")
			for path, command := range map[string]string{
				config.SettingsPath():            hooktest.LogStdinNoNewline(userLog),
				config.ProjectSettingsPath(work): hooktest.LogStdinNoNewline(projectLog),
			} {
				data, _ := json.Marshal(config.Settings{Hooks: map[string][]config.HookMatcher{"SessionStart": {{Hooks: []config.HookSpec{{Type: "command", Command: command}}}}}})
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(config.ProjectMCPPath(work), []byte(`{"mcpServers":{"repo":{"command":"project-server"}}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			ext := filepath.Join(config.ProjectExtensionsDir(work), "deploy.ts")
			if err := os.MkdirAll(filepath.Dir(ext), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(ext, []byte(`export default (atto: any) => atto.fs.writeFile("extension-ran", "bad")`), 0o600); err != nil {
				t.Fatal(err)
			}
			errOut, err := os.CreateTemp(t.TempDir(), "warnings")
			if err != nil {
				t.Fatal(err)
			}
			previous := os.Stderr
			os.Stderr = errOut
			t.Cleanup(func() { os.Stderr = previous; errOut.Close() })
			s := New("test", work)
			t.Cleanup(s.Close)
			request := `{"jsonrpc":"2.0","id":1,"method":"thread/start","params":{}}`
			if transport == "stdio" {
				var out bytes.Buffer
				if err := s.ServeStdio(context.Background(), strings.NewReader(request+"\n"), &out); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(out.String(), `"threadId"`) {
					t.Fatalf("thread did not start: %s", out.String())
				}
			} else {
				c := Connect(context.Background(), s)
				defer c.Close()
				var started map[string]any
				if err := c.Call(context.Background(), "thread/start", map[string]any{}, &started); err != nil || started["threadId"] == nil {
					t.Fatalf("thread did not start: %v %v", started, err)
				}
			}
			for _, path := range []string{projectLog, filepath.Join(work, "extension-ran")} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("unapproved project code ran: %s", path)
				}
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				data, err := os.ReadFile(userLog)
				if err == nil && len(data) > 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("user hook should run without approval: %q, %v", data, err)
				}
				time.Sleep(time.Millisecond)
			}
			warnings, _ := os.ReadFile(errOut.Name())
			items, err := trust.Discover(work)
			if err != nil || len(items) != func() int {
				if !extensions.Supported {
					return 2
				}
				return 3
			}() {
				t.Fatalf("trust items %+v: %v", items, err)
			}
			for _, in := range items {
				if !strings.Contains(string(warnings), in.Command()) {
					t.Fatalf("%s warning missing %q:\n%s", transport, in.Command(), warnings)
				}
			}
		})
	}
}

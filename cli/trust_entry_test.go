package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/extensions"
	"github.com/sebastianrcnt/atto/hooks/hooktest"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/trust"
)

func TestPrintAndBackgroundLeaveUnapprovedProjectCodeOffAndWarn(t *testing.T) {
	for _, mode := range []string{"print", "background"} {
		t.Run(mode, func(t *testing.T) {
			imageModelServer(t, `["text"]`)
			cwd := t.TempDir()
			t.Chdir(cwd)
			userLog := filepath.Join(cwd, "user.log")
			projectLog := filepath.Join(cwd, "project.log")
			for path, command := range map[string]string{
				config.SettingsPath():           hooktest.LogStdin(userLog),
				config.ProjectSettingsPath(cwd): hooktest.LogStdin(projectLog),
			} {
				data, _ := json.Marshal(config.Settings{Hooks: map[string][]config.HookMatcher{"SessionStart": {{Hooks: []config.HookSpec{{Type: "command", Command: command}}}}}})
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(config.ProjectMCPPath(cwd), []byte(`{"mcpServers":{"repo":{"command":"project-server"}}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			ext := filepath.Join(config.ProjectExtensionsDir(cwd), "deploy.ts")
			if err := os.MkdirAll(filepath.Dir(ext), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(ext, []byte(`export default (atto: any) => atto.fs.writeFile("extension-ran", "bad")`), 0o600); err != nil {
				t.Fatal(err)
			}
			quiet(t)
			errOut, err := os.CreateTemp(t.TempDir(), "warnings")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { errOut.Close() })
			os.Stderr = errOut
			if mode == "print" {
				err = RunPrint(PrintOptions{Prompt: "hi", NoSave: true})
			} else {
				w := session.New(cwd)
				w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "hi"}})
				w.Close()
				err = RunContinue([]string{w.ID}, io.Discard)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{projectLog, filepath.Join(cwd, "extension-ran")} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("unapproved project code ran: %s", path)
				}
			}
			if data, err := os.ReadFile(userLog); err != nil || len(data) == 0 {
				t.Fatalf("user hooks should run without approval: %q, %v", data, err)
			}
			warnings, _ := os.ReadFile(errOut.Name())
			items, err := trust.Discover(cwd)
			if err != nil || len(items) != func() int {
				if !extensions.Supported {
					return 2
				}
				return 3
			}() {
				t.Fatalf("trust items %+v: %v", items, err)
			}
			for _, in := range items {
				if !strings.Contains(string(warnings), in.Command()) || !strings.Contains(string(warnings), "will not run") {
					t.Fatalf("%s warning missing approval command %q:\n%s", mode, in.Command(), warnings)
				}
			}
		})
	}
}

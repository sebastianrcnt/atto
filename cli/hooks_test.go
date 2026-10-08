package cli

import (
	"encoding/json"
	"github.com/sebastianrcnt/atto/config"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/hooks/hooktest"
)

// atto -p runs Stop hooks at the end of each turn (a block continues it)
// and SessionEnd once when the run finishes.
func TestRunPrintStopAndSessionEndHooks(t *testing.T) {
	bodies := imageModelServer(t, `["text"]`)
	cwd := t.TempDir()
	t.Chdir(cwd)
	log := filepath.Join(t.TempDir(), "hooks.log")
	hook := func(command string) []any {
		return []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command}}}}
	}
	raw, _ := json.Marshal(map[string]any{"hooks": map[string]any{
		"Stop":       hook(hooktest.StopOnce("run the tests")),
		"SessionEnd": hook(hooktest.LogStdin(log)),
	}})
	if err := os.MkdirAll(filepath.Join(cwd, ".atto"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".atto", "settings.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	projectHooks, err := config.ProjectHooks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range projectHooks {
		if err := config.SetHookApproval(h, true); err != nil {
			t.Fatal(err)
		}
	}
	quiet(t)
	if err := RunPrint(PrintOptions{Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	b := bodies()
	if len(b) != 2 || !strings.Contains(b[1], "[Stop hook] run the tests") {
		t.Fatalf("Stop should send the model back to work once: %v", b)
	}
	data, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("SessionEnd should run once: %q", data)
	}
	var in map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &in); err != nil {
		t.Fatal(err)
	}
	if in["hook_event_name"] != "SessionEnd" || in["reason"] != "other" || in["session_id"] == "" {
		t.Fatalf("input %v", in)
	}
}

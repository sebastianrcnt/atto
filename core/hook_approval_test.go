package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/hooks/hooktest"
)

func TestSessionHookRunnerFiltersUnapprovedProjectHooks(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	cwd := t.TempDir()
	log := filepath.Join(t.TempDir(), "hook.log")
	command := hooktest.LogStdin(log)
	settings := config.Settings{Hooks: map[string][]config.HookMatcher{"SessionStart": {{Hooks: []config.HookSpec{{Type: "command", Command: command}}}}}}
	writeSettings := func() {
		t.Helper()
		data, err := json.Marshal(settings)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, config.ProjectSettingsPath(cwd), string(data))
	}
	writeSettings()
	runner, sources, err := LoadHooks(cwd)
	if err != nil || runner != nil || len(sources) != 1 {
		t.Fatalf("unapproved hooks should be listed but not runnable: %v, %+v, %v", runner, sources, err)
	}
	hooks, err := config.ProjectHooks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SetHookApproval(hooks[0], true); err != nil {
		t.Fatal(err)
	}
	runner, _, err = LoadHooks(cwd)
	if err != nil || runner == nil {
		t.Fatalf("approved runner %v: %v", runner, err)
	}
	runner.SessionStart(context.Background(), "startup")
	if data, err := os.ReadFile(log); err != nil || len(data) == 0 {
		t.Fatalf("approved hook did not run: %q, %v", data, err)
	}
	settings.Hooks["SessionStart"][0].Hooks[0].Timeout = 3
	writeSettings()
	runner, _, err = LoadHooks(cwd)
	if err != nil || runner != nil {
		t.Fatalf("changed hook remained runnable: %v, %v", runner, err)
	}
}

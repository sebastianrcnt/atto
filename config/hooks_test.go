package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The new events parse like the others, and project hooks follow user hooks.
func TestLoadHooksSessionEndAndNotification(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	cwd := t.TempDir()
	user := `{"hooks":{"SessionEnd":[{"matcher":"clear","hooks":[{"type":"command","command":"u"}]}],
		"Notification":[{"matcher":"idle_prompt","hooks":[{"type":"command","command":"n","timeout":3}]}]}}`
	proj := `{"hooks":{"SessionEnd":[{"hooks":[{"type":"command","command":"p"}]}],"Stop":[{"hooks":[{"type":"http","url":"http://x"}]}]}}`
	if err := os.WriteFile(SettingsPath(), []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cwd, ".atto"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ProjectSettingsPath(cwd), []byte(proj), 0o644); err != nil {
		t.Fatal(err)
	}

	projectHooks, err := ProjectHooks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range projectHooks {
		if err := SetHookApproval(h, true); err != nil {
			t.Fatal(err)
		}
	}
	h, err := LoadHooks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	end := h["SessionEnd"]
	if len(end) != 2 || end[0].Matcher != "clear" || end[0].Hooks[0].Command != "u" || end[1].Hooks[0].Command != "p" {
		t.Fatalf("SessionEnd %+v", end)
	}
	if n := h["Notification"]; len(n) != 1 || n[0].Matcher != "idle_prompt" || n[0].Hooks[0].Timeout != 3 {
		t.Fatalf("Notification %+v", n)
	}
	if len(h["Stop"]) != 1 {
		t.Fatalf("Stop %+v", h["Stop"])
	}
}

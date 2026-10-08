package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHookHashCoversExecutionContext(t *testing.T) {
	h := ProjectHook{Path: "ignored", Event: "PreToolUse", Matcher: "Bash", Spec: HookSpec{Type: "command", Command: "./check.sh", Timeout: 3}}
	changes := []ProjectHook{h, h, h, h, h, h, h}
	changes[0].Event = "PostToolUse"
	changes[1].Matcher = "Read"
	changes[2].Spec.Command = "./changed.sh"
	changes[3].Spec.Timeout = 4
	changes[4].Spec.Type = "http"
	changes[5].Spec.URL = "http://example.invalid"
	changes[6].Spec.Headers = map[string]string{"Authorization": "secret"}
	for _, changed := range changes {
		if changed.Hash() == h.Hash() {
			t.Fatalf("hash ignores content change: %+v", changed)
		}
	}
	h.Path = "another project"
	if h.Hash() != (ProjectHook{Event: "PreToolUse", Matcher: "Bash", Spec: h.Spec}).Hash() {
		t.Fatal("source path belongs in the storage key, not the content hash")
	}
	a := ProjectHook{Event: "Stop", Spec: HookSpec{Type: "http", Headers: map[string]string{"A": "1", "B": "2"}}}
	b := ProjectHook{Event: "Stop", Spec: HookSpec{Type: "http", Headers: map[string]string{"B": "2", "A": "1"}}}
	if a.Hash() != b.Hash() {
		t.Fatal("header map order changes the hash")
	}
}

func TestProjectHookStorageFilteringAndChanges(t *testing.T) {
	t.Setenv(EnvDir, t.TempDir())
	cwd := t.TempDir()
	if err := os.WriteFile(SettingsPath(), []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"user"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(ProjectSettingsPath(cwd)), 0o755); err != nil {
		t.Fatal(err)
	}
	project := `{"hooks":{"Stop":[{"matcher":"*","hooks":[{"type":"command","command":"one"},{"type":"command","command":"two"}]}]}}`
	if err := os.WriteFile(ProjectSettingsPath(cwd), []byte(project), 0o600); err != nil {
		t.Fatal(err)
	}
	hooks, err := ProjectHooks(cwd)
	if err != nil || len(hooks) != 2 {
		t.Fatalf("hooks %+v: %v", hooks, err)
	}
	loaded, err := LoadHooks(cwd)
	if err != nil || len(loaded["Stop"]) != 1 || loaded["Stop"][0].Hooks[0].Command != "user" {
		t.Fatalf("pending project hooks ran or user hooks were filtered: %+v, %v", loaded, err)
	}
	if err := SetHookApproval(hooks[0], true); err != nil {
		t.Fatal(err)
	}
	if err := SetHookApproval(hooks[1], false); err != nil {
		t.Fatal(err)
	}
	if HookApprovalOf(hooks[0]) != HookApproved || HookApprovalOf(hooks[1]) != HookDenied {
		t.Fatal("decisions were not persisted")
	}
	loaded, err = LoadHooks(cwd)
	if err != nil || len(loaded["Stop"]) != 2 || len(loaded["Stop"][1].Hooks) != 1 || loaded["Stop"][1].Hooks[0].Command != "one" {
		t.Fatalf("only approved project hooks should follow user hooks: %+v, %v", loaded, err)
	}
	changed := hooks[0]
	changed.Spec.Timeout = 1
	if HookApprovalOf(changed) != HookPending {
		t.Fatal("a changed hook retained approval")
	}
	other := hooks[0]
	other.Path = filepath.Join(t.TempDir(), ".atto", "settings.json")
	if HookApprovalOf(other) != HookPending {
		t.Fatal("approval leaked to another project")
	}
	if err := RevokeHook(hooks[0]); err != nil {
		t.Fatal(err)
	}
	if HookApprovalOf(hooks[0]) != HookPending {
		t.Fatal("revocation did not forget approval")
	}
}

func TestUserSettingsAreNotProjectHooks(t *testing.T) {
	root := t.TempDir()
	t.Setenv(EnvDir, filepath.Join(root, ".atto"))
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SettingsPath(), []byte(`{"hooks":{"Stop":[{"hooks":[{"command":"user"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	hooks, err := ProjectHooks(root)
	if err != nil || len(hooks) != 0 {
		t.Fatalf("user-owned settings were considered repository code: %+v, %v", hooks, err)
	}
	loaded, err := LoadHooks(root)
	if err != nil || len(loaded["Stop"]) != 1 {
		t.Fatalf("the same user settings should load only once, without approval: %+v, %v", loaded, err)
	}
}

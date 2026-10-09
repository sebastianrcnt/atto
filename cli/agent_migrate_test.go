package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/config"
)

func oldLayoutDir(t *testing.T) {
	t.Helper()
	t.Setenv(config.EnvDir, t.TempDir())
	t.Setenv(config.EnvAgent, "")
	t.Setenv(config.EnvLegacyAgent, "")
	t.Setenv("ATTO_SESSION_ID", "")
	state := filepath.Join(config.AgentStateDir(), "pppp0001")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	record := `{"name":"a","parent":"pppp0001","session":"aaaa0001","preset":"general","model":"fake/m","cwd":"/w","task":"t","created":"2026-01-01T00:00:00Z","turns":1,"job":1}`
	if err := os.WriteFile(filepath.Join(state, "a.json"), []byte(record), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "a.turn.json"), []byte(`{"turn":1,"status":"done"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	job := filepath.Join(config.Dir(), "jobs", "pppp0001", "1")
	if err := os.MkdirAll(job, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(job, "job.json"), []byte(`{"kind":"agent","id":1,"session":"pppp0001","status":"exited","exitCode":0,"started":"2026-01-01T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAgentCommandsRefuseTheOldLayoutWithoutATerminal(t *testing.T) {
	oldLayoutDir(t)
	old := askMigrate
	t.Cleanup(func() { askMigrate = old })
	askMigrate = func(io.Writer, string) bool { return false } // what the real one answers off a terminal
	for _, args := range [][]string{{"list"}, {"report", "a", "-session", "pppp0001"}, {"spawn", "x", "task"}, {"wait"}} {
		_, err := runAgent(t, args...)
		if !errors.Is(err, agentstate.ErrNeedsMigration) || !strings.Contains(err.Error(), "run `atto agent migrate`") {
			t.Fatalf("%v: %v", args, err)
		}
	}
	// roles reads no agent data.
	if _, err := runAgent(t, "roles"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := agentstate.ReadMarker(); ok {
		t.Fatal("a refused command wrote the marker")
	}
	// In a model's shell nobody is asked, and migrate itself is refused.
	askMigrate = func(io.Writer, string) bool { t.Fatal("asked inside a model shell"); return true }
	t.Setenv("ATTO_SESSION_ID", "pppp0001")
	if _, err := runAgent(t, "list"); !errors.Is(err, agentstate.ErrNeedsMigration) {
		t.Fatalf("inside atto: %v", err)
	}
	if _, err := runAgent(t, "migrate"); err == nil || !strings.Contains(err.Error(), "model shell") {
		t.Fatalf("migrate inside atto: %v", err)
	}
}

func TestFirstAgentCommandOffersToMigrate(t *testing.T) {
	oldLayoutDir(t)
	var prompt string
	old := askMigrate
	askMigrate = func(_ io.Writer, why string) bool { prompt = why; return true }
	t.Cleanup(func() { askMigrate = old })
	out, err := runAgent(t, "list")
	if err != nil || !strings.Contains(out, "migrated agent data") || !strings.Contains(out, "Backup: ") || !strings.Contains(out, "atto restore -force") {
		t.Fatalf("list after migrating: %q %v", out, err)
	}
	if !strings.Contains(prompt, "pppp0001") {
		t.Fatalf("the question says why: %q", prompt)
	}
	if _, ok, _ := agentstate.ReadMarker(); !ok {
		t.Fatal("no marker")
	}
	if st, err := agentstate.Load("aaaa0001"); err != nil || st.Root != "pppp0001" {
		t.Fatalf("migrated: %+v %v", st, err)
	}
	// Asked once: the next command finds the new layout.
	askMigrate = func(io.Writer, string) bool { t.Fatal("asked twice"); return false }
	if _, err := runAgent(t, "list", "-session", "pppp0001"); err != nil {
		t.Fatal(err)
	}
}

func TestAgentMigrateCommandAndNewerData(t *testing.T) {
	oldLayoutDir(t)
	out, err := runAgent(t, "migrate")
	if err != nil || !strings.Contains(out, "migrated agent data: 1 agents") || !strings.Contains(out, "must now run this atto or a newer one") {
		t.Fatalf("migrate: %q %v", out, err)
	}
	if out, err := runAgent(t, "migrate"); err != nil || !strings.Contains(out, "already has the current format") {
		t.Fatalf("again: %q %v", out, err)
	}
	// Data from a newer atto: every agent command refuses.
	if err := os.WriteFile(filepath.Join(config.AgentStateDir(), ".format"), []byte(`{"version":9}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"list"}, {"spawn", "x", "task"}, {"migrate"}} {
		if _, err := runAgent(t, args...); !errors.Is(err, agentstate.ErrNewerFormat) {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

package agent

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/shell"
)

func TestSystemPromptAgents(t *testing.T) {
	repo, cwd, dir := project(t)
	write(t, filepath.Join(repo, ".git"), "")
	sh := shell.Default()
	start := time.Date(2026, 1, 2, 3, 0, 0, 0, time.Local)
	a := &Agent{Cwd: cwd, Shell: sh}

	// Agents are always available: the prompt names the command, when to
	// use it and the presets, whatever settings.json says.
	write(t, filepath.Join(repo, ".atto", "agents", "review.md"), "---\ndescription: Reviews diffs\n---\nBe strict.")
	write(t, filepath.Join(dir, "settings.json"), `{"agents":{"enabled":false}}`)
	a.SetStart(start)
	for _, want := range []string{"on your own judgement", "do not for a small task", "atto agent spawn NAME", "atto agent send NAME", "FINAL_ANSWER", "- general: ", "- review: Reviews diffs"} {
		if !strings.Contains(a.system, want) {
			t.Fatalf("missing %q:\n%s", want, a.system)
		}
	}
	if strings.Contains(a.system, "Be strict.") {
		t.Fatal("a preset's body is for the agent only")
	}

	// An agent's prompt says what and where it is and has its role's
	// instructions, and it may start agents of its own at any depth.
	off := a.system
	a.Worker = &Worker{Name: "rev", Preset: "review", Instructions: "Be strict.", ID: "a1b2c3d4", Path: "/root/rev", Parent: "/root", ParentID: "p0p0p0p0"}
	a.SetStart(start)
	if !strings.Contains(a.system, "You are agent /root/rev (session a1b2c3d4)") || !strings.Contains(a.system, "/root (session p0p0p0p0) started you with role review") ||
		!strings.Contains(a.system, "Be strict.") || !strings.Contains(a.system, "atto agent spawn NAME") {
		t.Fatalf("agent prompt:\n%s", a.system)
	}
	if len(a.system) <= len(off) {
		t.Fatal("prompt unchanged")
	}
}

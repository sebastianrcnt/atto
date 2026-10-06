package agent

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/shell"
)

func TestSystemPromptSubagents(t *testing.T) {
	repo, cwd, dir := project(t)
	write(t, filepath.Join(repo, ".git"), "")
	sh := shell.Default()
	start := time.Date(2026, 1, 2, 3, 0, 0, 0, time.Local)
	a := &Agent{Cwd: cwd, Shell: sh}

	// Off (the default), the prompt says nothing about subagents.
	write(t, filepath.Join(repo, ".atto", "agents", "review.md"), "---\ndescription: Reviews diffs\n---\nBe strict.")
	a.SetStart(start)
	if strings.Contains(a.system, "atto agent") || strings.Contains(a.system, "Roles (-role") {
		t.Fatalf("subagents are off:\n%s", a.system)
	}
	off := a.system

	// On, it names the command, the rule and the presets.
	write(t, filepath.Join(dir, "settings.json"), `{"subagents":{"enabled":true}}`)
	a.SetStart(start)
	for _, want := range []string{"only when the user explicitly asks", "atto agent spawn NAME", "atto agent send NAME", "FINAL_ANSWER", "- general: ", "- review: Reviews diffs"} {
		if !strings.Contains(a.system, want) {
			t.Fatalf("missing %q:\n%s", want, a.system)
		}
	}
	if strings.Contains(a.system, "Be strict.") {
		t.Fatal("a preset's body is for the subagent only")
	}

	// An agent's prompt says what and where it is and has its role's
	// instructions; it offers agents of its own only when it may start them.
	a.Subagent = &Subagent{Name: "rev", Preset: "review", Instructions: "Be strict.", Path: "/root/rev", Parent: "/root"}
	a.SetStart(start)
	if !strings.Contains(a.system, "You are agent /root/rev") || !strings.Contains(a.system, "/root started you with role review") ||
		!strings.Contains(a.system, "Be strict.") || strings.Contains(a.system, "atto agent spawn") {
		t.Fatalf("agent prompt:\n%s", a.system)
	}
	a.Subagent.CanSpawn = true
	a.SetStart(start)
	if !strings.Contains(a.system, "You are agent /root/rev") || !strings.Contains(a.system, "atto agent spawn NAME") {
		t.Fatalf("agent that may spawn:\n%s", a.system)
	}
	if len(a.system) <= len(off) {
		t.Fatal("prompt unchanged")
	}
}

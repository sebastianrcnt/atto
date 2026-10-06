package agent

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/shell"
)

func TestSystemPromptSkills(t *testing.T) {
	repo, cwd, dir := project(t)
	home := filepath.Join(filepath.Dir(repo), "home")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	write(t, filepath.Join(repo, ".git"), "")
	write(t, filepath.Join(dir, "settings.json"), `{"skills":{"disabled":["atto-extensions"]}}`)

	sh := shell.Default()
	start := time.Date(2026, 1, 2, 3, 0, 0, 0, time.Local)
	a := &Agent{Cwd: cwd, Shell: sh}

	// Without skills the prompt is exactly the prompt of a build that knows
	// nothing about skills.
	a.SetStart(start)
	if want := systemPrompt(cwd, sh, start, nil); a.system != want || strings.Contains(a.system, "available_skills") {
		t.Fatalf("no skills changed the prompt:\n%s", a.system)
	}
	base := a.system

	write(t, filepath.Join(dir, "skills", "pdf", "SKILL.md"), "---\nname: pdf\ndescription: Work with PDFs\n---\nsteps")
	write(t, filepath.Join(repo, ".atto", "skills", "hidden", "SKILL.md"), "---\nname: hidden\ndescription: Only by command\ndisable-model-invocation: true\n---\nx")
	a.SetStart(start)
	loc := filepath.Join(dir, "skills", "pdf", "SKILL.md")
	if !strings.HasPrefix(a.system, base) || !strings.Contains(a.system, "<location>"+loc+"</location>") || strings.Contains(a.system, "Only by command") {
		t.Fatalf("prompt:\n%s", a.system)
	}
	if got, _ := a.Skills(); len(got) != 2 { // the hidden one stays available for /skill:
		t.Fatalf("snapshot: %+v", got)
	}

	// A skill added later does not change the running session's prompt.
	before := a.system
	write(t, filepath.Join(dir, "skills", "late", "SKILL.md"), "---\nname: late\ndescription: Late\n---\n")
	if a.system != before {
		t.Fatal("prompt changed mid-session")
	}
}

func TestBuiltinSkillsInPrompt(t *testing.T) {
	repo, cwd, dir := project(t)
	t.Setenv("HOME", filepath.Join(filepath.Dir(repo), "home"))
	t.Setenv("USERPROFILE", filepath.Join(filepath.Dir(repo), "home"))
	write(t, filepath.Join(repo, ".git"), "")
	start := time.Date(2026, 1, 2, 3, 0, 0, 0, time.Local)
	a := &Agent{Cwd: cwd, Shell: shell.Default()}

	a.SetStart(start)
	loc := filepath.Join(dir, "cache", "skills")
	if !strings.Contains(a.system, "<name>atto-extensions</name>") || !strings.Contains(a.system, "<location>"+loc) {
		t.Fatalf("builtin skill missing:\n%s", a.system)
	}
	first := a.system
	a.SetStart(start)
	if a.system != first {
		t.Fatal("prompt is not stable")
	}
	if sk, _ := a.Skills(); len(sk) == 0 || sk[0].Source != "builtin" {
		t.Fatalf("skills: %+v", sk)
	}

	// A user skill of the same name replaces it.
	write(t, filepath.Join(dir, "skills", "atto-extensions", "SKILL.md"), "---\nname: atto-extensions\ndescription: My own\n---\nx")
	a.SetStart(start)
	if !strings.Contains(a.system, "My own") || strings.Contains(a.system, loc) {
		t.Fatalf("override:\n%s", a.system)
	}

	// settings.json turns it off.
	write(t, filepath.Join(dir, "skills", "atto-extensions", "SKILL.md"), "---\nname: x\n---\n")
	write(t, filepath.Join(dir, "settings.json"), `{"skills":{"disabled":["atto-extensions"]}}`)
	a.SetStart(start)
	if strings.Contains(a.system, "available_skills") {
		t.Fatalf("disabled:\n%s", a.system)
	}
}

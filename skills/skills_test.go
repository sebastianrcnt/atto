package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func names(ss []Skill) string {
	var n []string
	for _, s := range ss {
		n = append(n, s.Name)
	}
	return strings.Join(n, ",")
}

func TestParseFrontmatter(t *testing.T) {
	src := string(rune(0xFEFF)) + "---\r\nname: pdf-tools\r\ndescription: \"Work with: PDFs\"\r\nother: 'it''s'\r\nlong: >\r\n  folded\r\n  text\r\nblock: |\r\n  a\r\n  b\r\ndisable-model-invocation: true # why\r\nnested:\r\n  - x\r\n  - y\r\n---\r\n\r\n# Body\r\n"
	fm, body, err := ParseFrontmatter(src)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"name": "pdf-tools", "description": "Work with: PDFs", "other": "it's", "long": "folded text", "block": "a\nb", "disable-model-invocation": "true"}
	for k, v := range want {
		if fm[k] != v {
			t.Errorf("%s = %q, want %q", k, fm[k], v)
		}
	}
	if body != "# Body" {
		t.Errorf("body %q", body)
	}
	if fm, body, _ := ParseFrontmatter("no front matter"); len(fm) != 0 || body != "no front matter" {
		t.Errorf("plain file: %v %q", fm, body)
	}
	if _, _, err := ParseFrontmatter("---\nnot yaml\n---\nx"); err == nil {
		t.Error("expected an error for a line without a key")
	}
}

func TestDiscoveryRules(t *testing.T) {
	tmp := t.TempDir()
	user := filepath.Join(tmp, "user")
	proj := filepath.Join(tmp, "proj", ".atto", "skills")
	home := filepath.Join(tmp, "home")

	write(t, filepath.Join(user, "alpha", "SKILL.md"), "---\nname: alpha\ndescription: The user alpha\n---\nbody")
	// A skill root is not searched further.
	write(t, filepath.Join(user, "alpha", "nested", "SKILL.md"), "---\nname: nested\ndescription: never seen\n---\n")
	// Nested group directory without SKILL.md is recursed into.
	write(t, filepath.Join(user, "group", "beta", "SKILL.md"), "---\ndescription: Name falls back to the directory\n---\n")
	// Loose .md in the root counts if it has a description; deeper ones do not.
	write(t, filepath.Join(user, "loose.md"), "---\nname: loose\ndescription: A loose skill\n---\n")
	write(t, filepath.Join(user, "notes.md"), "just notes")
	write(t, filepath.Join(user, "group", "deep.md"), "---\ndescription: ignored\n---\n")
	// Hidden and node_modules are skipped.
	write(t, filepath.Join(user, ".hidden", "SKILL.md"), "---\ndescription: hidden\n---\n")
	write(t, filepath.Join(user, "node_modules", "x", "SKILL.md"), "---\ndescription: dep\n---\n")
	// Duplicate: the user copy wins.
	write(t, filepath.Join(proj, "alpha", "SKILL.md"), "---\nname: alpha\ndescription: The project alpha\n---\n")
	write(t, filepath.Join(proj, "gamma", "SKILL.md"), "---\nname: gamma\ndescription: Project gamma\n---\n")
	// Other tools' directories are not read, in the home or the project.
	write(t, filepath.Join(home, ".claude", "skills", "delta", "SKILL.md"), "---\nname: delta\ndescription: Claude one\n---\n")
	write(t, filepath.Join(home, ".agents", "skills", "eps", "SKILL.md"), "---\nname: eps\ndescription: Agents one\n---\n")
	write(t, filepath.Join(tmp, "proj", ".agents", "skills", "zeta", "SKILL.md"), "---\nname: zeta\ndescription: Agents one\n---\n")

	root := filepath.Join(tmp, "proj")
	got, warns := Load(Dirs(user, root))
	if n := names(got); n != "alpha,beta,loose,gamma" {
		t.Fatalf("skills = %s", n)
	}
	if got[0].Description != "The user alpha" || got[0].BaseDir != filepath.Join(user, "alpha") {
		t.Errorf("alpha: %+v", got[0])
	}
	var dup bool
	for _, w := range warns {
		dup = dup || strings.Contains(w, `"alpha"`)
	}
	if !dup || len(warns) != 1 {
		t.Errorf("warnings = %v", warns)
	}
}

func TestValidation(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "Bad_Name", "SKILL.md"), "---\nname: Bad_Name\ndescription: d\n---\n")
	write(t, filepath.Join(dir, "x", "SKILL.md"), "---\nname: a--b\ndescription: d\n---\n")
	write(t, filepath.Join(dir, "nodesc", "SKILL.md"), "---\nname: nodesc\n---\n")
	write(t, filepath.Join(dir, "broken", "SKILL.md"), "---\ngarbage\n---\n")
	got, warns := Load([]string{dir})
	// Invalid names still load (pi warns only); missing description and bad YAML do not.
	if n := names(got); n != "Bad_Name,a--b" {
		t.Fatalf("skills = %s", n)
	}
	if len(warns) != 4 { // bad chars; consecutive hyphens; no description; YAML
		t.Fatalf("warnings = %v", warns)
	}
}

func TestFormatForPrompt(t *testing.T) {
	if FormatForPrompt(nil, "bash") != "" {
		t.Fatal("no skills must add nothing")
	}
	hidden := []Skill{{Name: "h", Description: "d", FilePath: "/x", DisableModelInvocation: true}}
	if FormatForPrompt(hidden, "bash") != "" {
		t.Fatal("only hidden skills must add nothing")
	}
	got := FormatForPrompt([]Skill{
		{Name: "a", Description: `Use <b> & "c"`, FilePath: "/s/a/SKILL.md"},
		hidden[0],
	}, "bash")
	want := "\nThe following skills provide specialized instructions for specific tasks.\n" +
		"Use bash to load a skill's file when the task matches its description.\n" +
		"When a skill file references a relative path, resolve it against the skill directory (parent of SKILL.md / dirname of the path) and use that absolute path in tool commands.\n\n" +
		"<available_skills>\n  <skill>\n    <name>a</name>\n    <description>Use &lt;b&gt; &amp; &quot;c&quot;</description>\n    <location>/s/a/SKILL.md</location>\n  </skill>\n</available_skills>\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestExpand(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "s", "SKILL.md"), "---\nname: s\ndescription: d\n---\n\nDo the thing.\n")
	ss, _ := Load([]string{dir})
	got, ok, err := Expand(ss, "/skill:s  fix the bug ")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	want := "<skill name=\"s\" location=\"" + filepath.Join(dir, "s", "SKILL.md") + "\">\nReferences are relative to " + filepath.Join(dir, "s") + ".\n\nDo the thing.\n</skill>\n\nfix the bug"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if got, ok, _ := Expand(ss, "/skill:s"); !ok || strings.HasSuffix(got, "\n\n") || !strings.HasSuffix(got, "</skill>") {
		t.Errorf("no args: %q", got)
	}
	if _, ok, _ := Expand(ss, "/skill:nope x"); ok {
		t.Error("unknown skill must pass through")
	}
}

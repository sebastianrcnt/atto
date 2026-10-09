package agentstate

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

func TestValidName(t *testing.T) {
	for _, ok := range []string{"a", "test-runner", "x2", "9"} {
		if err := ValidName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-a", "A", "a_b", "a b", "../x", "a.json", strings.Repeat("a", 41)} {
		if ValidName(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestPresetsPrecedence(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	root := t.TempDir()
	sub := filepath.Join(root, "pkg")
	write(t, filepath.Join(os.Getenv("ATTO_DIR"), "agents", "review.md"), "---\ndescription: global review\nmodel: fake/m\n---\nglobal body")
	write(t, filepath.Join(os.Getenv("ATTO_DIR"), "agents", "docs.md"), "---\ndescription: |\n  writes\n  docs\neffort: low\n---\nDocs body.")
	write(t, filepath.Join(root, ".atto", "agents", "review.md"), "---\ndescription: project review\n---\nproject body")
	write(t, filepath.Join(sub, ".atto", "agents", "general.md"), "---\nname: general\ndescription: my general\n---\nmine")
	write(t, filepath.Join(root, ".atto", "agents", "Bad.md"), "no frontmatter")

	dirs := Dirs(sub, root)
	if len(dirs) != 3 || !strings.HasSuffix(dirs[2], filepath.Join("pkg", ".atto", "agents")) {
		t.Fatalf("dirs %v", dirs)
	}
	ps, warns := LoadPresets(dirs)
	if len(warns) != 1 || !strings.Contains(warns[0], "Bad.md") {
		t.Fatalf("warnings %v", warns)
	}
	got := map[string]Preset{}
	var names []string
	for _, p := range ps {
		got[p.Name] = p
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "docs,general,review" {
		t.Fatalf("names %v", names)
	}
	if r := got["review"]; r.Description != "project review" || r.Instructions != "project body" || r.Model != "" {
		t.Fatalf("project overrides global: %+v", r)
	}
	if g := got["general"]; g.Instructions != "mine" || g.Path == "" {
		t.Fatalf("a file overrides the built-in preset: %+v", g)
	}
	if d := got["docs"]; d.Effort != "low" || d.Instructions != "Docs body." {
		t.Fatalf("docs %+v", d)
	}
	if !strings.Contains(PromptList(ps), "- docs: writes docs\n") {
		t.Fatalf("prompt list %q", PromptList(ps))
	}

	// Without files only the built-in one is there.
	ps, _ = LoadPresets([]string{t.TempDir()})
	if len(ps) != 1 || ps[0].Name != "general" || ps[0].Instructions == "" {
		t.Fatalf("built-in %+v", ps)
	}
	if _, err := Find(ps, "nope"); err == nil || !strings.Contains(err.Error(), "presets: general") {
		t.Fatalf("find: %v", err)
	}
}

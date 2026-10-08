package subagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestStateUniqueAndTurnStatus(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	s := State{Name: "a", Parent: "p1", Session: "c1", Created: time.Now()}
	if err := Create(s); err != nil {
		t.Fatal(err)
	}
	if err := Create(s); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatalf("second create: %v", err)
	}
	if err := Create(State{Name: "../x", Parent: "p1"}); err == nil {
		t.Fatal("bad name saved")
	}
	if _, err := Load("p1", "b"); err == nil {
		t.Fatal("loaded a missing subagent")
	}
	got, err := Load("p1", "a")
	if err != nil || got.Latest().Status != Idle {
		t.Fatalf("load %+v %v", got, err)
	}
	// A turn without a job never started.
	got.Turns = 1
	if st := got.Latest(); st.Status != Failed {
		t.Fatalf("status %+v", st)
	}
	_ = SaveTurn("p1", "a", Turn{N: 1, Status: Done, Started: time.Now().Add(-time.Minute), Ended: time.Now()})
	got.Job = 99 // a job that doesn't exist: the turn's own record is kept only for a live one
	if st := got.Latest(); st.Status != Failed {
		t.Fatalf("status %+v", st)
	}
	if l := List("p1"); len(l) != 1 || l[0].Name != "a" {
		t.Fatalf("list %+v", l)
	}
}

func TestSlotsQueue(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	r1, ok, err := TryAcquire("p", 2)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	r2, ok, _ := TryAcquire("p", 2)
	if !ok {
		t.Fatal("second slot")
	}
	if _, ok, _ := TryAcquire("p", 2); ok {
		t.Fatal("a third turn must wait")
	}
	other, ok, err := TryAcquire("other", 2)
	if err != nil || !ok {
		t.Fatal("another session has slots of its own:", err)
	}
	defer other()
	slotPoll = 10 * time.Millisecond
	got := make(chan func(), 1)
	go func() {
		r, _ := Acquire(t.Context(), "p", func() int { return 2 })
		got <- r
	}()
	select {
	case <-got:
		t.Fatal("acquired while full")
	case <-time.After(100 * time.Millisecond):
	}
	r1()
	select {
	case r := <-got:
		r()
	case <-time.After(2 * time.Second):
		t.Fatal("the queued turn did not get the freed slot")
	}
	r2()
}

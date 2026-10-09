package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/provider/providertest"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
	"github.com/sebastianrcnt/atto/ui"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "core.autocrlf=false"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// diffApp is a terminal whose project is a git repository with a
// modified, a staged and an untracked file, running only the built-in
// extensions; the model is scripted.
func diffApp(t *testing.T, script ...providertest.Reply) (*App, *providertest.Model) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	m := providertest.New(t, script...)
	cwd := loadedEnv(t, m.URL)
	if err := os.RemoveAll(filepath.Join(cwd, ".git")); err != nil { // loadedEnv's stand-in
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(cwd, "main.go"), "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n")
	writeTestFile(t, filepath.Join(cwd, "util.go"), "package main\n")
	gitIn(t, cwd, "init", "-q")
	gitIn(t, cwd, "add", ".")
	gitIn(t, cwd, "commit", "-q", "-m", "init")
	writeTestFile(t, filepath.Join(cwd, "main.go"), "package main\n\nfunc main() {\n\tprintln(\"hello\")\n\tprintln(\"bye\")\n}\n")
	writeTestFile(t, filepath.Join(cwd, "util.go"), "package main\n\nfunc util() {}\n")
	gitIn(t, cwd, "add", "util.go")
	writeTestFile(t, filepath.Join(cwd, "notes.txt"), "todo\n")
	return startApp(t, cwd), m
}

// textBlocks are the transcript's blocks of extension text.
func textBlocks(a *App) []*extTextBlock {
	var out []*extTextBlock
	for _, c := range a.ui.Body.Children {
		if g, ok := c.(gap); ok {
			if b, ok := g.Component.(*extTextBlock); ok {
				out = append(out, b)
			}
		}
	}
	return out
}

func lastText(t *testing.T, a *App, n int) *extTextBlock {
	t.Helper()
	var b *extTextBlock
	within(t, a, "the diff block", func() bool {
		if bs := textBlocks(a); len(bs) == n {
			b = bs[n-1]
			return true
		}
		return false
	})
	return b
}

func portableDiff(t *testing.T, a *App, n int) *tui.Elements {
	t.Helper()
	var e *tui.Elements
	within(t, a, "portable diff", func() bool {
		if len(a.uiBlocks) != n {
			return false
		}
		for _, b := range a.uiBlocks {
			if e == nil || b.Rev > e.Rev {
				e = b
			}
		}
		return e != nil
	})
	return e
}
func TestDiffCommandShowsABlock(t *testing.T) {
	a, _ := diffApp(t)
	typeLine(a, "/diff")
	e := portableDiff(t, a, 1)
	a.ui.Do(func() {
		for _, width := range []int{40, 80, 120, 160} {
			lines := e.Render(width)
			for _, l := range lines {
				if tui.VisibleWidth(l) > width {
					t.Fatal("overflow")
				}
			}
			builtinGolden(t, "diff", width, lines)
		}
		shown := plainLines(e.Render(80))
		for _, want := range []string{"▸ git diff", "2 files changed, +4 -1", "?? notes.txt"} {
			if !strings.Contains(shown, want) {
				t.Errorf("missing %q: %s", want, shown)
			}
		}
		// Preserve a concrete before screenshot using the retired renderer, only in tests.
		var parts []string
		for _, c := range e.Tree.Children {
			parts = append(parts, ui.PlainText(c))
		}
		old := &extTextBlock{ext: "diff", title: "git diff", text: strings.Join(parts, "\n"), lang: "diff", preview: int(e.Tree.Props["previewLines"].(float64))}
		builtinGolden(t, "diff-before", 80, old.Render(80))
		e.Click(0)
		expanded := plainLines(e.Render(80))
		if !strings.Contains(expanded, `+   println("hello")`) {
			t.Fatal(expanded)
		}
		if !strings.Contains(strings.Join(e.Render(80), "\n"), tui.FG(2, `+   println("hello")`)) {
			t.Fatal("lost diff colors")
		}
	})
}
func TestDiffArgumentsInTUI(t *testing.T) {
	a, _ := diffApp(t)
	typeLine(a, "/diff --staged")
	e := portableDiff(t, a, 1)
	text := ui.PlainText(*e.Tree)
	if !strings.Contains(text, "1 file changed, +2 -0") || strings.Contains(text, "main.go") {
		t.Fatal(text)
	}
	typeLine(a, "/diff main.go")
	e = portableDiff(t, a, 2)
	text = ui.PlainText(*e.Tree)
	if !strings.Contains(text, "git diff main.go") || !strings.Contains(text, "1 file changed, +2 -1") || strings.Contains(text, "util.go") {
		t.Fatal(text)
	}
}
func TestDiffBlockIsDisplayOnlyAndSurvivesResume(t *testing.T) {
	a, m := diffApp(t, providertest.Reply{Text: "first"}, providertest.Reply{Text: "second"})
	send(t, a, "q1")
	typeLine(a, "/diff")
	e := portableDiff(t, a, 1)
	var shown string
	a.ui.Do(func() { shown = plainLines(e.Render(80)) })
	send(t, a, "q2")
	reqs := m.Requests()
	if len(reqs) != 2 {
		t.Fatal(len(reqs))
	}
	for _, bad := range []string{"diff --git", "files changed", "git diff", "util.go", "notes.txt"} {
		if strings.Contains(reqs[1], bad) {
			t.Fatalf("display leaked: %s", bad)
		}
	}
	_, entries, err := session.Load(a.sessPath)
	if err != nil {
		t.Fatal(err)
	}
	opens, updates := 0, 0
	for _, entry := range entries {
		switch entry.Type {
		case session.TypeUIBlock:
			opens++
		case session.TypeUIBlockUpdate:
			updates++
		case session.TypeExtText:
			t.Fatal("native diff used legacy API")
		}
	}
	if opens != 1 || updates != 1 {
		t.Fatal(opens, updates)
	}
	r := reopen(t, a)
	b := portableDiff(t, r, 1)
	if got := plainLines(b.Render(80)); got != shown {
		t.Errorf("resume:\n%s\nlive:\n%s", got, shown)
	}
	var order []string
	for _, c := range r.ui.Body.Children {
		if g, ok := c.(gap); ok {
			switch g.Component.(type) {
			case *userBlock:
				order = append(order, "user")
			case *tui.Elements:
				order = append(order, "diff")
			}
		}
	}
	if fmt.Sprint(order) != "[user diff user]" {
		t.Fatal(order)
	}
}

func TestExtTextRenderingIsGenericAndCached(t *testing.T) {
	d := &details{}
	b := &extTextBlock{d: d, ext: "demo", title: "Report", text: "plain\n+not green\n\ttabbed\x1b[31m\n", lang: ""}
	got := plainLines(b.Render(40))
	if got != "± Report · demo\n  plain\n  +not green\n     tabbed" {
		t.Errorf("plain text:\n%s", got)
	}
	if strings.Contains(strings.Join(b.Render(40), ""), tui.FG(2, "")) {
		t.Error("only lang diff colours")
	}
	if b.Click(0) {
		t.Error("short text does not collapse")
	}
	// A preview of 2 of 5 lines.
	b = &extTextBlock{d: d, ext: "demo", title: "T", text: "1\n2\n3\n4\n5", preview: 2}
	if got := plainLines(b.Render(40)); !strings.Contains(got, "  2\n    + 3 lines (click or ctrl+t to expand)") {
		t.Errorf("preview:\n%s", got)
	}
	d.on, d.gen = true, d.gen+1 // ctrl+t
	if got := plainLines(b.Render(40)); !strings.Contains(got, "  5\n    − Show less") {
		t.Errorf("expanded by ctrl+t:\n%s", got)
	}
	// A narrow width truncates lines.
	long := &extTextBlock{d: d, ext: "demo", title: "T", text: strings.Repeat("x", 100)}
	for _, l := range long.Render(30) {
		if tui.VisibleWidth(l) > 30 {
			t.Errorf("too wide: %q", l)
		}
	}
}

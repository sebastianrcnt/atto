package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "core.autocrlf=false"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// diffApp is an app whose project is a git repository with a modified, a
// staged and an untracked file, running only the built-in extensions.
func diffApp(t *testing.T) *App {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	a := loadedApp(t)
	if err := os.RemoveAll(filepath.Join(a.cwd, ".git")); err != nil { // loadedApp's stand-in
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(a.cwd, "main.go"), "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n")
	writeTestFile(t, filepath.Join(a.cwd, "util.go"), "package main\n")
	gitIn(t, a.cwd, "init", "-q")
	gitIn(t, a.cwd, "add", ".")
	gitIn(t, a.cwd, "commit", "-q", "-m", "init")
	writeTestFile(t, filepath.Join(a.cwd, "main.go"), "package main\n\nfunc main() {\n\tprintln(\"hello\")\n\tprintln(\"bye\")\n}\n")
	writeTestFile(t, filepath.Join(a.cwd, "util.go"), "package main\n\nfunc util() {}\n")
	gitIn(t, a.cwd, "add", "util.go")
	writeTestFile(t, filepath.Join(a.cwd, "notes.txt"), "todo\n")

	a.ext = core.LoadExtensions(a.agent, newTUIHost(a))
	t.Cleanup(func() {
		a.ext.Close()
		a.doQuit()
	})
	a.ui.Do(func() {
		a.newSession("")
		a.sessionStartHook("startup")
	})
	return a
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

func TestDiffCommandShowsABlock(t *testing.T) {
	a := diffApp(t)
	a.ui.Do(func() { a.runCommand("/diff") })
	b := lastText(t, a, 1)

	var collapsed, expanded, raw string
	a.ui.Do(func() {
		lines := b.Render(80)
		raw = strings.Join(lines, "\n")
		collapsed = plainLines(lines)
	})
	t.Logf("collapsed, width 80:\n%s", collapsed)
	for _, want := range []string{"± git diff · diff", "2 files changed, +4 -1 (1 staged, 1 unstaged, 1 untracked)", "?? notes.txt  untracked"} {
		if !strings.Contains(collapsed, want) {
			t.Errorf("collapsed lacks %q:\n%s", want, collapsed)
		}
	}
	if !strings.Contains(collapsed, "lines (click or ctrl+t to expand)") {
		t.Errorf("a long diff is collapsed with the usual row:\n%s", collapsed)
	}
	// Diff colours: added green, removed red, hunks cyan, headers dim.
	a.ui.Do(func() { b.Click(0); expanded = plainLines(b.Render(80)); raw = strings.Join(b.Render(80), "\n") })
	t.Logf("expanded, width 80:\n%s", expanded)
	for _, want := range []string{tui.FG(2, `+   println("hello")`), tui.FG(1, `-   println("hi")`), tui.FG(6, "@@ -1,5 +1,6 @@"), tui.Dim("+++ b/main.go")} {
		if !strings.Contains(raw, "  "+want) {
			t.Errorf("no %q in\n%q", want, raw)
		}
	}
	if !strings.Contains(expanded, "− Show less") || strings.Contains(expanded, "to expand") {
		t.Errorf("expanded:\n%s", expanded)
	}
	// Each line fits the width.
	a.ui.Do(func() {
		for _, l := range b.Render(80) {
			if w := tui.VisibleWidth(l); w > 80 {
				t.Errorf("a line of %d columns: %q", w, l)
			}
		}
	})
	// Clicking the footer collapses it again.
	a.ui.Do(func() {
		n := len(b.Render(80))
		if !b.Click(n-1) || strings.Contains(plainLines(b.Render(80)), "Show less") {
			t.Error("the footer collapses the block")
		}
		if b.Click(2) {
			t.Error("a click inside the text does nothing")
		}
	})
}

func TestDiffArgumentsInTUI(t *testing.T) {
	a := diffApp(t)
	a.ui.Do(func() { a.runCommand("/diff --staged") })
	b := lastText(t, a, 1)
	if !strings.HasPrefix(b.text, "1 file changed, +2 -0\n") || strings.Contains(b.text, "main.go") {
		t.Errorf("--staged:\n%s", b.text)
	}
	a.ui.Do(func() { a.runCommand("/diff main.go") })
	b = lastText(t, a, 2)
	if b.title != "git diff main.go" || !strings.HasPrefix(b.text, "1 file changed, +2 -1") || strings.Contains(b.text, "util.go") {
		t.Errorf("path:\n%s", b.text)
	}
	// Not a repository: a notice, no block.
	plain := t.TempDir()
	a.ui.Do(func() { a.cwd = plain })
	a.ext.Close()
	a.agent.Cwd = plain
	a.ext = core.LoadExtensions(a.agent, newTUIHost(a))
	a.ui.Do(func() { a.runCommand("/diff") })
	within(t, a, "the notice", func() bool { return strings.Contains(bodyText(a), "[diff] Not a git repository:") })
	if n := len(textBlocks(a)); n != 2 {
		t.Errorf("%d blocks", n)
	}
}

func TestDiffBlockIsDisplayOnlyAndSurvivesResume(t *testing.T) {
	a := diffApp(t)
	srv := newMainServer(t, reply{"", "first"}, reply{"", "second"})
	useMain(t, a, srv, "")
	runTurn(t, a, "q1")
	a.ui.Do(func() { a.runCommand("/diff") })
	b := lastText(t, a, 1)
	var shown string
	a.ui.Do(func() { shown = plainLines(b.Render(80)) })
	runTurn(t, a, "q2")

	// The model's context has none of it.
	for _, m := range a.agent.Messages() {
		if strings.Contains(m.Content, "diff --git") || strings.Contains(m.Content, "files changed") {
			t.Errorf("message %q reached the model", m.Content)
		}
	}
	reqs := srv.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests", len(reqs))
	}
	for _, bad := range []string{"diff --git", "files changed", "git diff", "util.go", "notes.txt"} {
		if strings.Contains(reqs[1], bad) {
			t.Errorf("the request mentions %q:\n%s", bad, reqs[1])
		}
	}
	// The conversation went on around it.
	if !strings.Contains(reqs[1], `"q1"`) || !strings.Contains(reqs[1], `"first"`) || !strings.Contains(reqs[1], `"q2"`) {
		t.Errorf("request %s", reqs[1])
	}

	// Saved as one ext_text entry.
	_, entries, err := session.Load(a.sess.Path)
	if err != nil {
		t.Fatal(err)
	}
	var saved []session.Entry
	for _, e := range entries {
		if e.Type == session.TypeExtText {
			saved = append(saved, e)
		}
	}
	if len(saved) != 1 || saved[0].Ext != "diff" || saved[0].Title != "git diff" || saved[0].Lang != "diff" || saved[0].Display != b.text || saved[0].Preview != b.preview {
		t.Fatalf("saved %+v", saved)
	}

	// A resumed session shows the same block, in the same place, and the
	// model's context is still without it.
	r := &App{ui: tui.New(nullTerm{}), agent: agent.New(config.ModelRef{ProviderName: "t", Model: config.Model{ID: "m"}}, "", a.cwd),
		cwd: a.cwd, quit: make(chan struct{})}
	r.build()
	r.newSession("")
	r.resume(a.sess.Path)
	t.Cleanup(func() { r.sess.Close() })
	bs := textBlocks(r)
	if len(bs) != 1 {
		t.Fatalf("%d blocks after resume", len(bs))
	}
	if got := plainLines(bs[0].Render(80)); got != shown {
		t.Errorf("resumed:\n%s\nlive:\n%s", got, shown)
	}
	var order []string
	for _, c := range r.ui.Body.Children {
		if g, ok := c.(gap); ok {
			switch g.Component.(type) {
			case *userBlock:
				order = append(order, "user")
			case *extTextBlock:
				order = append(order, "diff")
			}
		}
	}
	if fmt.Sprint(order) != "[user diff user]" {
		t.Errorf("order %v", order)
	}
	for _, m := range r.agent.Messages() {
		if strings.Contains(m.Content, "diff --git") {
			t.Errorf("resumed context has %q", m.Content)
		}
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

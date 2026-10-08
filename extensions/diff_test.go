package extensions

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/config"
)

// The built-in /diff extension, run against real repositories.

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "core.autocrlf=false"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// diffEnv is an empty ATTO_DIR and a directory to make repositories in.
func diffEnv(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("ATTO_DIR", filepath.Join(root, "atto"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig")) // the user's diff settings are not under test
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	if err := config.Ensure(); err != nil {
		t.Fatal(err)
	}
	return root
}

func lines(n int, prefix string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%s%d\n", prefix, i)
	}
	return b.String()
}

// changedRepo has a modified, a deleted, a staged-new, a staged-modified and
// an untracked file, and a modified file in a subdirectory.
func changedRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(diffEnv(t), "repo")
	write(t, filepath.Join(dir, "mod.txt"), "one\ntwo\nthree\n")
	write(t, filepath.Join(dir, "del.txt"), "a\nb\n")
	write(t, filepath.Join(dir, "staged.txt"), "old\n")
	write(t, filepath.Join(dir, "sub", "inner.txt"), "x\n")
	git(t, dir, "init", "-q")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "init")

	write(t, filepath.Join(dir, "mod.txt"), "one\nTWO\nthree\n")
	if err := os.Remove(filepath.Join(dir, "del.txt")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "sub", "inner.txt"), "y\n")
	write(t, filepath.Join(dir, "add.txt"), "new1\nnew2\n")
	write(t, filepath.Join(dir, "staged.txt"), "new\n")
	git(t, dir, "add", "add.txt", "staged.txt")
	write(t, filepath.Join(dir, "untracked.txt"), "u\n")
	return dir
}

// runDiff runs /diff args and waits for what it shows: the text block, or
// else the notice.
func runDiff(t *testing.T, m *Manager, h *fakeHost, args string) (shownText, string) {
	t.Helper()
	before := len(h.shown())
	nbefore := len(h.snapshot().notices)
	if !m.RunCommand("diff", args) {
		t.Fatal("no /diff command")
	}
	var text shownText
	var notice string
	eventually(t, "/diff "+args, func() bool {
		if s := h.shown(); len(s) > before {
			text = s[len(s)-1]
			return true
		}
		if n := h.snapshot().notices; len(n) > nbefore {
			notice = n[len(n)-1]
			return true
		}
		return false
	})
	return text, notice
}

func TestDiffSummaryAndContent(t *testing.T) {
	dir := changedRepo(t)
	h := newHost(true)
	m := load(t, dir, h)

	got, notice := runDiff(t, m, h, "")
	if notice != "" {
		t.Fatalf("notice %q", notice)
	}
	if got.ext != "diff" || got.title != "git diff" || got.opts.Lang != "diff" {
		t.Fatalf("%+v", got)
	}
	ls := strings.Split(got.text, "\n")
	if ls[0] != "5 files changed, +5 -5 (2 staged, 3 unstaged, 1 untracked)" {
		t.Fatalf("summary line %q", ls[0])
	}
	for _, want := range []string{"M  staged.txt", "A  add.txt", " M mod.txt", " D del.txt", " M sub/inner.txt", "?? untracked.txt"} {
		if !strings.Contains(got.text, "  "+want) {
			t.Errorf("no row %q:\n%s", want, got.text)
		}
	}
	for _, want := range []string{"mod.txt  ", "+1 -1", "del.txt  ", "+0 -2", "add.txt  ", "+2 -0", "untracked"} {
		if !strings.Contains(got.text, want) {
			t.Errorf("summary lacks %q:\n%s", want, got.text)
		}
	}
	// The summary comes first, then staged changes, then unstaged ones.
	idx := func(s string) int { return strings.Index(got.text, s) }
	order := []int{idx("5 files changed"), idx("== Staged changes =="), idx("+++ b/add.txt"), idx("== Unstaged changes =="), idx("deleted file mode"), idx("-two"), idx("+TWO")}
	if !slices.IsSorted(order) || slices.Contains(order, -1) {
		t.Errorf("order %v:\n%s", order, got.text)
	}
	if strings.Contains(got.text, "u\n") && strings.Contains(got.text, "+u") {
		t.Errorf("an untracked file's content is not shown:\n%s", got.text)
	}
	// The block collapses after the summary and a few diff lines.
	if got.opts.Preview < 8 || got.opts.Preview > len(ls) {
		t.Errorf("preview %d of %d lines", got.opts.Preview, len(ls))
	}

	// A path limits it, relative to the session's directory.
	got, _ = runDiff(t, m, h, "sub")
	if got.title != "git diff sub" || !strings.HasPrefix(got.text, "1 file changed, +1 -1 (1 unstaged)") ||
		strings.Contains(got.text, "mod.txt") || !strings.Contains(got.text, "-x") {
		t.Errorf("/diff sub:\n%s", got.text)
	}

	// --staged: only what is staged, with no sections.
	got, _ = runDiff(t, m, h, "--staged")
	if got.title != "git diff --staged" || !strings.HasPrefix(got.text, "2 files changed, +3 -1\n") {
		t.Errorf("/diff --staged:\n%s", got.text)
	}
	for _, bad := range []string{"mod.txt", "del.txt", "untracked", "== ", "-two"} {
		if strings.Contains(got.text, bad) {
			t.Errorf("--staged shows %q:\n%s", bad, got.text)
		}
	}
	if !strings.Contains(got.text, "+new1") || !strings.Contains(got.text, "-old") || !strings.Contains(got.text, "\n+new") {
		t.Errorf("--staged lacks the staged diff:\n%s", got.text)
	}
	// Both together, in either order, and --cached.
	if got, _ = runDiff(t, m, h, "--cached"); !strings.Contains(got.title, "--staged") {
		t.Errorf("title %q", got.title)
	}
}

func TestDiffNothingToShow(t *testing.T) {
	dir := changedRepo(t)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "all")
	h := newHost(true)
	m := load(t, dir, h)
	if _, n := runDiff(t, m, h, ""); n != "info diff: No changes." {
		t.Errorf("notice %q", n)
	}
	if _, n := runDiff(t, m, h, "--staged"); n != "info diff: Nothing staged." {
		t.Errorf("notice %q", n)
	}
	if _, n := runDiff(t, m, h, "sub"); n != "info diff: No changes in sub." {
		t.Errorf("notice %q", n)
	}
	// Only an untracked file: listed, with no diff to show.
	write(t, filepath.Join(dir, "fresh.txt"), "x\n")
	got, _ := runDiff(t, m, h, "")
	if got.text != "0 files changed, +0 -0 (1 untracked)\n  ?? fresh.txt  untracked" {
		t.Errorf("text %q", got.text)
	}
}

func TestDiffNotARepository(t *testing.T) {
	dir := filepath.Join(diffEnv(t), "plain")
	write(t, filepath.Join(dir, "a.txt"), "x")
	h := newHost(true)
	m := load(t, dir, h)
	text, notice := runDiff(t, m, h, "")
	if text.title != "" || notice != "warning diff: Not a git repository: "+dir {
		t.Fatalf("%+v %q", text, notice)
	}
}

func TestDiffLargeDiffIsCutAndCollapsed(t *testing.T) {
	dir := filepath.Join(diffEnv(t), "big")
	write(t, filepath.Join(dir, "big.txt"), lines(2500, "old "))
	git(t, dir, "init", "-q")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "init")
	write(t, filepath.Join(dir, "big.txt"), lines(2500, "new "))
	h := newHost(true)
	m := load(t, dir, h)
	got, _ := runDiff(t, m, h, "")
	ls := strings.Split(got.text, "\n")
	if !strings.HasPrefix(got.text, "1 file changed, +2500 -2500 (1 unstaged)\n") {
		t.Fatalf("summary:\n%s", ls[:3])
	}
	// The summary (the total and a row), a blank line, 2000 diff lines and
	// the note about the rest.
	if len(ls) != 2+1+2000+1 || !strings.Contains(ls[len(ls)-1], "diff cut:") {
		t.Fatalf("%d lines, last %q", len(ls), ls[len(ls)-1])
	}
	// Collapsed: the summary and the first diff lines.
	if got.opts.Preview != 2+1+14 {
		t.Errorf("preview %d", got.opts.Preview)
	}
	if strings.Contains(strings.Join(ls[:got.opts.Preview], "\n"), "@@ -2") {
		t.Errorf("the preview reaches too far")
	}
}

func TestDiffPathsWithSpacesAndBinaryFiles(t *testing.T) {
	dir := filepath.Join(diffEnv(t), "odd")
	write(t, filepath.Join(dir, "my dir", "a b.txt"), "1\n")
	write(t, filepath.Join(dir, "bin.dat"), "a\x00b")
	git(t, dir, "init", "-q")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "init")
	write(t, filepath.Join(dir, "my dir", "a b.txt"), "2\n")
	write(t, filepath.Join(dir, "bin.dat"), "a\x00c")
	h := newHost(true)
	m := load(t, dir, h)

	got, _ := runDiff(t, m, h, "my dir")
	if !strings.HasPrefix(got.text, "1 file changed, +1 -1") || !strings.Contains(got.text, "my dir/a b.txt") || strings.Contains(got.text, "bin.dat") {
		t.Errorf("a path with a space:\n%s", got.text)
	}
	got, _ = runDiff(t, m, h, `"my dir/a b.txt"`)
	if !strings.HasPrefix(got.text, "1 file changed, +1 -1") {
		t.Errorf("a quoted path:\n%s", got.text)
	}
	got, _ = runDiff(t, m, h, "bin.dat")
	if !strings.Contains(got.text, "bin.dat  binary") || !strings.HasPrefix(got.text, "1 file changed, +0 -0") {
		t.Errorf("a binary file:\n%s", got.text)
	}
	if _, n := runDiff(t, m, h, "it's"); !strings.Contains(n, "single quote") {
		t.Errorf("notice %q", n)
	}
}

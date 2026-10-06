package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

func saveSession(t *testing.T, cwd, text string) string {
	t.Helper()
	w := session.New(cwd)
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: text}})
	w.Close()
	return w.Path
}

// repoDir makes a directory that looks like a git checkout of branch.
func repoDir(t *testing.T, name, branch string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	head := "ref: refs/heads/" + branch + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte(head), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func plain(p *resumePicker, width int) string {
	return strings.ReplaceAll(tui.StripEscapes(strings.Join(p.Render(width), "\n")), tui.CursorMarker, "")
}

func TestFormatSize(t *testing.T) {
	for n, want := range map[int64]string{0: "0B", 1023: "1023B", 1024: "1.0KB", 811725: "792.7KB", 3 << 20: "3.0MB", 5 << 30: "5.0GB"} {
		if got := formatSize(n); got != want {
			t.Errorf("formatSize(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestResumePickerRender(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	cwd := repoDir(t, "proj", "feat")
	saveSession(t, cwd, "fix the parser")
	cur := saveSession(t, cwd, "write docs\nwith two lines")
	saveSession(t, "/work/b", "elsewhere")

	p := newResumePicker(cwd, cur)
	out := plain(p, 80)
	t.Logf("\n%s", out)
	lines := strings.Split(out, "\n")
	if !strings.HasPrefix(lines[0], "Resume session (1 of 2)") {
		t.Fatalf("title and position: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "╭─") || !strings.Contains(lines[2], "⌕ Search…") || !strings.HasPrefix(lines[3], "╰─") {
		t.Fatalf("search box: %q", lines[1:4])
	}
	if lines[4] != "proj" || lines[5] != "" {
		t.Fatalf("group header: %q", lines[4:6])
	}
	if !strings.HasPrefix(lines[6], "❯ ") || !strings.Contains(lines[7], " · feat · ") || lines[8] != "" {
		t.Fatalf("two-line rows with a blank line: %q", lines[6:9])
	}
	if !strings.Contains(out, "current") || strings.Contains(out, "elsewhere") {
		t.Fatalf("current marker, only this directory: %q", out)
	}
	if strings.Contains(out, "Filter:") || strings.Contains(out, "›") {
		t.Fatalf("old header: %q", out)
	}
	if !strings.Contains(lines[len(lines)-1], "Esc to cancel") {
		t.Fatalf("footer last: %q", lines[len(lines)-1])
	}

	for _, c := range "docs" { // typed text replaces the placeholder
		p.HandleInput(string(c))
	}
	if out := plain(p, 80); strings.Contains(out, "Search…") || !strings.Contains(out, "│ ⌕ docs") || !strings.Contains(out, "(1 of 1)") {
		t.Fatalf("typed search: %q", out)
	}
}

func TestResumePickerFooterWraps(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	p := newResumePicker(repoDir(t, "proj", "main"), "")
	for _, w := range []int{40, 60, 100} {
		var foot []string
		for l := range strings.SplitSeq(plain(p, w), "\n") {
			if strings.Contains(l, " to ") || strings.Contains(l, "Type to") {
				foot = append(foot, l)
			}
		}
		joined := strings.Join(foot, " · ")
		if !strings.Contains(joined, "Esc to cancel") || !strings.Contains(joined, "Ctrl+B to only show current branch") {
			t.Fatalf("width %d: hints cut: %q", w, joined)
		}
		for _, l := range foot {
			if tui.VisibleWidth(l) > w || strings.HasSuffix(l, " ·") {
				t.Fatalf("width %d: line too wide or split mid-separator: %q", w, l)
			}
		}
		if w == 40 && len(foot) < 3 {
			t.Fatalf("narrow footer should wrap: %q", foot)
		}
	}
	// Outside a repository there is no branch to filter by.
	q := newResumePicker(t.TempDir(), "")
	if out := plain(q, 100); strings.Contains(out, "Ctrl+B") {
		t.Fatalf("no branch, no ctrl+b hint: %q", out)
	}
}

func TestResumePickerScrollMarkers(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	for i := range 9 {
		saveSession(t, "/w", "message "+string(rune('a'+i)))
	}
	p := newResumePicker("/w", "")
	count := func() int { return strings.Count(plain(p, 80), "now ·") }
	out := plain(p, 80)
	if got := count(); got != resumeVisible || !strings.Contains(out, "(1 of 9)") {
		t.Fatalf("%d rows: %q", got, out)
	}
	if !strings.Contains(out, "\n↓ ") || strings.Contains(out, "↑") {
		t.Fatalf("down arrow only at the top: %q", out)
	}
	for range 4 {
		p.HandleInput("\x1b[B")
	}
	if out := plain(p, 80); !strings.Contains(out, "\n↑ ") || !strings.Contains(out, "\n↓ ") || !strings.Contains(out, "(5 of 9)") {
		t.Fatalf("both arrows in the middle: %q", out)
	}
	p.HandleInput("\x1b[A")
	p.HandleInput("\x1b[A")
	p.HandleInput("\x1b[A")
	p.HandleInput("\x1b[A")
	p.HandleInput("\x1b[A") // wraps to the end
	if p.list.Selected != 8 {
		t.Fatalf("up wraps: %d", p.list.Selected)
	}
	if out := plain(p, 80); !strings.Contains(out, "\n↑ ") || strings.Contains(out, "↓") {
		t.Fatalf("up arrow only at the end: %q", out)
	}
}

func TestResumePicker(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	saveSession(t, "/work/a", "fix the parser")
	saveSession(t, "/work/a", "write docs")
	saveSession(t, "/work/b", "elsewhere")

	p := newResumePicker("/work/a", "")
	if n := len(p.list.Items); n != 2 {
		t.Fatalf("only this directory's sessions: %d", n)
	}
	for _, k := range []string{"d", "o", "c"} { // search
		p.HandleInput(k)
	}
	if n := len(p.list.Visible()); n != 1 {
		t.Fatalf("typing searches: %d", n)
	}
	p.HandleInput("x")
	if out := plain(p, 80); !strings.Contains(out, "No results for your search") {
		t.Fatalf("empty search: %q", out)
	}
	p.onCancel = func() { t.Fatal("esc clears the search first") }
	p.HandleInput("\x1b")
	p.HandleInput("\x1b[Z") // shift+tab: archived
	if out := plain(p, 80); !strings.Contains(out, "Archived sessions") || !strings.Contains(out, "No archived sessions") {
		t.Fatalf("archived toggle: %q", out)
	}
	p.HandleInput("\x1b[Z")

	p.HandleInput("\x01") // ctrl+a: all projects
	if out := plain(p, 80); len(p.list.Items) != 3 || !strings.Contains(out, "All projects") || !strings.Contains(out, "⌁") {
		t.Fatalf("ctrl+a lists all directories: %d %q", len(p.list.Items), out)
	}
	p.HandleInput("\x01")
	if len(p.list.Items) != 2 {
		t.Fatalf("ctrl+a again: back to this directory: %d", len(p.list.Items))
	}
	p.HandleInput("\t") // tab is an alias
	if len(p.list.Items) != 3 {
		t.Fatalf("tab lists all directories: %d", len(p.list.Items))
	}

	var archived, picked session.Summary
	p.onArchive = func(s session.Summary) { archived = s; _, _ = session.Archive(s.Path) }
	p.onPick = func(s session.Summary) { picked = s }
	p.HandleInput("\x18") // ctrl+x
	if archived.Path == "" || len(p.list.Items) != 2 {
		t.Fatalf("ctrl+x archives and reloads: %q %d", archived.Path, len(p.list.Items))
	}
	p.HandleInput("\r")
	if picked.Path == "" || picked.Path == archived.Path {
		t.Fatalf("enter picks the selection: %q", picked.Path)
	}

	called := false
	p.onCancel = func() { called = true }
	p.HandleInput("\x1b")
	if !called {
		t.Fatal("esc cancels")
	}
}

func TestResumePickerEmptyHint(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	saveSession(t, "/work/b", "elsewhere")
	p := newResumePicker("/work/a", "")
	if out := plain(p, 80); !strings.Contains(out, "No sessions in this directory · Ctrl+A shows all 1") {
		t.Fatalf("empty state: %q", out)
	}
}

func TestResumePickerBranchFilter(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	cwd := repoDir(t, "proj", "feat")
	saveSession(t, cwd, "on feat")
	// A session from an older atto, or another branch.
	other := saveSession(t, cwd, "on main")
	data, _ := os.ReadFile(other)
	_ = os.WriteFile(other, []byte(strings.Replace(string(data), `"gitBranch":"feat"`, `"gitBranch":"main"`, 1)), 0o644)

	p := newResumePicker(cwd, "")
	if len(p.list.Items) != 2 {
		t.Fatalf("all branches: %d", len(p.list.Items))
	}
	p.HandleInput("\x02") // ctrl+b
	if len(p.list.Items) != 1 || !strings.Contains(plain(p, 80), "proj · feat") {
		t.Fatalf("only the current branch: %d", len(p.list.Items))
	}
	p.HandleInput("\x02")
	if len(p.list.Items) != 2 {
		t.Fatalf("toggled back: %d", len(p.list.Items))
	}
}

func TestResumePickerPreview(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	w := session.New("/w")
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "hello there"}})
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: "general kenobi"}})
	w.Close()
	saveSession(t, "/w", "other thing")

	p := newResumePicker("/w", "")
	var picked session.Summary
	p.onPick = func(s session.Summary) { picked = s }
	p.HandleInput("h") // a space is text while searching
	p.HandleInput(" ")
	if p.mode != modeList || p.list.Query() != "h " {
		t.Fatalf("space types into a non-empty search: mode %v query %q", p.mode, p.list.Query())
	}
	p.HandleInput("\x7f")
	p.HandleInput("\x7f")

	// Pick the two-message session by content: both are saved within the
	// same clock tick on some systems, so their order is not fixed.
	for i, it := range p.list.Items {
		if it.Data.(session.Summary).Messages == 2 {
			p.list.Selected = i
		}
	}
	p.HandleInput(" ")
	if p.mode != modePreview {
		t.Fatal("space on an empty search previews")
	}
	out := plain(p, 80)
	if !strings.Contains(out, "You") || !strings.Contains(out, "hello there") || !strings.Contains(out, "general kenobi") || !strings.Contains(out, "Space or Esc") {
		t.Fatalf("preview: %q", out)
	}
	p.HandleInput("\x1b[A") // scrolling must not break
	p.HandleInput(" ")
	if p.mode != modeList || p.list.Query() != "" {
		t.Fatal("space returns to the list")
	}
	p.HandleInput(" ")
	p.onCancel = func() { t.Fatal("esc in a preview goes back, not cancel") }
	p.HandleInput("\x1b")
	if p.mode != modeList {
		t.Fatal("esc returns to the list")
	}
	p.HandleInput(" ")
	p.HandleInput("\r")
	if picked.Preview != "hello there" {
		t.Fatalf("enter in a preview resumes it: %q", picked.Preview)
	}
}

func TestResumePickerRename(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	path := saveSession(t, "/w", "first message")
	if err := session.Rename(path, "old name"); err != nil {
		t.Fatal(err)
	}
	p := newResumePicker("/w", "")
	p.onRename = func(s session.Summary, name string) error { return session.Rename(s.Path, name) }
	if out := plain(p, 80); !strings.Contains(out, "old name") || strings.Contains(out, "first message") {
		t.Fatalf("a named session shows its name: %q", out)
	}
	p.HandleInput("\x12") // ctrl+r
	if out := plain(p, 80); p.mode != modeRename || !strings.Contains(out, "Rename session") || !strings.Contains(out, "old name") {
		t.Fatalf("prefilled with the name: %q", out)
	}
	p.HandleInput("\x1b") // esc cancels
	if p.mode != modeList || p.list.Items[0].Data.(session.Summary).Name != "old name" {
		t.Fatal("esc cancels the rename")
	}
	p.HandleInput("\x12")
	p.HandleInput("\x15") // ctrl+u clears
	for _, c := range "new name" {
		p.HandleInput(string(c))
	}
	p.HandleInput("\r")
	if p.mode != modeList {
		t.Fatalf("enter saves: %v", p.renameErr)
	}
	if got := p.list.Items[0].Data.(session.Summary).Name; got != "new name" {
		t.Fatalf("renamed on disk: %q", got)
	}
}

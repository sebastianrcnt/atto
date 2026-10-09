package extensions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/sebastianrcnt/atto/shell"
	"github.com/sebastianrcnt/atto/ui"
	"unicode/utf8"
)

type fileStat struct {
	path           string
	added, removed int
	binary         bool
}

var diffHeader = regexp.MustCompile(`^diff --git a/(.*) b/(.*)$`)

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}
func parseDiffArgs(args string) (staged bool, path string) {
	var rest []string
	for _, w := range strings.FieldsFunc(args, jsSpace) {
		if w == "--staged" || w == "--cached" {
			staged = true
		} else if w != "--" {
			rest = append(rest, w)
		}
	}
	return staged, unquote(strings.Join(rest, " "))
}
func parseDiff(text string) []fileStat {
	var out []fileStat
	inHunk := false
	for line := range strings.SplitSeq(text, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			path := line[11:]
			if m := diffHeader.FindStringSubmatch(line); m != nil {
				path = m[2]
			}
			out = append(out, fileStat{path: path})
			inHunk = false
			continue
		}
		if len(out) == 0 {
			continue
		}
		cur := &out[len(out)-1]
		if strings.HasPrefix(line, "@@") {
			inHunk = true
		} else if !inHunk {
			if strings.HasPrefix(line, "Binary files ") || strings.HasPrefix(line, "GIT binary patch") {
				cur.binary = true
			} else if strings.HasPrefix(line, "rename to ") {
				cur.path = line[10:]
			}
		} else if strings.HasPrefix(line, "+") {
			cur.added++
		} else if strings.HasPrefix(line, "-") {
			cur.removed++
		}
	}
	return out
}

// jsLen preserves TypeScript's UTF-16 string lengths (padding and clipping).
func jsLen(s string) int { return len(utf16.Encode([]rune(s))) }
func jsSlice(s string, n int) string {
	u := utf16.Encode([]rune(s))
	if len(u) > n {
		u = u[:n]
	}
	return string(utf16.Decode(u))
}

type gitResult struct {
	stdout, stderr string
	code           int
}

func (m *Manager) diffGit(ctx context.Context, rest string) gitResult {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := shell.Command(ctx, "git -c core.quotepath=off "+rest)
	cmd.Dir = m.cwd
	id, _ := m.session()
	cmd.Env = append(os.Environ(), "ATTO_SESSION_ID="+id, "ATTO_EXTENSION=diff")
	var out, errout bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errout
	err := shell.Run(cmd)
	code := 0
	if ctx.Err() != nil {
		code = -1
	} else if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		code = ee.ExitCode()
	} else if err != nil {
		code = -1
		errout.WriteString(err.Error())
	}
	return gitResult{out.String(), errout.String(), code}
}
func firstLine(s string) string { line, _, _ := strings.Cut(strings.TrimSpace(s), "\n"); return line }
func (m *Manager) nativeDiff(ctx context.Context, h Host, args string) {
	staged, path := parseDiffArgs(args)
	notify := func(text, level string) {
		if ctx.Err() == nil {
			h.Notify("diff", text, level)
		}
	}
	if strings.Contains(path, "'") {
		notify("/diff: a path with a single quote is not supported", "warning")
		return
	}
	probe := m.diffGit(ctx, "rev-parse --is-inside-work-tree")
	if probe.code != 0 {
		why := strings.TrimSpace(probe.stderr)
		if strings.Contains(strings.ToLower(why), "not a git repository") {
			notify("Not a git repository: "+m.cwd, "warning")
		} else {
			msg := "/diff: git failed"
			if why != "" {
				msg += ": " + firstLine(why)
			}
			notify(msg, "error")
		}
		return
	}
	spec := ""
	if path != "" {
		spec = " -- '" + path + "'"
	}
	flags := "--no-color --no-ext-diff --src-prefix=a/ --dst-prefix=b/"
	var results [3]gitResult
	done := make(chan int, 3)
	for i, rest := range []string{"status --porcelain=v1" + spec, "diff --cached " + flags + spec, "diff " + flags + spec} {
		if staged && i == 2 {
			done <- i
			continue
		}
		go func(i int, rest string) { results[i] = m.diffGit(ctx, rest); done <- i }(i, rest)
	}
	for range 3 {
		<-done
	}
	for _, r := range results {
		if r.code != 0 {
			why := firstLine(r.stderr)
			if why == "" {
				why = fmt.Sprintf("exit %d", r.code)
			}
			notify("/diff: git failed: "+why, "error")
			return
		}
	}
	status, cached, working := results[0], results[1], results[2]
	codes := map[string]string{}
	var untracked []string
	for line := range strings.SplitSeq(status.stdout, "\n") {
		if len(line) < 4 {
			continue
		}
		xy := line[:2]
		parts := strings.Split(line[3:], " -> ")
		name := unquote(parts[len(parts)-1])
		if xy == "??" {
			untracked = append(untracked, name)
		} else {
			codes[name] = xy
		}
	}
	stagedStats, workingStats := parseDiff(cached.stdout), parseDiff(working.stdout)
	var files []fileStat
	indices := map[string]int{}
	for _, s := range append(stagedStats, workingStats...) {
		if i, ok := indices[s.path]; ok {
			files[i].added += s.added
			files[i].removed += s.removed
			files[i].binary = files[i].binary || s.binary
		} else {
			indices[s.path] = len(files)
			files = append(files, s)
		}
	}
	where := ""
	if path != "" {
		where = " in " + path
	}
	if len(files) == 0 && (staged || len(untracked) == 0) {
		text := "No changes"
		if staged {
			text = "Nothing staged"
		}
		notify(text+where+".", "info")
		return
	}
	added, removed := 0, 0
	for _, f := range files {
		added += f.added
		removed += f.removed
	}
	plural := "s"
	if len(files) == 1 {
		plural = ""
	}
	total := fmt.Sprintf("%d file%s changed, +%d -%d", len(files), plural, added, removed)
	if !staged {
		var bits []string
		for _, b := range []struct {
			n     int
			label string
		}{{len(stagedStats), "staged"}, {len(workingStats), "unstaged"}, {len(untracked), "untracked"}} {
			if b.n > 0 {
				bits = append(bits, fmt.Sprintf("%d %s", b.n, b.label))
			}
		}
		if len(bits) > 0 {
			total += " (" + strings.Join(bits, ", ") + ")"
		}
	}
	summary := []string{total}
	var rows [][3]string
	for _, f := range files {
		code := codes[f.path]
		if code == "" {
			code = "  "
		}
		counts := fmt.Sprintf("+%d -%d", f.added, f.removed)
		if f.binary {
			counts = "binary"
		}
		rows = append(rows, [3]string{code, f.path, counts})
	}
	if !staged {
		for _, u := range untracked {
			rows = append(rows, [3]string{"??", u, "untracked"})
		}
	}
	width := 0
	for _, r := range rows {
		if n := jsLen(r[1]); n > width {
			width = n
		}
	}
	if width > 60 {
		width = 60
	}
	for i, r := range rows {
		if i == 40 {
			break
		}
		pad := max(width-jsLen(r[1]), 0)
		summary = append(summary, "  "+r[0]+" "+r[1]+strings.Repeat(" ", pad)+"  "+r[2])
	}
	if len(rows) > 40 {
		summary = append(summary, fmt.Sprintf("  ... and %d more", len(rows)-40))
	}
	var diff []string
	for _, section := range [][2]string{{"Staged changes", cached.stdout}, {"Unstaged changes", working.stdout}} {
		if strings.TrimSpace(section[1]) == "" {
			continue
		}
		if !staged {
			diff = append(diff, "== "+section[0]+" ==")
		}
		diff = append(diff, strings.Split(strings.TrimRight(section[1], "\n"), "\n")...)
	}
	if len(diff) > 2000 {
		hidden := len(diff) - 2000
		diff = append(diff[:2000], fmt.Sprintf("... diff cut: %d more lines (run git diff for all of it)", hidden))
	}
	title := "git diff"
	if staged {
		title += " --staged"
	}
	if path != "" {
		title += " " + path
	}
	if ctx.Err() == nil {
		tree := diffTree(title, summary, diff)
		if host, ok := h.(interface{ UIBlock(string, ui.Node) }); ok {
			host.UIBlock(title, tree)
		} else {
			h.Notify("diff", ui.PlainText(tree), "info")
		}

	}
}

// diffTree preserves the git/error/line truncation pipeline above, adding the
// catalog's byte budget. It is shared by full and noext builds.
func diffTree(title string, summary, diff []string) ui.Node {
	var rows []ui.Row
	for i, line := range summary[1:] {
		rows = append(rows, ui.Row{Key: fmt.Sprintf("file-%d", i), Cells: []string{line}})
	}
	source := strings.Join(diff, "\n")
	const budget = 110 << 10
	if len(source) > budget {
		source = source[:budget]
		for !utf8.ValidString(source) {
			source = source[:len(source)-1]
		}
		source += "\n... diff cut: byte limit (run git diff for all of it)"
	}
	return ui.Collapse(ui.CollapseProps{Key: "diff", Title: title, PreviewLines: len(summary) + 1 + 14}, ui.Text(ui.TextProps{Text: summary[0], Bold: true}), ui.List(ui.ListProps{Rows: rows, EmptyText: ""}), ui.Text(ui.TextProps{}), ui.Diff(ui.DiffProps{Source: source}))
}

package app

import (
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/tui"
)

func TestExpanderOverridesDetails(t *testing.T) {
	d := &details{}
	b := &thinkingBlock{d: d, done: true}
	b.text.WriteString("line one\nline two")
	g := gap{b}

	if len(b.Render(40)) != 1 {
		t.Fatal("should start collapsed")
	}
	g.Click(1) // header line (after the gap line)
	if len(b.Render(40)) < 3 {
		t.Fatal("click should expand")
	}
	g.Click(4) // "Show less", the last line
	if len(b.Render(40)) != 1 {
		t.Fatal("second click should collapse")
	}

	// ctrl+t expands everything; a click on "Show less" still collapses.
	d.on, d.gen = true, d.gen+1
	if !b.expanded() {
		t.Fatal("ctrl+t should expand")
	}
	b.Render(40) // clicks hit what was last drawn
	g.Click(4)   // "Show less", the last line
	if b.expanded() {
		t.Fatal("click should collapse even with details on")
	}
	// Toggling ctrl+t again resets per-block choices.
	d.on, d.gen = false, d.gen+1
	d.on, d.gen = true, d.gen+1
	if !b.expanded() {
		t.Fatal("ctrl+t should reset per-block state")
	}
}

func TestThinkingClickWhileStreaming(t *testing.T) {
	b := &thinkingBlock{d: &details{}}
	b.text.WriteString(strings.Repeat("word ", 400))
	collapsed := len(b.Render(40))
	if !b.Click(0) {
		t.Fatal("streaming thinking should be clickable")
	}
	if expanded := len(b.Render(40)); expanded <= collapsed {
		t.Fatalf("expanded %d <= collapsed %d", expanded, collapsed)
	}
}

func TestAssistantBulletNotDoubled(t *testing.T) {
	b := &textBlock{}
	b.text.WriteString("- apple\n- pear")
	if got := StripLine(b.Render(40)[0]); got != "  • apple" {
		t.Fatalf("got %q", got)
	}
	c := &textBlock{}
	c.text.WriteString("Plain answer.")
	if got := StripLine(c.Render(40)[0]); got != "• Plain answer." {
		t.Fatalf("got %q", got)
	}
}

func StripLine(s string) string { return tui.StripEscapes(s) }

func TestDisplayLinesTrimsPadding(t *testing.T) {
	got := displayLines("\r\nUptime  Free\r\n3h      3.9\r\n        \r\n        \r\n\r\n\r")
	if len(got) != 2 || got[0] != "Uptime  Free" || got[1] != "3h      3.9" {
		t.Fatalf("%q", got)
	}
	if got := displayLines("  \r\n\r\n"); len(got) != 0 {
		t.Fatalf("blank output has no lines: %q", got)
	}
}

func TestClickTogglesOnlyHeaderAndDisclosure(t *testing.T) {
	b := &toolBlock{d: &details{}, done: true}
	b.args.Description = "list"
	b.args.Command = "ls"
	b.append("1\n2\n3\n4\n5\n6\n")
	if out := b.Render(60); len(out) != 1 || StripLine(out[0]) != "✓ list · 0ms  $ ls" {
		t.Fatalf("done: not one line: %q", out)
	}
	if !b.Click(0) {
		t.Fatal("the line does not open")
	}
	out := b.Render(60) // header, $ ls, 1, 2, "… +2 lines", 5, 6
	if got := StripLine(out[4]); got != "    … +2 lines (click or ctrl+t to expand)" || StripLine(out[6]) != "    6" {
		t.Fatalf("preview %q", out)
	}
	if b.Click(1) || b.Click(2) || b.Click(len(out)-1) || b.expanded() {
		t.Fatal("a click on the command or the output toggled")
	}
	if !b.Click(4) || !b.expanded() {
		t.Fatal("the disclosure line expands")
	}
	b.Render(60)
	if b.Click(3) || !b.expanded() {
		t.Fatal("a click in the expanded output toggled")
	}
	if !b.Click(0) || b.expanded() || len(b.Render(60)) != 1 {
		t.Fatal("the header folds the block to its line")
	}
	if !b.Click(0) || b.expanded() || len(b.Render(60)) != 7 {
		t.Fatal("opened again, it shows the preview, not all")
	}

	// Nothing more than the output to show: only the header toggles.
	short := &toolBlock{d: &details{}, done: true}
	short.args.Command = "true"
	short.append("ok\n")
	short.Render(60)
	if !short.Click(0) || len(short.Render(60)) != 3 || short.Click(1) || short.Click(2) {
		t.Fatal("a short block: only the header toggles")
	}

	// A failed call shows why, folded.
	bad := &toolBlock{d: &details{}, done: true, res: agent.BashResult{ExitCode: 2}}
	bad.args.Command = "make"
	bad.append("a\nb\nc\nerror: boom\n")
	if got := plainLines(bad.Render(60)); got != "✗ make · exit 2 · 0ms\n  └ c\n    error: boom" {
		t.Fatalf("failed, folded:\n%s", got)
	}

	th := &thinkingBlock{d: &details{}, done: true}
	th.Render(60)
	if th.Click(0) {
		t.Fatal("empty thinking toggled")
	}
	th.text.WriteString("a\nb")
	th.Render(60)
	if !th.Click(0) {
		t.Fatal("thinking header expands")
	}
	th.Render(60) // header, a, b, Show less
	if th.Click(1) || !th.Click(3) || th.expanded() {
		t.Fatal("thinking: only header and Show less toggle")
	}

	c := &compactBlock{d: &details{}}
	c.notes.WriteString("notes")
	c.Render(60)
	if !c.Click(0) {
		t.Fatal("compaction header expands")
	}
	n := len(c.Render(60))
	if c.Click(1) || !c.Click(n-1) {
		t.Fatal("compaction: only header and Show less toggle")
	}
}

func TestCommandLinesWrap(t *testing.T) {
	cmd := "cd /some/long/project/path && npm run build && npm test -- --watch=false"
	got := commandLines(cmd, 30, 0)
	if len(got) < 3 || !strings.HasPrefix(tui.StripEscapes(got[0]), "  $ cd") || !strings.HasPrefix(tui.StripEscapes(got[1]), "    ") {
		t.Fatalf("wrapped: %q", got)
	}
	short := commandLines(cmd, 30, commandPreviewLines)
	if len(short) != 2 || !strings.HasSuffix(tui.StripEscapes(short[1]), "…") || tui.VisibleWidth(short[1]) > 30 {
		t.Fatalf("collapsed: %q", short)
	}
	if one := commandLines("ls", 30, commandPreviewLines); len(one) != 1 || tui.StripEscapes(one[0]) != "  $ ls" {
		t.Fatalf("short command: %q", one)
	}
}

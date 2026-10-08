package app

import (
	"strings"

	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/tui"
)

// Text an extension shows with ctx.ui.showText: a collapsible block that is
// display only. It is recorded as an ext_text session entry, which the model
// never sees, so a resumed session shows it again.

// defaultTextPreview is how many lines of the text show while collapsed.
const defaultTextPreview = 10

// extTextBlock shows a title and text; long text is cut to its first lines
// until the block is expanded (click or ctrl+t).
type extTextBlock struct {
	expander
	clickable
	ext, title, text, lang string
	preview                int
	cache                  tui.RenderCache[extTextKey]
}

type extTextKey struct {
	open bool
}

func (b *extTextBlock) Click(line int) bool {
	if !b.hit(line) {
		return false
	}
	b.toggle()
	return true
}

func (b *extTextBlock) Render(width int) []string {
	return b.cache.Render(width, extTextKey{open: b.expanded()}, func() []string { return b.render(width) })
}

func (b *extTextBlock) render(width int) []string {
	head := tui.FG(5, tui.Bold("±")) + " " + tui.Bold(tui.StripControls(b.title)) + tui.Dim(" · "+tui.StripControls(b.ext))
	out := []string{tui.Truncate(head, width, tui.Dim("…"))}
	lines := textLines(b.text)
	preview := b.preview
	if preview <= 0 {
		preview = defaultTextPreview
	}
	collapsible := len(lines) > preview
	hidden := 0
	if collapsible && !b.expanded() {
		hidden = len(lines) - preview
		lines = lines[:preview]
	}
	style := plainLine
	if b.lang == "diff" {
		style = diffLine
	}
	for _, l := range lines {
		out = append(out, tui.Truncate("  "+style(l), width, tui.Dim("…")))
	}
	switch {
	case b.expanded() && collapsible:
		out = append(out, disclosure(true, 0, ""))
	case hidden > 0:
		out = append(out, disclosure(false, hidden, "lines"))
	}
	return b.clicks(collapsible, out, collapsible)
}

// textLines splits text into lines for display: no escape sequences, tabs
// widened, line ends and trailing blank lines dropped. Leading spaces stay
// (a diff's context lines start with one).
func textLines(text string) []string {
	lines := strings.Split(tui.StripControls(tui.StripEscapes(text)), "\n")
	for i, l := range lines {
		lines[i] = strings.ReplaceAll(strings.TrimRight(l, "\r"), "\t", "   ")
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func plainLine(l string) string { return l }

// diffHeaders start the lines of a file's header in a git diff.
var diffHeaders = []string{"diff --git", "index ", "--- ", "+++ ", "new file mode", "deleted file mode",
	"old mode", "new mode", "similarity index", "rename ", "copy ", "Binary files"}

// diffLine colours a line of a unified diff: added green, removed red, hunk
// markers cyan, file headers dim.
func diffLine(l string) string {
	for _, h := range diffHeaders {
		if strings.HasPrefix(l, h) {
			return tui.Dim(l)
		}
	}
	switch {
	case strings.HasPrefix(l, "@@"):
		return tui.FG(6, l)
	case strings.HasPrefix(l, "+"):
		return tui.FG(2, l)
	case strings.HasPrefix(l, "-"):
		return tui.FG(1, l)
	}
	return l
}

// extTextStarted adds the block of an ext_text item, live and on replay.
func (a *App) extTextStarted(it *transcript.Item) {
	a.add(&extTextBlock{d: &a.details, ext: it.Ext, title: it.Title, text: it.Text, lang: it.Lang, preview: it.Preview})
}

package tui

import (
	"strings"
	"testing"
)

func TestEditorTitleOnTopRule(t *testing.T) {
	e := &Editor{Prompt: "› ", Title: "fix the parser"}
	lines := e.Render(40)
	top := StripEscapes(lines[0])
	if !strings.HasSuffix(top, "─ fix the parser ─") || VisibleWidth(lines[0]) != 40 {
		t.Fatalf("top %q (%d wide)", top, VisibleWidth(lines[0]))
	}
	if strings.Contains(StripEscapes(lines[len(lines)-1]), "fix") {
		t.Fatal("the bottom rule has no title")
	}
	e.Title = strings.Repeat("long ", 20)
	if w := VisibleWidth(e.Render(40)[0]); w != 40 {
		t.Fatalf("a long title fits: %d", w)
	}
}

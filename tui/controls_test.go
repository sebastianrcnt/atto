package tui

import (
	"strings"
	"testing"
)

const spoofMarker = "\x1b]7337;detach\x07"

func TestEditorStripsUntrustedControls(t *testing.T) {
	for _, insert := range []func(*Editor){
		func(e *Editor) { e.HandleInput(PastePrefix + "before" + spoofMarker + "\x00\x08after\nnext") },
		func(e *Editor) { e.SetText("before" + spoofMarker + "after") },
		func(e *Editor) { e.Replace(0, 0, "before"+spoofMarker+"after", 100) },
	} {
		e := NewEditor("> ")
		insert(e)
		if e.Cursor() > len([]rune(e.Text())) {
			t.Fatal("sanitized insertion left cursor past buffer")
		}
		if strings.ContainsAny(e.Text(), "\x00\x07\x08\x1b") {
			t.Fatalf("unsafe editor text %q", e.Text())
		}
		for _, row := range e.Render(120) {
			if strings.Contains(row, "\x1b]7337") {
				t.Fatalf("editor rendered marker %q", row)
			}
		}
	}
}

func TestMarkdownStripsControlsBeforeStyling(t *testing.T) {
	for _, text := range []string{spoofMarker, "`" + spoofMarker + "`", "# " + spoofMarker, "```\n" + spoofMarker + "\n```", "[link](https://example.org/" + spoofMarker + ")"} {
		for _, line := range Markdown(text, 120) {
			if strings.Contains(line, "\x1b]7337") {
				t.Fatalf("markdown emitted marker %q", line)
			}
		}
		if got := inline(text); strings.Contains(got, "\x1b]7337") {
			t.Fatalf("inline emitted marker %q", got)
		}
	}
}

func TestStripControls(t *testing.T) {
	if got := StripControls("a\x00\x07\x1b\x7f\u009db\n\t한"); got != "ab\n\t한" {
		t.Fatalf("stripped %q", got)
	}
}

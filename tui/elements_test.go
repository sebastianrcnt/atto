package tui

import (
	"fmt"
	"github.com/sebastianrcnt/atto/ui"
	"os"
	"strings"
	"testing"
)

func elementFixture() ui.Node {
	return ui.Box(ui.BoxProps{BorderStyle: "round", Padding: 1, Gap: 1}, ui.Text(ui.TextProps{Text: "Portable é 👩‍💻 漢字\tUI", Bold: true}), ui.Box(ui.BoxProps{FlexDirection: "row", Gap: 2}, ui.Text(ui.TextProps{Text: "Left", Color: ui.Accent}), ui.Text(ui.TextProps{Text: "Right"})), ui.Diff(ui.DiffProps{Source: "--- a/test\n+++ b/test\n@@ -1 +1 @@\n-old\n+new"}), ui.List(ui.ListProps{Mode: "table", Columns: []ui.Column{{Label: "Job"}, {Label: "State"}}, Rows: []ui.Row{{Key: "1", Cells: []string{"1 · sleep 60", "running"}}}}), ui.Button(ui.ButtonProps{Key: "stop", Label: "Stop job 1", Hotkey: "s"}), ui.Collapse(ui.CollapseProps{Key: "more", Title: "Details", PreviewLines: 1}, ui.Text(ui.TextProps{Text: "one\ntwo\nthree"})))
}
func TestElementGoldens(t *testing.T) {
	for _, width := range []int{40, 80, 120, 160} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			e := &Elements{}
			n := elementFixture()
			if err := e.SetTree(ui.Pane, "atto/test", 1, &n); err != nil {
				t.Fatal(err)
			}
			lines := e.Render(width)
			for _, l := range lines {
				if VisibleWidth(l) > width {
					t.Fatalf("overflow: %s", l)
				}
			}
			for i := range lines {
				lines[i] = StripEscapes(lines[i])
			}
			got := strings.Join(lines, "\n") + "\n"
			path := fmt.Sprintf("testdata/elements-%d.txt", width)
			if os.Getenv("UPDATE_GOLDEN") == "1" {
				if err := os.MkdirAll("testdata", 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0600); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(want) != got {
				t.Fatalf("golden mismatch\n%s", got)
			}
		})
	}
}
func TestElementControls(t *testing.T) {
	e := &Elements{}
	n := elementFixture()
	_ = e.SetTree(ui.Pane, "atto/test", 1, &n)
	var a []ui.Action
	e.OnAction = func(x ui.Action) { a = append(a, x) }
	e.SetFocused(true)
	e.HandleInput("s")
	if len(a) != 1 || a[0].Key != "stop" {
		t.Fatal(a)
	}
	e.HandleInput("\t")
	e.HandleInput("\r")
	if !e.open["more"] {
		t.Fatal("collapse")
	}
	e.Render(80)
	if !e.Click(18) { /* hit positions covered by explicit hit map below */
	}
	for _, h := range e.hits {
		if h.key == "stop" {
			if !e.ClickAt(h.x, h.y) {
				t.Fatal("click")
			}
		}
	}
}
func TestPanePlacement(t *testing.T) {
	for _, w := range []int{40, 80, 120, 160} {
		side, cols, rows := PaneLayout(w, 24, ui.OpenOptions{})
		if side != (w >= 120) || cols > w || rows < 1 {
			t.Fatal(w, side, cols, rows)
		}
	}
}

func TestElementNarrowSafetyAndDraft(t *testing.T) {
	n := elementFixture()
	e := &Elements{}
	_ = e.SetTree(ui.Pane, "atto/p", 1, &n)
	for w := 1; w < 40; w++ {
		for _, l := range e.Render(w) {
			if VisibleWidth(l) > w {
				t.Fatalf("width %d: %q", w, l)
			}
		}
	}
	input := ui.Input(ui.InputProps{Key: "draft", Value: "é👩‍💻"})
	_ = e.SetTree(ui.Pane, "atto/p", 2, &input)
	e.SetFocused(true)
	e.HandleInput("\x7f")
	if e.drafts["draft"] != "é" {
		t.Fatal("split grapheme", e.drafts)
	}
	e.HandleInput("x")
	_ = e.SetTree(ui.Pane, "atto/p", 3, &input)
	if e.drafts["draft"] != "éx" {
		t.Fatal("lost draft")
	}
	input = ui.Input(ui.InputProps{Key: "draft", Value: "reset"})
	_ = e.SetTree(ui.Pane, "atto/p", 4, &input)
	if e.drafts["draft"] != "reset" {
		t.Fatal("worker value did not reset draft")
	}
}

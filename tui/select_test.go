package tui

import (
	"strings"
	"testing"
)

func numbered(n int) []SelectItem {
	var out []SelectItem
	for i := range n {
		out = append(out, SelectItem{Label: "item" + string(rune('a'+i)), Detail: "d"})
	}
	return out
}

func joinPlain(lines []string) string { return StripEscapes(strings.Join(lines, "\n")) }

func TestSelectListWindowAndIndicator(t *testing.T) {
	l := &SelectList{Items: numbered(10), MaxVisible: 5}
	got := l.Render(80)
	if len(got) != 6 || !strings.Contains(joinPlain(got[5:]), "(1/10)") {
		t.Fatalf("5 rows and an indicator: %q", got)
	}
	l.Selected = 5 // centred: rows 3..7
	got = l.Render(80)
	if !strings.Contains(joinPlain(got[0:1]), "itemd") || !strings.Contains(joinPlain(got[2:3]), "› itemf") || !strings.Contains(joinPlain(got[5:]), "(6/10)") {
		t.Fatalf("the window centres the selection: %q", got)
	}
	l.Selected = 9
	got = l.Render(80)
	if !strings.Contains(joinPlain(got[0:1]), "itemf") || !strings.Contains(joinPlain(got[4:5]), "itemj") {
		t.Fatalf("the window stops at the end: %q", got)
	}
	short := &SelectList{Items: numbered(3), MaxVisible: 5}
	if got := short.Render(80); len(got) != 3 {
		t.Fatalf("no indicator when everything fits: %q", got)
	}
}

func TestSelectListWrapAndKeys(t *testing.T) {
	var picked string
	l := &SelectList{Items: numbered(4), MaxVisible: 2, OnSelect: func(it SelectItem) { picked = it.Label }}
	l.HandleInput("\x1b[A")
	if l.Selected != 3 {
		t.Fatalf("up wraps to the last: %d", l.Selected)
	}
	l.HandleInput("\x1b[B")
	if l.Selected != 0 {
		t.Fatalf("down wraps to the first: %d", l.Selected)
	}
	for range 3 {
		l.HandleInput("\x1b[6~")
	}
	if l.Selected != 3 {
		t.Fatalf("page down clamps: %d", l.Selected)
	}
	l.HandleInput("\r")
	if picked != "itemd" {
		t.Fatalf("enter picks: %q", picked)
	}
}

func TestSelectListFilterAndSource(t *testing.T) {
	l := &SelectList{Items: []SelectItem{{Label: "alpha"}, {Label: "beta"}}, Filterable: true}
	l.HandleInput("b")
	if v := l.Visible(); len(v) != 1 || v[0].Label != "beta" {
		t.Fatalf("typing filters: %v", v)
	}
	if !strings.Contains(joinPlain(l.Render(80)), "Filter: b") {
		t.Fatal("filter line shows the text")
	}

	text := ""
	s := &SelectList{Items: []SelectItem{{Label: "alpha", Value: "alpha"}, {Label: "amber", Value: "amber"}, {Label: "beta", Value: "beta"}}, Filterable: true}
	s.Source = func() string { return text }
	s.Match = func(it SelectItem, q string) bool { return strings.HasPrefix(it.Value, q) }
	s.HandleInput("x") // typing is the source's business, not the list's
	text = "a"
	s.Visible()
	s.Selected = 1
	if v := s.Visible(); len(v) != 2 || s.Selected != 1 {
		t.Fatalf("same text keeps the selection: %v %d", v, s.Selected)
	}
	text = "al"
	if v := s.Visible(); len(v) != 1 || s.Selected != 0 {
		t.Fatalf("a changed source resets the selection: %v %d", v, s.Selected)
	}
	text = "zz"
	if s.Render(80) != nil {
		t.Fatal("an external list with no match renders nothing")
	}
}

func TestSelectListCustomRows(t *testing.T) {
	l := &SelectList{Items: numbered(3), MaxVisible: 2, RenderRow: func(it SelectItem, sel bool, w int) []string {
		return []string{it.Label, "  second line"}
	}}
	if got := l.Render(80); len(got) != 5 { // 2 rows x 2 lines + indicator
		t.Fatalf("custom rows may span lines: %q", got)
	}
}

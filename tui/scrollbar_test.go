package tui

import (
	"fmt"
	"strings"
	"testing"
)

// noBar drops the scrollbar column from screen rows.
func noBar(rows []string) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		r = strings.TrimSuffix(r, barTrack)
		r = strings.TrimSuffix(r, barThumb)
		out[i] = strings.TrimRight(r, " ")
	}
	return out
}

// barCol is the glyph in the last column of each screen row: # thumb,
// . track, space none.
func barCol(rows []string) string {
	var b strings.Builder
	for _, r := range rows {
		switch {
		case strings.HasSuffix(r, barThumb):
			b.WriteString("#")
		case strings.HasSuffix(r, barTrack):
			b.WriteString(".")
		default:
			b.WriteString(" ")
		}
	}
	return b.String()
}

func bodyLines(n int) []string {
	var l []string
	for i := range n {
		l = append(l, fmt.Sprintf("line %d", i))
	}
	return l
}

func TestThumbGeometry(t *testing.T) {
	cases := []struct {
		total, rows, start int
		top, size          int
	}{
		{10, 10, 0, 0, 10}, // fits: the thumb is the whole track
		{20, 10, 0, 0, 5},  // top
		{20, 10, 10, 5, 5}, // bottom
		{20, 10, 5, 3, 5},  // middle (2.5 rounds up)
		{40, 10, 15, 4, 3}, // middle, smaller thumb
		{1000000, 10, 0, 0, 1},
		{1000000, 10, 999990, 9, 1}, // huge: one row, at the bottom
		{1000000, 10, 500000, 5, 1},
		{1000000, 10, 1, 0, 1},      // barely scrolled stays at the top
		{1000000, 10, 999989, 9, 1}, // barely above the bottom
		{11, 10, 0, 0, 9},           // tiny overflow: the thumb nearly fills
		{11, 10, 1, 1, 9},
		{3, 1, 1, 0, 1}, // one-row track
	}
	for _, c := range cases {
		top, size := thumbGeometry(c.total, c.rows, c.start)
		if top != c.top || size != c.size {
			t.Errorf("thumbGeometry(%d,%d,%d) = %d,%d want %d,%d", c.total, c.rows, c.start, top, size, c.top, c.size)
		}
	}
}

func TestScrollbarShownOnlyWhenOverflowing(t *testing.T) {
	r := newSelRig(t, 20, 6, "a", "b", "c") // 5 body rows
	if s := barCol(r.v.screenRows()); strings.TrimSpace(s) != "" || r.ui.bar.on {
		t.Fatalf("bar for content that fits: %q", s)
	}
	r.body.l = bodyLines(10)
	r.ui.RenderNow()
	// 10 lines over 5 rows: a thumb of 3 rows (rounded), at the bottom
	// while following output.
	if s := barCol(r.v.screenRows()); s != "..###"+" " {
		t.Fatalf("bar at the bottom: %q", s)
	}
	r.ui.ScrollBy(100)
	r.ui.RenderNow()
	if s := barCol(r.v.screenRows()); s != "###.."+" " {
		t.Fatalf("bar at the top: %q", s)
	}
	if got := noBar(r.v.screenRows())[0]; got != "line 0" {
		t.Fatalf("text %q", got)
	}
}

func TestScrollbarNotInInline(t *testing.T) {
	ui, v, c := setup(20, 5)
	c.l = bodyLines(30)
	ui.RenderNow()
	for _, row := range v.screenRows() {
		if strings.Contains(row, barTrack) || strings.Contains(row, barThumb) {
			t.Fatalf("inline mode drew a bar: %q", row)
		}
	}
}

func TestScrollbarClickAndDrag(t *testing.T) {
	r := newSelRig(t, 20, 11, bodyLines(100)...) // 10 track rows, thumb of 1
	// Following output: the thumb is on the last row. Click the track at row
	// 1: jump to the top.
	r.later()
	r.click(20, 1)
	r.ui.RenderNow()
	if got := noBar(r.v.screenRows())[0]; got != "line 0" {
		t.Fatalf("click at the top of the track: first row %q", got)
	}
	if len(r.body.clicked) != 0 {
		t.Fatalf("a bar click reached the body: %v", r.body.clicked)
	}
	// Click the middle of the track.
	r.click(20, 6)
	r.ui.RenderNow()
	if first := r.ui.viewStart; first < 40 || first > 55 {
		t.Fatalf("middle click scrolled to line %d", first)
	}
	// Grab the thumb and drag to the bottom.
	r.later()
	r.press(20, r.ui.bar.top+1)
	r.move(20, 10)
	r.ui.RenderNow()
	if r.ui.ScrollOffset() != 0 {
		t.Fatalf("dragged to the bottom, scroll %d", r.ui.ScrollOffset())
	}
	// Dragging past the end of the track clamps; release ends the drag.
	r.move(20, 1)
	r.ui.RenderNow()
	if r.ui.viewStart != 0 {
		t.Fatalf("dragged to the top, view starts at %d", r.ui.viewStart)
	}
	r.release(20, 1)
	r.move(20, 10) // no button held: ignored
	r.ui.RenderNow()
	if r.ui.viewStart != 0 {
		t.Fatal("scrolled after the button was released")
	}
}

func TestScrollbarDragKeepsGrabOffset(t *testing.T) {
	r := newSelRig(t, 20, 11, bodyLines(20)...) // thumb of 5 of 10 rows
	r.ui.ScrollBy(100)
	r.ui.RenderNow() // thumb rows 0..4
	r.later()
	r.press(20, 3) // grab the middle of the thumb
	if r.ui.viewStart != 0 {
		t.Fatalf("pressing the thumb moved the view to %d", r.ui.viewStart)
	}
	r.move(20, 5) // two rows down
	r.ui.RenderNow()
	if r.ui.bar.top != 2 {
		t.Fatalf("thumb top %d after dragging two rows", r.ui.bar.top)
	}
}

func TestScrollbarPressNeverSelects(t *testing.T) {
	r := newSelRig(t, 20, 11, bodyLines(100)...)
	r.later()
	r.press(20, 3)
	r.move(5, 8) // off the bar, over the text
	r.release(5, 8)
	r.ui.RenderNow()
	if r.ui.sel.active || len(r.copied) != 0 {
		t.Fatalf("a bar drag selected: %+v copied %q", r.ui.sel, r.copied)
	}
	// The same drag started on the text does select.
	r.drag(1, 2, 5, 3)
	if !r.ui.sel.active {
		t.Fatal("control: a text drag should select")
	}
}

func TestScrollbarExcludedFromSelection(t *testing.T) {
	for _, pad := range []int{0, 1} {
		r := newSelRig(t, 20, 6, bodyLines(30)...)
		r.ui.PaddingX = pad
		r.ui.RenderNow()
		r.drag(1, 1, 20, 3) // out to the bar column
		got := r.lastCopy(t)
		if strings.ContainsAny(got, barTrack+barThumb) {
			t.Fatalf("pad %d: copied the bar: %q", pad, got)
		}
		for _, row := range r.reversed()[:3] {
			if strings.ContainsAny(row, barTrack+barThumb) {
				t.Fatalf("pad %d: highlighted the bar: %q", pad, row)
			}
		}
	}
}

func TestScrollbarWideCharAtRightEdge(t *testing.T) {
	r := newSelRig(t, 10, 4, "x", "y", "z", strings.Repeat("中", 12))
	rows := r.v.screenRows()[:3]
	// 9 usable columns: four wide characters and a blank, then the bar.
	if !strings.HasPrefix(rows[2], "中中中中") || strings.HasPrefix(rows[2], "中中中中中") {
		t.Fatalf("row 2: %q", rows[2])
	}
	if !strings.HasSuffix(rows[2], barTrack) && !strings.HasSuffix(rows[2], barThumb) {
		t.Fatalf("no bar after wide characters: %q", rows[2])
	}
	if w := VisibleWidth(r.ui.prevFrame[2]); w != 10 {
		t.Fatalf("frame row width %d", w)
	}
}

func TestScrollbarBothRepaintModes(t *testing.T) {
	for _, full := range []bool{false, true} {
		r := newSelRig(t, 20, 6, bodyLines(30)...)
		r.ui.FullRepaint = full
		r.ui.RenderNow()
		before := barCol(r.v.screenRows())
		r.ui.ScrollBy(10)
		r.ui.RenderNow()
		after := barCol(r.v.screenRows())
		if before == after || !strings.Contains(before, "#") || !strings.Contains(after, "#") {
			t.Fatalf("full=%v: bar did not move: %q -> %q", full, before, after)
		}
	}
}

func TestScrollbarResize(t *testing.T) {
	r := newSelRig(t, 20, 6, bodyLines(30)...)
	r.v.resize(30, 12)
	r.ui.RenderNow()
	if r.ui.bar.col != 30 || r.ui.bar.rows != 11 {
		t.Fatalf("bar after resize: %+v", r.ui.bar)
	}
	r.body.l = bodyLines(5)
	r.ui.RenderNow()
	if r.ui.bar.on || strings.Contains(barCol(r.v.screenRows()), "#") {
		t.Fatal("bar still shown for content that fits")
	}
}

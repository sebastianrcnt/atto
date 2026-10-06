package tui

import (
	"slices"
	"strings"
	"testing"
)

func plain(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = StripEscapes(l)
	}
	return out
}

func TestMarkdownBlocks(t *testing.T) {
	src := "# Title\n\nSome **bold** and `code` text\nwrapped softly.\n\n- one\n- two\n  - nested\n1. first\n\n```go\nfunc main() {\n\tx := 1\n}\n```\n\n> quoted\n\n---\n\n| a | b |\n|---|--:|\n| 1 | 22 |"
	got := plain(Markdown(src, 30))
	want := []string{
		"Title",
		"",
		"Some bold and code text",
		"wrapped softly.",
		"",
		"• one",
		"• two",
		"  ◦ nested",
		"1. first",
		"",
		"╭ go",
		"│ func main() {",
		"│     x := 1",
		"│ }",
		"",
		"│ quoted",
		"",
		strings.Repeat("─", 30),
		"",
		"┌───┬────┐",
		"│ a │  b │",
		"├───┼────┤",
		"│ 1 │ 22 │",
		"└───┴────┘",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestMarkdownInline(t *testing.T) {
	cases := map[string]string{
		"**b** *i* ***bi*** ~~s~~": "\x1b[1mb\x1b[22m \x1b[3mi\x1b[23m \x1b[1m\x1b[3mbi\x1b[23m\x1b[22m \x1b[9ms\x1b[29m",
		"snake_case_name":          "snake_case_name",
		"2 * 3 * 4":                "2 * 3 * 4",
		"**unclosed":               "**unclosed",
		`\*lit\*`:                  "*lit*",
		"`a*b*c`":                  codeStyleOn + "a*b*c" + codeStyleOff,
	}
	for in, want := range cases {
		if got := inline(in); got != want {
			t.Errorf("inline(%q) = %q, want %q", in, got, want)
		}
	}
	if got := StripEscapes(inline("[docs](https://x.y)")); got != "docs" {
		t.Errorf("link text %q", got)
	}
}

func TestMarkdownWidthAndStreaming(t *testing.T) {
	src := "| col | another long column |\n|---|---|\n| some long cell text here | x |\n\n```\nunterminated code that is long enough to wrap around"
	for _, w := range []int{12, 20, 40} {
		for _, l := range Markdown(src, w) {
			if VisibleWidth(l) > w {
				t.Errorf("width %d: line %q is %d wide", w, StripEscapes(l), VisibleWidth(l))
			}
		}
	}
}

func TestTableWithEmptyHeader(t *testing.T) {
	got := plain(Markdown("| | |\n|---|---|\n| OS | Windows |\n| CPU | 19% |\n", 40))
	want := []string{
		"┌─────┬─────────┐",
		"│ OS  │ Windows │",
		"├─────┼─────────┤",
		"│ CPU │ 19%     │",
		"└─────┴─────────┘",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s", strings.Join(got, "\n"))
	}
}

// A table never panics or overflows at any width, wide characters and
// long words included.
func TestTableAtEveryWidth(t *testing.T) {
	md := "| 항목 | 상태 | 비고 |\n|---|:---:|---:|\n| 연료 시스템 | 완료 | aircraft_systems_fuel_management |\n| 지상 서비스 | 진행 중 | 🛫 ground |\n| x | y | z |"
	for w := 1; w <= 80; w++ {
		for _, l := range Markdown(md, w) {
			// Below 4 columns a wide character can't fit; no panic is enough.
			if vw := VisibleWidth(l); w >= 4 && vw > w {
				t.Fatalf("width %d: line is %d wide: %q", w, vw, StripEscapes(l))
			}
		}
	}
}

// A narrow terminal shrinks table columns to fit, but never below the
// widest character a column holds: a two-column character in a cell one
// column wide would be cut away. With no room even for that, the table is
// drawn as "header: value" lines.
func TestMarkdownTableNarrow(t *testing.T) {
	src := "| ab | bc |\n|---|---|\n| 한 | xx |"
	got := plain(Markdown(src, 10))
	want := []string{
		"┌────┬───┐",
		"│ ab │ b │",
		"│    │ c │",
		"├────┼───┤",
		"│ 한 │ x │",
		"│    │ x │",
		"└────┴───┘",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s", strings.Join(got, "\n"))
	}
	if got := plain(Markdown(src, 9)); !slices.Equal(got, []string{"ab: 한", "bc: xx"}) {
		t.Errorf("no room for a grid: got %q", got)
	}
}

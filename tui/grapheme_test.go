package tui

import (
	"slices"
	"strings"
	"testing"
)

// These expectations use narrow East Asian Ambiguous characters and emoji
// presentation for VS16 sequences. Tabs follow atto's three-column policy,
// not terminal tab stops. Escape widths measure text, not cursor displacement.
func TestGraphemeFormatting(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		width    int
		plain    string
		controls string
	}{
		{"CJK", "漢字", 4, "漢字", "漢字"},
		{"family ZWJ", "👨‍👩‍👧‍👦", 2, "👨‍👩‍👧‍👦", "👨‍👩‍👧‍👦"},
		{"woman technologist ZWJ", "👩‍💻", 2, "👩‍💻", "👩‍💻"},
		{"man health worker ZWJ", "👨‍⚕️", 2, "👨‍⚕️", "👨‍⚕️"},
		{"US flag", "🇺🇸", 2, "🇺🇸", "🇺🇸"},
		{"Korea flag", "🇰🇷", 2, "🇰🇷", "🇰🇷"},
		{"skin tone", "👍🏽", 2, "👍🏽", "👍🏽"},
		{"profession skin tone", "👩🏽‍💻", 2, "👩🏽‍💻", "👩🏽‍💻"},
		{"digit keycap", "1️⃣", 2, "1️⃣", "1️⃣"},
		{"hash keycap", "#️⃣", 2, "#️⃣", "#️⃣"},
		{"star keycap", "*️⃣", 2, "*️⃣", "*️⃣"},
		{"combining acute", "e\u0301", 1, "e\u0301", "e\u0301"},
		{"text heart", "❤", 1, "❤", "❤"},
		{"emoji heart", "❤️", 2, "❤️", "❤️"},
		{"text warning", "⚠", 1, "⚠", "⚠"},
		{"emoji warning", "⚠️", 2, "⚠️", "⚠️"},
		{"ambiguous width", "·Ω─", 3, "·Ω─", "·Ω─"},
		{"zero width joiner", "x\u200dy", 2, "x\u200dy", "x\u200dy"},
		{"zero width space", "x\u200by", 2, "x\u200by", "x\u200by"},
		{"tabs", "a\tb", 5, "a\tb", "a\tb"},
		{"SGR", "\x1b[31m👩‍💻\x1b[0m", 2, "👩‍💻", "[31m👩‍💻[0m"},
		{"styled keycap", "\x1b[31m1️⃣\x1b[0m", 2, "1️⃣", "[31m1️⃣[0m"},
		{"OSC 8 BEL", "\x1b]8;;https://example.org\x07👩‍💻\x1b]8;;\x07", 2, "👩‍💻", "]8;;https://example.org👩‍💻]8;;"},
		{"OSC 8 ST", "\x1b]8;;https://example.org\x1b\\👩‍💻\x1b]8;;\x1b\\", 2, "👩‍💻", "]8;;https://example.org\\👩‍💻]8;;\\"},
		{"OSC 52", "a\x1b]52;c;aGk=\x07b", 2, "ab", "a]52;c;aGk=b"},
		{"cursor movement", "a\x1b[2Cb", 2, "ab", "a[2Cb"},
		{"SGR before combining mark", "e\x1b[31m\u0301", 1, "e\u0301", "e[31m\u0301"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := VisibleWidth(tc.text); got != tc.width {
				t.Errorf("VisibleWidth(%q) = %d, want %d", tc.text, got, tc.width)
			}
			if got := StripEscapes(tc.text); got != tc.plain {
				t.Errorf("StripEscapes(%q) = %q, want %q", tc.text, got, tc.plain)
			}
			if got := StripControls(tc.text); got != tc.controls {
				t.Errorf("StripControls(%q) = %q, want %q", tc.text, got, tc.controls)
			}
			// A forced cut retains the complete grapheme(s), never a partial
			// emoji or combining sequence. A fitting string stays byte-exact.
			if got := Truncate(tc.text, tc.width, "…"); got != tc.text {
				t.Errorf("Truncate fitting text = %q, want %q", got, tc.text)
			}
			plain := strings.ReplaceAll(tc.plain, "\t", "   ")
			cut := Truncate(tc.text+"XY", tc.width+1, "…")
			if got, want := StripEscapes(cut), plain+"…"; got != want {
				t.Errorf("Truncate forced cut = %q, want %q", got, want)
			}
			if got := VisibleWidth(cut); got != tc.width+1 {
				t.Errorf("truncated width = %d, want %d", got, tc.width+1)
			}
			want := []string{plain, "XY"}
			if tc.width == 1 {
				want = []string{plain, "X", "Y"}
			}
			lines := Wrap(tc.text+"XY", tc.width)
			for i, line := range lines {
				if got := VisibleWidth(line); got > tc.width {
					t.Errorf("wrapped line %q is %d wide, limit %d", line, got, tc.width)
				}
				lines[i] = StripEscapes(line)
			}
			if !slices.Equal(lines, want) {
				t.Errorf("Wrap = %q, want %q", lines, want)
			}
		})
	}
}

func TestEmojiKeycapBoundaries(t *testing.T) {
	for _, keycap := range []string{"0️⃣", "1️⃣", "2️⃣", "3️⃣", "4️⃣", "5️⃣", "6️⃣", "7️⃣", "8️⃣", "9️⃣", "#️⃣", "*️⃣"} {
		t.Run(keycap, func(t *testing.T) {
			if got := VisibleWidth(keycap + keycap); got != 4 {
				t.Errorf("two keycaps width = %d, want 4", got)
			}
			for width := 1; width <= 2; width++ {
				if got := Truncate(keycap+"XY", width, "…"); got != "…" {
					t.Errorf("Truncate at %d = %q, want ellipsis", width, got)
				}
			}
			if got, want := WrapHard(keycap+keycap, 2), []string{keycap, wrapJoin + keycap}; !slices.Equal(got, want) {
				t.Errorf("WrapHard = %q, want %q", got, want)
			}
		})
	}
}

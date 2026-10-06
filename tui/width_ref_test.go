package tui

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/rivo/uniseg"
)

// The old, allocation-heavy cells, Truncate, Wrap, WrapHard and
// StripWrapMarks, kept as a reference: the fast ones must give the same
// bytes for any input.

type oldCell struct {
	esc   string
	text  string
	width int
}

func oldCells(s string) (out []oldCell, trailing string) {
	var esc strings.Builder
	for i := 0; i < len(s); {
		if n := escapeLen(s, i); n > 0 {
			esc.WriteString(s[i : i+n])
			i += n
			continue
		}
		end := strings.IndexByte(s[i:], 0x1b)
		if end < 0 {
			end = len(s)
		} else {
			end += i
		}
		g := uniseg.NewGraphemes(s[i:end])
		for g.Next() {
			t := g.Str()
			w := g.Width()
			if t == "\t" {
				t, w = "   ", 3
			}
			out = append(out, oldCell{esc: esc.String(), text: t, width: w})
			esc.Reset()
		}
		i = end
	}
	return out, esc.String()
}

func oldTruncate(s string, width int, tail string) string {
	if VisibleWidth(s) <= width {
		return s
	}
	tw := VisibleWidth(tail)
	if tw > width {
		tail, tw = "", 0
	}
	cs, _ := oldCells(s)
	var b strings.Builder
	w := 0
	for _, c := range cs {
		if w+c.width > width-tw {
			break
		}
		b.WriteString(c.esc)
		b.WriteString(c.text)
		w += c.width
	}
	b.WriteString(Reset)
	b.WriteString(tail)
	return b.String()
}

func oldWrapHard(s string, width int) []string {
	cs, trailing := oldCells(s)
	var out []string
	var cur strings.Builder
	var st sgrState
	w := 0
	for _, c := range cs {
		if w+c.width > width && w > 0 {
			out = append(out, cur.String())
			cur.Reset()
			cur.WriteString(st.active)
			cur.WriteString(wrapJoin)
			w = 0
		}
		st.feed(c.esc)
		cur.WriteString(c.esc)
		cur.WriteString(c.text)
		w += c.width
	}
	cur.WriteString(trailing)
	return append(out, cur.String())
}

func oldStripWrapMarks(s string) string {
	if !strings.Contains(s, wrapMark) {
		return s
	}
	return strings.NewReplacer(wrapSpace, "", wrapJoin, "").Replace(s)
}

func oldWrap(text string, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	var st sgrState
	for logical := range strings.SplitSeq(text, "\n") {
		out = append(out, oldWrapLine(logical, width, &st)...)
	}
	return out
}

func oldWrapLine(line string, width int, st *sgrState) []string {
	cs, trailing := oldCells(line)
	if len(cs) == 0 {
		st.feed(trailing)
		return []string{trailing}
	}
	var lines []string
	var cur strings.Builder
	curW := 0
	start := func(mark string) {
		cur.Reset()
		cur.WriteString(st.active)
		cur.WriteString(mark)
		curW = 0
	}
	flush := func() { lines = append(lines, cur.String()) }
	start("")
	isSpace := func(c oldCell) bool { return c.text == " " }
	for i := 0; i < len(cs); {
		j := i
		sp := isSpace(cs[i])
		tokW := 0
		for j < len(cs) && isSpace(cs[j]) == sp {
			tokW += cs[j].width
			j++
		}
		tok := cs[i:j]
		i = j
		if sp {
			for _, c := range tok {
				st.feed(c.esc)
				cur.WriteString(c.esc)
				if curW > 0 && curW+c.width <= width {
					cur.WriteString(c.text)
					curW += c.width
				}
			}
			continue
		}
		if curW+tokW > width && curW > 0 {
			flush()
			start(wrapSpace)
		}
		for _, c := range tok {
			if curW+c.width > width && curW > 0 {
				flush()
				start(wrapJoin)
			}
			st.feed(c.esc)
			cur.WriteString(c.esc)
			cur.WriteString(c.text)
			curW += c.width
		}
	}
	st.feed(trailing)
	cur.WriteString(trailing)
	flush()
	for k := range lines {
		lines[k] = strings.TrimRight(lines[k], " ")
	}
	return lines
}

var refPieces = []string{"a", "b", "word", " ", "  ", "\t", "\n", "é", "é", "漢字", "😀", "👍🏽", "🇰🇷", "‍", "x‍y",
	"\x1b[1m", "\x1b[22m", "\x1b[0m", "\x1b[38;5;6m", "\x1b]8;;http://x\x07", "\x1b]8;;\x07", wrapSpace, wrapJoin, "\x1b_atto:wq\x07",
	"\x1b ", "\r", "-", "long-unbroken-token-here", "\x7f"}

func refString(r *rand.Rand) string {
	var b strings.Builder
	for range r.Intn(40) {
		b.WriteString(refPieces[r.Intn(len(refPieces))])
	}
	return b.String()
}

// TestWidthMatchesReference compares the fast functions with the
// reference ones on random mixes of text, wide and combining graphemes,
// tabs, newlines, styles, hyperlinks and wrap marks.
func TestWidthMatchesReference(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	n := 30000
	if testing.Short() {
		n = 3000
	}
	eq := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	for range n {
		s := refString(r)
		w := r.Intn(12)
		want := oldWrap(s, w)
		if got := Wrap(s, w); !eq(got, want) {
			t.Fatalf("Wrap(%q, %d) = %q, want %q", s, w, got, want)
		}
		k := 1 + r.Intn(4)
		if got, want := WrapFirst(s, w, k), want[:min(k, len(want))]; !eq(got, want) {
			t.Fatalf("WrapFirst(%q, %d, %d) = %q, want %q", s, w, k, got, want)
		}
		if got, want := WrapHard(s, w), oldWrapHard(s, w); !eq(got, want) {
			t.Fatalf("WrapHard(%q, %d) = %q, want %q", s, w, got, want)
		}
		os, ot := oldCells(s)
		ns, nt := cells(s)
		if len(os) != len(ns) || ot != nt {
			t.Fatalf("cells(%q) = %v %q, want %v %q", s, ns, nt, os, ot)
		}
		for i := range os {
			if os[i].esc != ns[i].esc || os[i].text != ns[i].text || os[i].width != ns[i].width {
				t.Fatalf("cells(%q)[%d] = %v, want %v", s, i, ns[i], os[i])
			}
		}
		if got, want := Truncate(s, w, "…"), oldTruncate(s, w, "…"); got != want {
			t.Fatalf("Truncate(%q, %d) = %q, want %q", s, w, got, want)
		}
		if got, want := StripWrapMarks(s), oldStripWrapMarks(s); got != want {
			t.Fatalf("StripWrapMarks(%q) = %q, want %q", s, got, want)
		}
	}
}

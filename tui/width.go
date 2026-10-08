package tui

import (
	"strings"
	"unsafe"

	"github.com/rivo/uniseg"
)

// Reset clears SGR attributes and closes any open OSC 8 hyperlink.
// The renderer appends it to every line so styles never bleed across lines.
const Reset = "\x1b[0m\x1b]8;;\x07"

// escapeLen returns the byte length of the terminal escape sequence starting
// at s[i], or 0 if s[i] does not start one. Handles CSI, OSC, APC/DCS/PM/SOS
// (terminated by BEL or ST) and two-byte ESC sequences.
func escapeLen(s string, i int) int {
	if i >= len(s) || s[i] != 0x1b || i+1 >= len(s) {
		return 0
	}
	switch s[i+1] {
	case '[':
		for j := i + 2; j < len(s); j++ {
			if c := s[j]; c >= 0x40 && c <= 0x7e {
				return j - i + 1
			}
		}
		return len(s) - i
	case ']', '_', 'P', '^', 'X':
		for j := i + 2; j < len(s); j++ {
			if s[j] == 0x07 {
				return j - i + 1
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j - i + 2
			}
		}
		return len(s) - i
	default:
		return 2
	}
}

// StripControls makes untrusted text safe to style and display. Newlines
// and tabs are retained; terminal controls, including ESC, are not.
func StripControls(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' || r >= 0x7f && r <= 0x9f {
			return -1
		}
		return r
	}, s)
}

// StripEscapes removes all terminal escape sequences, keeping visible text.
func StripEscapes(s string) string {
	if strings.IndexByte(s, 0x1b) < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if n := escapeLen(s, i); n > 0 {
			i += n
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// VisibleWidth returns the number of terminal columns s occupies.
// Escape sequences are zero-width and tabs count as three columns.
func VisibleWidth(s string) int {
	if w, ok := asciiWidth(s); ok {
		return w
	}
	s = StripEscapes(s)
	w := 0
	for _, r := range s {
		if r == '\t' {
			w += 3
		}
	}
	return w + uniseg.StringWidth(strings.ReplaceAll(s, "\t", "")) + keycapExtraWidth(s)
}

// keycapExtraWidth corrects uniseg's one-column width for emoji keycaps:
// an ASCII digit, # or * followed by VS16 and COMBINING ENCLOSING KEYCAP.
// Other variation-selector sequences keep uniseg's width.
func keycapExtraWidth(s string) (width int) {
	const suffix = "\ufe0f\u20e3"
	for {
		i := strings.Index(s, suffix)
		if i < 0 {
			return width
		}
		if i > 0 {
			if c := s[i-1]; c >= '0' && c <= '9' || c == '#' || c == '*' {
				width++
			}
		}
		s = s[i+len(suffix):]
	}
}

// asciiWidth is the fast path for printable ASCII mixed with escape sequences,
// which is what most styled lines look like.
func asciiWidth(s string) (int, bool) {
	w := 0
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == 0x1b:
			n := escapeLen(s, i)
			if n == 0 {
				return 0, false
			}
			i += n
			continue
		case c == '\t':
			w += 3
		case c >= 0x20 && c < 0x7f:
			w++
		default:
			return 0, false
		}
		i++
	}
	return w, true
}

// cell is one grapheme cluster together with the escape sequences that
// precede it.
type cell struct {
	esc   string // escape sequences emitted before the grapheme
	text  string
	width int
}

// cells splits s into graphemes, attaching escape sequences to the following
// grapheme. Trailing escapes are returned separately.
func cells(s string) (out []cell, trailing string) {
	sc := cellScanner{s: s}
	for {
		c, ok := sc.next()
		if !ok {
			return out, c.esc
		}
		out = append(out, c)
	}
}

// cellScanner walks a string one cell at a time without allocating: a
// cell's escapes and grapheme are substrings of the string. Graphemes are
// segmented per run of text between escapes.
type cellScanner struct {
	s     string
	i     int  // next byte
	end   int  // end of the current run
	ascii bool // the run is printable ASCII and tabs: one byte per cell
	state int  // uniseg state within the run
}

// next returns the next cell. At the end it returns false, with the
// trailing escapes in the cell's esc.
func (sc *cellScanner) next() (cell, bool) {
	var c cell
	if sc.i >= sc.end {
		start := sc.i
		for sc.i < len(sc.s) {
			n := escapeLen(sc.s, sc.i)
			if n == 0 {
				break
			}
			sc.i += n
		}
		c.esc = sc.s[start:sc.i]
		if sc.i >= len(sc.s) {
			return c, false
		}
		// The run lasts up to the next escape (a lone ESC, at the very
		// end, is text).
		sc.end = len(sc.s)
		if j := strings.IndexByte(sc.s[sc.i+1:], 0x1b); j >= 0 {
			sc.end = sc.i + 1 + j
		}
		sc.state, sc.ascii = -1, true
		for k := sc.i; k < sc.end; k++ {
			if b := sc.s[k]; (b < 0x20 || b >= 0x7f) && b != '\t' {
				sc.ascii = false
				break
			}
		}
	}
	if sc.ascii {
		c.text, c.width = sc.s[sc.i:sc.i+1], 1
		sc.i++
	} else {
		var b int
		c.text, _, b, sc.state = uniseg.StepString(sc.s[sc.i:sc.end], sc.state)
		c.width = b>>uniseg.ShiftWidth + keycapExtraWidth(c.text)
		sc.i += len(c.text)
	}
	if c.text == "\t" {
		c.text, c.width = "   ", 3
	}
	return c, true
}

// Truncate cuts s to at most width columns, appending tail (e.g. "…") when it
// had to cut. Escape sequences are preserved, and a Reset is appended on cut
// only when the kept text actually carries styles: a plain string stays plain,
// so callers that write to a pipe do not print raw escapes.
func Truncate(s string, width int, tail string) string {
	if VisibleWidth(s) <= width {
		return s
	}
	tw := VisibleWidth(tail)
	if tw > width {
		tail, tw = "", 0
	}
	var b strings.Builder
	b.Grow(len(s) + len(Reset) + len(tail))
	sc := cellScanner{s: s}
	w := 0
	styled := false
	for {
		c, ok := sc.next()
		if !ok || w+c.width > width-tw {
			break
		}
		if c.esc != "" {
			styled = true
		}
		b.WriteString(c.esc)
		b.WriteString(c.text)
		w += c.width
	}
	if styled {
		b.WriteString(Reset)
	}
	b.WriteString(tail)
	return b.String()
}

// isSGR reports whether esc is a Select Graphic Rendition sequence.
func isSGR(esc string) bool {
	return len(esc) >= 3 && esc[1] == '[' && esc[len(esc)-1] == 'm'
}

func isSGRReset(esc string) bool {
	return esc == "\x1b[m" || esc == "\x1b[0m"
}

// sgrState accumulates active SGR sequences so a wrapped continuation line can
// re-apply the style that was active where the previous line broke.
type sgrState struct{ active string }

func (s *sgrState) feed(esc string) {
	for i := 0; i < len(esc); {
		n := escapeLen(esc, i)
		if n == 0 {
			n = 1
		}
		seq := esc[i : i+n]
		if isSGR(seq) {
			if isSGRReset(seq) {
				s.active = ""
			} else {
				s.active += seq
			}
		}
		i += n
	}
}

// Soft-wrap marks: Wrap and WrapHard begin every continuation line with
// one, so a selection can copy wrapped text as the line it was. They are
// zero-width APC sequences, kept through prefixes and truncation, and the
// renderer strips them before writing (as it does CursorMarker). The mark
// sits where the continued text starts, after any prefix the caller adds.
const (
	wrapSpace = "\x1b_atto:ws\x07" // the break replaced a space
	wrapJoin  = "\x1b_atto:wj\x07" // the break split a word
	wrapMark  = "\x1b_atto:w"      // common prefix of both
)

// StripWrapMarks removes soft-wrap marks, for lines that leave the
// renderer some other way.
func StripWrapMarks(s string) string {
	i := strings.Index(s, wrapMark)
	if i < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for ; i >= 0; i = strings.Index(s, wrapMark) {
		b.WriteString(s[:i])
		s = s[i:]
		switch {
		case strings.HasPrefix(s, wrapSpace):
			s = s[len(wrapSpace):]
		case strings.HasPrefix(s, wrapJoin):
			s = s[len(wrapJoin):]
		default:
			b.WriteString(wrapMark)
			s = s[len(wrapMark):]
		}
	}
	b.WriteString(s)
	return b.String()
}

// Wrap word-wraps text to width columns. Explicit newlines are honoured,
// words longer than width are hard-broken, and styling active at a break is
// carried onto the next line. Continuation lines start with a soft-wrap
// mark (see wrapSpace).
func Wrap(text string, width int) []string { return wrapText(text, width, 0) }

// WrapFirst returns the first n lines of Wrap(text, width), and stops
// wrapping there: a cheap way to tell whether text fits in n lines.
func WrapFirst(text string, width, n int) []string { return wrapText(text, width, max(1, n)) }

// wrapText wraps text, keeping only the first limit lines unless limit is 0.
func wrapText(text string, width, limit int) []string {
	w := wrapper{width: max(1, width), limit: limit}
	size := len(text)
	if limit > 0 {
		size = min(size, limit*(w.width+16))
	}
	w.buf = make([]byte, 0, size+size/4+16)
	for !w.full() {
		i := strings.IndexByte(text, '\n')
		if i < 0 {
			w.line(text)
			break
		}
		w.line(text[:i])
		text = text[i+1:]
	}
	return w.lines()
}

// wrapper builds all the lines of a Wrap in one buffer, so wrapping
// allocates a few times per call, not per cell or per line.
type wrapper struct {
	width int
	limit int // lines wanted, or 0 for all
	st    sgrState
	buf   []byte
	cuts  []int // where each finished line ends in buf
}

// full reports whether the lines wanted are done.
func (w *wrapper) full() bool { return w.limit > 0 && len(w.cuts) >= w.limit }

// lines returns the finished lines, substrings of one string.
func (w *wrapper) lines() []string {
	out := make([]string, len(w.cuts))
	if len(w.buf) == 0 {
		return out
	}
	// buf is not written again, so the string can share its bytes.
	all := unsafe.String(&w.buf[0], len(w.buf))
	from := 0
	for i, to := range w.cuts {
		out[i], from = all[from:to], to
	}
	return out
}

// cut ends the line that began at from; trim drops its trailing spaces
// (kept before a wrap).
func (w *wrapper) cut(from int, trim bool) {
	for trim && len(w.buf) > from && w.buf[len(w.buf)-1] == ' ' {
		w.buf = w.buf[:len(w.buf)-1]
	}
	w.cuts = append(w.cuts, len(w.buf))
}

// line wraps one logical line.
func (w *wrapper) line(line string) {
	width, st := w.width, &w.st
	sc := cellScanner{s: line}
	c, ok := sc.next()
	if !ok {
		st.feed(c.esc)
		w.buf = append(w.buf, c.esc...)
		w.cut(len(w.buf), false)
		return
	}
	var from, curW int
	start := func(mark string) {
		from = len(w.buf)
		w.buf = append(w.buf, st.active...)
		w.buf = append(w.buf, mark...)
		curW = 0
	}
	start("")
	for ok {
		// The next token: a run of spaces or a run of non-spaces. Measure
		// it on a copy of the scanner, then write its cells.
		sp := c.text == " "
		tokW := c.width
		for peek := sc; ; {
			n, more := peek.next()
			if !more || (n.text == " ") != sp {
				break
			}
			tokW += n.width
		}
		if !sp && curW+tokW > width && curW > 0 {
			if w.cut(from, true); w.full() {
				return
			}
			start(wrapSpace) // tokens alternate, so a space run came before
		}
		for {
			if sp {
				// Spaces at a break point are dropped; otherwise kept if they fit.
				st.feed(c.esc)
				w.buf = append(w.buf, c.esc...)
				if curW > 0 && curW+c.width <= width {
					w.buf = append(w.buf, c.text...)
					curW += c.width
				}
			} else {
				if curW+c.width > width && curW > 0 {
					if w.cut(from, true); w.full() {
						return
					}
					start(wrapJoin)
				}
				st.feed(c.esc)
				w.buf = append(w.buf, c.esc...)
				w.buf = append(w.buf, c.text...)
				curW += c.width
			}
			if c, ok = sc.next(); !ok || (c.text == " ") != sp {
				break
			}
		}
	}
	st.feed(c.esc) // trailing escapes
	w.buf = append(w.buf, c.esc...)
	w.cut(from, true)
}

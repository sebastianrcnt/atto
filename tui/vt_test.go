package tui

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// vterm is a tiny terminal emulator that understands exactly the sequences
// the renderer emits. It keeps scrollback so tests can assert that the full
// transcript (scrollback + screen) matches what was rendered.
type vterm struct {
	w, h       int
	screen     [][]rune
	scrollback []string
	r, c       int
	cursorOn   bool
}

func newVterm(w, h int) *vterm {
	v := &vterm{w: w, h: h}
	v.screen = make([][]rune, h)
	for i := range v.screen {
		v.screen[i] = blankRow(w)
	}
	return v
}

func blankRow(w int) []rune {
	r := make([]rune, w)
	for i := range r {
		r[i] = ' '
	}
	return r
}

func (v *vterm) Start(func(string), func()) error { return nil }
func (v *vterm) Stop()                            {}
func (v *vterm) Size() (int, int)                 { return v.w, v.h }

// resize mimics a terminal that keeps the bottom of the screen anchored.
func (v *vterm) resize(w, h int) {
	rows := v.rows()
	v.w, v.h = w, h
	v.scrollback = nil
	v.screen = nil
	start := max(0, len(rows)-h)
	v.scrollback = append(v.scrollback, rows[:start]...)
	for _, r := range rows[start:] {
		row := blankRow(w)
		copy(row, []rune(r))
		v.screen = append(v.screen, row)
	}
	for len(v.screen) < h {
		v.screen = append(v.screen, blankRow(w))
	}
	v.r = min(v.r, h-1)
}

func (v *vterm) lineFeed() {
	if v.r < v.h-1 {
		v.r++
		return
	}
	v.scrollback = append(v.scrollback, rowString(v.screen[0]))
	copy(v.screen, v.screen[1:])
	v.screen[v.h-1] = blankRow(v.w)
}

func (v *vterm) Write(s string) {
	for i := 0; i < len(s); {
		switch s[i] {
		case 0x1b:
			n := escapeLen(s, i)
			v.escape(s[i : i+n])
			i += n
		case '\r':
			v.c = 0
			i++
		case '\n':
			v.lineFeed()
			i++
		default:
			r, n := utf8.DecodeRuneInString(s[i:])
			v.put(r)
			i += n
		}
	}
}

// put draws one rune. A wide rune takes two cells (the second holds 0), and
// overwriting half of a wide rune blanks the other half, as real terminals do.
func (v *vterm) put(r rune) {
	w := VisibleWidth(string(r))
	if w == 0 {
		return
	}
	for k := range w {
		c := v.c + k
		if c >= v.w {
			break
		}
		row := v.screen[v.r]
		if row[c] == 0 && c > 0 && row[c-1] != 0 { // right half of a wide rune
			row[c-1] = ' '
		}
		if c+1 < v.w && row[c+1] == 0 { // left half of a wide rune
			row[c+1] = ' '
		}
		if k == 0 {
			row[c] = r
		} else {
			row[c] = 0
		}
	}
	v.c += w
}

func rowString(r []rune) string {
	var b strings.Builder
	for _, c := range r {
		if c != 0 {
			b.WriteRune(c)
		}
	}
	return strings.TrimRight(b.String(), " ")
}

func (v *vterm) escape(seq string) {
	if len(seq) < 3 || seq[1] != '[' {
		return // OSC/APC: ignore
	}
	final := seq[len(seq)-1]
	params := seq[2 : len(seq)-1]
	if strings.HasPrefix(params, "?") {
		if params == "?25" {
			v.cursorOn = final == 'h'
		}
		return
	}
	if final == 'H' {
		v.r, v.c = 0, 0
		if r, c, ok := strings.Cut(params, ";"); ok {
			rn, _ := strconv.Atoi(r)
			cn, _ := strconv.Atoi(c)
			v.r, v.c = rn-1, cn-1
		}
		return
	}
	n, err := strconv.Atoi(params)
	if err != nil {
		n = 1
	}
	switch final {
	case 'A':
		v.r = max(0, v.r-n)
	case 'B':
		v.r = min(v.h-1, v.r+n)
	case 'G':
		v.c = n - 1
	case 'K':
		if params == "2" {
			v.screen[v.r] = blankRow(v.w)
		}
	case 'J':
		switch params {
		case "2":
			for i := range v.screen {
				v.screen[i] = blankRow(v.w)
			}
		case "3":
			v.scrollback = nil
		}
	}
}

// rows returns scrollback followed by screen rows, with trailing blank rows
// trimmed.
func (v *vterm) rows() []string {
	out := append([]string(nil), v.scrollback...)
	for _, r := range v.screen {
		out = append(out, rowString(r))
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// screenRows returns just the visible screen, untrimmed in height.
func (v *vterm) screenRows() []string {
	out := make([]string, len(v.screen))
	for i, r := range v.screen {
		out[i] = rowString(r)
	}
	return out
}

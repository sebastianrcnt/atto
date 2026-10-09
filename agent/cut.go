package agent

import (
	"fmt"
	"strings"
)

// outView is a command's output as the model and the screen see it: the
// start and the end of a text that may be far too long to hold. It supports
// what is done to output before the model reads it (tidy, appending a
// line) and cutting its middle, with the same results as doing those on
// the whole text.
type outView struct {
	keep int    // the most head and tail hold
	head string // the first bytes (the whole text if short)
	tail string // the last bytes, ending the text
	n    int    // the size of the text
	nl   int    // its line feeds
}

// viewOf is the view of a text held whole.
func viewOf(s string) outView {
	return outView{keep: max(len(s), int(maxOutputBytes.Load())), head: s, tail: s, n: len(s), nl: strings.Count(s, "\n")}
}

// whole returns the text if the view holds all of it.
func (v outView) whole() (string, bool) {
	if v.n <= len(v.head) {
		return v.head[:v.n], true
	}
	if off := v.n - len(v.tail); off <= len(v.head) {
		return v.head + v.tail[len(v.head)-off:], true
	}
	return "", false
}

// preview is the text if the view holds all of it, and otherwise its start
// and end around a marker.
func (v outView) preview() string {
	if s, ok := v.whole(); ok {
		return s
	}
	return fmt.Sprintf("%s\n[… %d bytes omitted …]\n%s", v.head, v.n-len(v.head)-len(v.tail), v.tail)
}

// tidy is the view of tidy(text): no trailing lines of white space.
func (v outView) tidy() outView {
	lines := strings.Split(v.tail, "\n")
	k := len(lines)
	for k > 0 && strings.TrimSpace(lines[k-1]) == "" {
		k--
	}
	if k == len(lines) {
		return v
	}
	kept := strings.Join(lines[:k], "\n")
	v.n -= len(v.tail) - len(kept)
	v.nl -= (len(lines) - 1) - max(k-1, 0)
	v.tail = kept
	v.head = v.head[:min(len(v.head), v.n)]
	return v
}

// trimNL is the view of the text without its trailing line feeds.
func (v outView) trimNL() outView {
	k := len(v.tail) - len(strings.TrimRight(v.tail, "\n"))
	if k == 0 {
		return v
	}
	v.tail = v.tail[:len(v.tail)-k]
	v.n -= k
	v.nl -= k
	v.head = v.head[:min(len(v.head), v.n)]
	return v
}

// appendString is the view of the text followed by s.
func (v outView) appendString(s string) outView {
	if s == "" {
		return v
	}
	if len(v.head) == v.n && len(v.head) < v.keep {
		v.head += s
		v.head = v.head[:min(len(v.head), v.keep)]
	}
	v.tail += s
	if len(v.tail) > v.keep {
		v.tail = v.tail[len(v.tail)-v.keep:]
	}
	v.n += len(s)
	v.nl += strings.Count(s, "\n")
	return v
}

// lastLines returns up to n last lines and the number of lines. A text
// without lines has none.
func (v outView) lastLines(n int) ([]string, int) {
	text := v.tail
	if s, ok := v.whole(); ok {
		text = s
	}
	lines := strings.Split(text, "\n")
	total := v.nl + 1
	if len(lines) == 1 && lines[0] == "" {
		return nil, 0
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, total
}

// cut keeps the first and last limit/2 bytes (on line boundaries) of a
// text longer than limit. note starts the message saying so, without its
// closing bracket. cut is false, and body the text, when it is short enough.
func (v outView) cut(limit int) (body, note string, cut bool) {
	if v.n <= limit {
		s, _ := v.whole()
		return s, "", false
	}
	half := limit / 2
	head := v.head[:min(half, len(v.head))]
	if i := strings.LastIndexByte(head, '\n'); i > 0 {
		head = head[:i]
	}
	tail := v.tail[max(0, len(v.tail)-half):]
	if i := strings.IndexByte(tail, '\n'); i >= 0 && i < len(tail)-1 {
		tail = tail[i+1:]
	}
	total := v.nl + 1
	omitted := total - (strings.Count(head, "\n") + 1) - (strings.Count(tail, "\n") + 1)
	note = fmt.Sprintf("[output truncated: %d lines, ~%d tokens; showing the start and the end", total, v.n/4)
	return fmt.Sprintf("%s\n[… %d lines omitted …]\n%s", head, max(omitted, 0), tail), note, true
}

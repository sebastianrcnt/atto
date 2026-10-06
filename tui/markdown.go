package tui

import (
	"regexp"
	"strings"
)

// Markdown renders a subset of CommonMark/GFM to styled terminal lines:
// headings, paragraphs, emphasis, inline code, links, fenced code blocks,
// lists (nested, ordered/unordered, task items), blockquotes, rules and
// tables. It is tolerant of incomplete input so it can render while streaming.
func Markdown(src string, width int) []string {
	src = StripControls(strings.ReplaceAll(src, "\r\n", "\n"))
	width = max(width, 4)
	r := &mdRenderer{width: width}
	r.blocks(strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n"), width)
	// Trim trailing blank lines.
	for len(r.out) > 0 && r.out[len(r.out)-1] == "" {
		r.out = r.out[:len(r.out)-1]
	}
	return r.out
}

var (
	reHeading  = regexp.MustCompile(`^ {0,3}(#{1,6})\s+(.*?)\s*#*\s*$`)
	reFence    = regexp.MustCompile("^ {0,3}(```+|~~~+)\\s*([^`\\s]*)")
	reRule     = regexp.MustCompile(`^ {0,3}(-[ \t]*){3,}$|^ {0,3}(\*[ \t]*){3,}$|^ {0,3}(_[ \t]*){3,}$`)
	reQuote    = regexp.MustCompile(`^ {0,3}> ?(.*)$`)
	reListItem = regexp.MustCompile(`^(\s*)([-*+]|\d{1,9}[.)])\s+(.*)$`)
	reTableSep = regexp.MustCompile(`^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$`)
	reTask     = regexp.MustCompile(`^\[([ xX])\]\s+`)
)

type mdRenderer struct {
	width int
	out   []string
}

func (r *mdRenderer) emit(lines ...string) { r.out = append(r.out, lines...) }

// blank adds a separating blank line unless one is already there.
func (r *mdRenderer) blank() {
	if len(r.out) > 0 && r.out[len(r.out)-1] != "" {
		r.out = append(r.out, "")
	}
}

func isBlank(s string) bool { return strings.TrimSpace(s) == "" }

// startsBlock reports whether line begins a non-paragraph block.
func startsBlock(line string) bool {
	return reHeading.MatchString(line) || reFence.MatchString(line) || reRule.MatchString(line) ||
		reQuote.MatchString(line) || reListItem.MatchString(line)
}

func (r *mdRenderer) blocks(lines []string, width int) {
	for i := 0; i < len(lines); {
		line := lines[i]
		switch {
		case isBlank(line):
			i++

		case reFence.MatchString(line):
			m := reFence.FindStringSubmatch(line)
			fence := m[1]
			var code []string
			i++
			for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), fence[:3]) {
				code = append(code, lines[i])
				i++
			}
			i++ // closing fence (absent while streaming)
			r.blank()
			r.codeBlock(code, m[2], width)
			r.blank()

		case reHeading.MatchString(line):
			m := reHeading.FindStringSubmatch(line)
			r.blank()
			text := inline(m[2])
			switch len(m[1]) {
			case 1:
				text = Bold(FG(5, text))
			case 2:
				text = Bold(FG(6, text))
			default:
				text = Bold(text)
			}
			r.emit(Wrap(text, width)...)
			r.blank()
			i++

		case reRule.MatchString(line):
			r.blank()
			r.emit(Dim(strings.Repeat("─", width)))
			r.blank()
			i++

		case reQuote.MatchString(line):
			var inner []string
			for i < len(lines) && reQuote.MatchString(lines[i]) {
				inner = append(inner, reQuote.FindStringSubmatch(lines[i])[1])
				i++
			}
			sub := &mdRenderer{width: width - 2}
			sub.blocks(inner, width-2)
			for len(sub.out) > 0 && sub.out[len(sub.out)-1] == "" {
				sub.out = sub.out[:len(sub.out)-1]
			}
			r.blank()
			for _, l := range sub.out {
				r.emit(Dim("│ ") + Italic(l))
			}
			r.blank()

		case reListItem.MatchString(line):
			i = r.list(lines, i, width)

		case i+1 < len(lines) && strings.Contains(line, "|") && reTableSep.MatchString(lines[i+1]):
			rows := [][]string{splitRow(line)}
			aligns := tableAligns(lines[i+1])
			i += 2
			for i < len(lines) && strings.Contains(lines[i], "|") && !isBlank(lines[i]) {
				rows = append(rows, splitRow(lines[i]))
				i++
			}
			r.blank()
			r.table(rows, aligns, width)
			r.blank()

		default:
			var para []string
			for i < len(lines) && !isBlank(lines[i]) && (len(para) == 0 || !startsBlock(lines[i])) {
				l := lines[i]
				// Hard line breaks: two trailing spaces or a backslash.
				if strings.HasSuffix(l, "  ") || strings.HasSuffix(l, "\\") {
					l = strings.TrimRight(strings.TrimSuffix(l, "\\"), " ") + "\n"
				} else {
					l = strings.TrimSpace(l) + " "
				}
				para = append(para, l)
				i++
			}
			r.blank()
			text := strings.TrimSpace(strings.Join(para, ""))
			r.emit(Wrap(inline(text), width)...)
			r.blank()
		}
	}
}

func (r *mdRenderer) codeBlock(code []string, lang string, width int) {
	bar := Dim("│ ")
	if lang != "" {
		r.emit(Dim("╭ " + lang))
	}
	for _, l := range code {
		l = strings.ReplaceAll(l, "\t", "    ")
		for _, w := range WrapHard(l, max(1, width-2)) {
			r.emit(bar + w)
		}
	}
	if len(code) == 0 {
		r.emit(bar)
	}
}

// list renders consecutive list items starting at lines[i] and returns the
// index after the list.
func (r *mdRenderer) list(lines []string, i int, width int) int {
	type item struct {
		indent int
		marker string
		text   []string
	}
	var items []*item
	for i < len(lines) {
		l := lines[i]
		if m := reListItem.FindStringSubmatch(l); m != nil {
			items = append(items, &item{indent: len(strings.ReplaceAll(m[1], "\t", "    ")), marker: m[2], text: []string{m[3]}})
			i++
			continue
		}
		if isBlank(l) {
			// A blank line ends the list unless the next line continues it.
			if i+1 < len(lines) && (reListItem.MatchString(lines[i+1]) || strings.HasPrefix(lines[i+1], "  ")) {
				i++
				continue
			}
			break
		}
		if len(items) > 0 && !startsBlock(l) {
			last := items[len(items)-1]
			last.text = append(last.text, strings.TrimSpace(l))
			i++
			continue
		}
		break
	}

	// Map raw indents to nesting levels.
	var levels []int
	r.blank()
	for _, it := range items {
		for len(levels) > 0 && levels[len(levels)-1] > it.indent {
			levels = levels[:len(levels)-1]
		}
		if len(levels) == 0 || levels[len(levels)-1] < it.indent {
			levels = append(levels, it.indent)
		}
		depth := len(levels) - 1

		marker := FG(6, "•")
		if depth%2 == 1 {
			marker = FG(6, "◦")
		}
		if c := it.marker[0]; c >= '0' && c <= '9' {
			marker = FG(6, it.marker)
		}
		text := strings.Join(it.text, " ")
		if m := reTask.FindStringSubmatch(text); m != nil {
			text = text[len(m[0]):]
			if m[1] == " " {
				marker = Dim("☐")
			} else {
				marker = FG(2, "☑")
			}
		}
		pad := strings.Repeat("  ", depth)
		head := pad + marker + " "
		hang := strings.Repeat(" ", VisibleWidth(head))
		for k, l := range Wrap(inline(text), max(1, width-VisibleWidth(head))) {
			if k == 0 {
				r.emit(head + l)
			} else {
				r.emit(hang + l)
			}
		}
	}
	r.blank()
	return i
}

func splitRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	var cells []string
	var cur strings.Builder
	inCode := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\\' && i+1 < len(line) && line[i+1] == '|':
			cur.WriteByte('|')
			i++
		case c == '`':
			inCode = !inCode
			cur.WriteByte(c)
		case c == '|' && !inCode:
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	return append(cells, strings.TrimSpace(cur.String()))
}

func tableAligns(sep string) []byte {
	var out []byte
	for _, c := range splitRow(sep) {
		l, r := strings.HasPrefix(c, ":"), strings.HasSuffix(c, ":")
		switch {
		case l && r:
			out = append(out, 'c')
		case r:
			out = append(out, 'r')
		default:
			out = append(out, 'l')
		}
	}
	return out
}

func (r *mdRenderer) table(rows [][]string, aligns []byte, width int) {
	// Models often write key/value tables with an empty header ("| | |");
	// draw those without a header row instead of an empty box.
	header := true
	if len(rows) > 1 && strings.TrimSpace(strings.Join(rows[0], "")) == "" {
		header, rows = false, rows[1:]
	}
	ncol := 0
	for _, row := range rows {
		ncol = max(ncol, len(row))
	}
	cells := make([][]string, len(rows))
	natural, narrowest := make([]int, ncol), make([]int, ncol)
	for i, row := range rows {
		cells[i] = make([]string, ncol)
		for j := 0; j < ncol; j++ {
			if j < len(row) {
				cells[i][j] = inline(row[j])
				if i == 0 && header {
					cells[i][j] = Bold(cells[i][j])
				}
			}
			natural[j] = max(natural[j], VisibleWidth(cells[i][j]))
			// narrowest[j] is the widest character the column holds: nothing
			// is broken across cells, so a column narrower than that cannot
			// show its text (a CJK or emoji character is two columns wide).
			for _, l := range WrapHard(cells[i][j], 1) {
				narrowest[j] = max(narrowest[j], VisibleWidth(l))
			}
		}
	}

	// Each column costs its width plus 3 ("│ " + " "), plus a final border.
	avail := width - 3*ncol - 1
	if avail < ncol || sum(narrowest) > avail {
		// No room for a grid, not even one wide enough for every character:
		// fall back to "header: value" lines.
		if !header {
			for _, row := range cells {
				r.emit(Wrap(strings.Join(row, " · "), width)...)
			}
			return
		}
		for _, row := range cells[1:] {
			for j, c := range row {
				r.emit(Wrap(cells[0][j]+": "+c, width)...)
			}
			r.emit("")
		}
		return
	}
	// Columns give up width one step at a time, but never go below their
	// narrowest. Those minimums fit, as the check above made sure, so there is
	// always a column left to take a step from, and no character ends up in a
	// cell too narrow for it.
	widths := append([]int(nil), natural...)
	for sum(widths) > avail {
		wi := 0
		for j, w := range widths {
			if w > narrowest[j] && (widths[wi] == narrowest[wi] || w > widths[wi]) {
				wi = j
			}
		}
		widths[wi]--
	}

	border := func(l, m, rt string) string {
		var b strings.Builder
		b.WriteString(l)
		for j, w := range widths {
			b.WriteString(strings.Repeat("─", w+2))
			if j < ncol-1 {
				b.WriteString(m)
			}
		}
		b.WriteString(rt)
		return Dim(b.String())
	}
	r.emit(border("┌", "┬", "┐"))
	for i, row := range cells {
		wrapped := make([][]string, ncol)
		h := 1
		for j, c := range row {
			wrapped[j] = Wrap(c, max(1, widths[j]))
			for k, l := range wrapped[j] {
				// A row holds several cells: it can't join the next row.
				wrapped[j][k] = StripWrapMarks(l)
			}
			h = max(h, len(wrapped[j]))
		}
		for k := 0; k < h; k++ {
			var b strings.Builder
			b.WriteString(Dim("│"))
			for j := 0; j < ncol; j++ {
				var c string
				if k < len(wrapped[j]) {
					c = wrapped[j][k]
				}
				if VisibleWidth(c) > widths[j] {
					// A wide character can't wrap into a column one cell wide.
					c = Truncate(c, widths[j], "")
				}
				gap := max(0, widths[j]-VisibleWidth(c))
				a := byte('l')
				if j < len(aligns) {
					a = aligns[j]
				}
				switch a {
				case 'r':
					c = strings.Repeat(" ", gap) + c
				case 'c':
					c = strings.Repeat(" ", gap/2) + c + strings.Repeat(" ", gap-gap/2)
				default:
					c += strings.Repeat(" ", gap)
				}
				b.WriteString(" " + c + " " + Dim("│"))
			}
			r.emit(b.String())
		}
		// A rule between every row (as Claude Code draws them): cells that
		// wrap onto several lines would otherwise run into the next row.
		if i < len(cells)-1 {
			r.emit(border("├", "┼", "┤"))
		}
	}
	r.emit(border("└", "┴", "┘"))
}

func sum(xs []int) int {
	t := 0
	for _, x := range xs {
		t += x
	}
	return t
}

const (
	codeStyleOn  = "\x1b[38;5;180m"
	codeStyleOff = "\x1b[39m"
)

// inline renders emphasis, code spans, links and escapes.
func inline(s string) string {
	s = StripControls(s)
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && strings.IndexByte("\\`*_{}[]()#+-.!|~<>", s[i+1]) >= 0:
			b.WriteByte(s[i+1])
			i += 2
			continue

		case c == '`':
			n := runLen(s, i, '`')
			fence := s[i : i+n]
			if j := strings.Index(s[i+n:], fence); j >= 0 {
				code := s[i+n : i+n+j]
				if len(code) > 1 && code[0] == ' ' && code[len(code)-1] == ' ' {
					code = code[1 : len(code)-1]
				}
				b.WriteString(codeStyleOn + code + codeStyleOff)
				i += n + j + n
				continue
			}

		case c == '*' || c == '_' || c == '~':
			n := runLen(s, i, c)
			if c == '~' && n != 2 {
				b.WriteString(s[i : i+n])
				i += n
				continue
			}
			run := n
			if n >= 3 && c != '~' {
				n = 3
			} else if n > 2 {
				n = 2
			}
			delim := s[i : i+n]
			// Opening delimiter must be followed by non-space; '_' must not
			// be intra-word.
			if i+n < len(s) && s[i+n] != ' ' && !(c == '_' && i > 0 && isWordByte(s[i-1])) {
				if j := findClose(s, i+n, delim); j >= 0 {
					inner := inline(s[i+n : j])
					switch {
					case c == '~':
						inner = "\x1b[9m" + inner + "\x1b[29m"
					case n == 3:
						inner = Bold(Italic(inner))
					case n == 2:
						inner = Bold(inner)
					default:
						inner = Italic(inner)
					}
					b.WriteString(inner)
					i = j + n
					continue
				}
			}
			// Unmatched: emit the whole run literally.
			b.WriteString(s[i : i+run])
			i += run
			continue

		case c == '[':
			if end := strings.Index(s[i:], "]("); end > 0 {
				textEnd := i + end
				if close := strings.IndexByte(s[textEnd+2:], ')'); close >= 0 {
					url := s[textEnd+2 : textEnd+2+close]
					text := inline(s[i+1 : textEnd])
					b.WriteString(link(url, text))
					i = textEnd + 2 + close + 1
					continue
				}
			}

		case c == '<':
			if end := strings.IndexByte(s[i:], '>'); end > 0 {
				u := s[i+1 : i+end]
				if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
					b.WriteString(link(u, u))
					i += end + 1
					continue
				}
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

func link(url, text string) string {
	return "\x1b]8;;" + url + "\x07" + FG(4, "\x1b[4m"+text+"\x1b[24m") + "\x1b]8;;\x07"
}

func runLen(s string, i int, c byte) int {
	n := 0
	for i+n < len(s) && s[i+n] == c {
		n++
	}
	return n
}

func isWordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80
}

// findClose finds a closing delimiter for an emphasis run, skipping code
// spans. The closer must follow a non-space character.
func findClose(s string, from int, delim string) int {
	for j := from; j+len(delim) <= len(s); j++ {
		if s[j] == '`' {
			n := runLen(s, j, '`')
			if k := strings.Index(s[j+n:], s[j:j+n]); k >= 0 {
				j += n + k + n - 1
				continue
			}
		}
		if strings.HasPrefix(s[j:], delim) && j > from && s[j-1] != ' ' {
			// Don't close on a longer run (e.g. "*" inside "**").
			after := j + len(delim)
			if after < len(s) && s[after] == delim[0] && len(delim) < 3 {
				j = after
				continue
			}
			if delim[0] == '_' && after < len(s) && isWordByte(s[after]) {
				continue
			}
			return j
		}
	}
	return -1
}

// WrapHard breaks s every width columns without regard to words, preserving
// all whitespace. Used for code. Continuation lines start with a soft-wrap
// mark.
func WrapHard(s string, width int) []string {
	wr := wrapper{buf: make([]byte, 0, len(s)+16)}
	sc := cellScanner{s: s}
	from, w := 0, 0
	for {
		c, ok := sc.next()
		if !ok {
			wr.buf = append(wr.buf, c.esc...)
			break
		}
		if w+c.width > width && w > 0 {
			wr.cut(from, false)
			from = len(wr.buf)
			wr.buf = append(wr.buf, wr.st.active...)
			wr.buf = append(wr.buf, wrapJoin...)
			w = 0
		}
		wr.st.feed(c.esc)
		wr.buf = append(wr.buf, c.esc...)
		wr.buf = append(wr.buf, c.text...)
		w += c.width
	}
	wr.cut(from, false)
	return wr.lines()
}

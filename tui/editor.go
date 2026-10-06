package tui

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

// LargePaste is the size, in characters, above which a paste is shown as
// a placeholder (codex-rs: LARGE_PASTE_CHAR_THRESHOLD).
const LargePaste = 1000

// Attachment is something other than text sent with a prompt, such as an
// image. It shows in the editor as its label; deleting the label removes
// it.
type Attachment struct {
	Label string
	Value any
}

// element is a placeholder in the buffer, edited as one unit: a large
// paste (expanded on submit) or an attachment.
type element struct {
	label string
	paste string
	att   *Attachment
	image int // image number, for attachments added with AttachImage
}

// span is where an element's label sits in the buffer.
type span struct {
	start, end int
	el         int // index into elems
}

// Editor is a multi-line prompt input. Enter submits (Ctrl+Enter sends now);
// Alt+Enter or Ctrl+J inserts a newline. Up/Down walk submission history when the buffer is a
// single line.
type Editor struct {
	Prompt string
	// OnSubmit receives the text, with large pastes expanded, and the
	// attachments whose labels are still in it, in order.
	OnSubmit func(text string, att []Attachment)
	// OnSendNow, if set, receives what Ctrl+Enter (or its fallback, Ctrl+G)
	// commits, in place of OnSubmit.
	OnSendNow func(text string, att []Attachment)
	// OnPaste, if set, sees pastes up to LargePaste characters first and
	// returns true to consume one (e.g. an image path dropped on the
	// terminal).
	OnPaste func(text string) bool
	// Rule styles the horizontal rules drawn above and below the input.
	Rule func(string) string

	buf     []rune
	pos     int
	elems   []element
	focused bool

	history []draft
	histIdx int   // len(history) means "not browsing"
	draft   draft // buffer saved when history browsing starts
}

// draft is the editor content, kept for history.
type draft struct {
	buf   []rune
	elems []element
}

func NewEditor(prompt string) *Editor { return &Editor{Prompt: prompt} }

func (e *Editor) SetFocused(f bool) { e.focused = f }

// Text returns the buffer as shown, with placeholders unexpanded.
func (e *Editor) Text() string { return string(e.buf) }

// SetText replaces the buffer. Placeholders whose labels survive in s are
// kept, and att adds attachments whose labels s contains.
func (e *Editor) SetText(s string, att ...Attachment) {
	e.buf = []rune(s)
	e.pos = len(e.buf)
	for _, a := range att {
		e.elems = append(e.elems, element{label: a.Label, att: &a, image: imageNumber(a.Label)})
	}
	e.reconcile()
}

// Cursor returns the cursor position, in runes from the start of the text.
func (e *Editor) Cursor() int { return e.pos }

// LineBeforeCursor returns the cursor's line up to the cursor.
func (e *Editor) LineBeforeCursor() string { return string(e.buf[e.lineStart():e.pos]) }

// AfterCursor returns the text after the cursor.
func (e *Editor) AfterCursor() string { return string(e.buf[e.pos:]) }

// Replace deletes del runes before the cursor and delAfter after it,
// inserts s there and leaves the cursor cursor runes into s (completions).
func (e *Editor) Replace(del, delAfter int, s string, cursor int) {
	e.deleteRange(e.pos-del, e.pos+delAfter) // leaves the cursor at the gap
	from := e.pos
	e.insert(s)
	e.pos = from + min(cursor, utf8.RuneCountInString(s))
}

// Attachments returns the attachments currently in the buffer.
func (e *Editor) Attachments() []Attachment {
	var out []Attachment
	for _, sp := range e.spans() {
		if a := e.elems[sp.el].att; a != nil {
			out = append(out, *a)
		}
	}
	return out
}

// AttachImage inserts an image placeholder such as "[image 1: 1024x768 PNG]"
// at the cursor and returns its label. desc describes the image.
func (e *Editor) AttachImage(desc string, v any) string {
	n := 1
	for _, el := range e.elems {
		n = max(n, el.image+1)
	}
	label := fmt.Sprintf("[image %d: %s]", n, desc)
	e.insertElement(element{label: label, att: &Attachment{Label: label, Value: v}, image: n})
	e.insert(" ")
	return label
}

// imageNumber reads N back from an "[image N: …]" label (0 if none).
func imageNumber(label string) int {
	var n int
	if _, err := fmt.Sscanf(label, "[image %d:", &n); err != nil {
		return 0
	}
	return n
}

// pasteLabel names a large paste like codex: "[Pasted Content N chars]",
// with " #2", " #3"… when pastes of the same size are pending.
func (e *Editor) pasteLabel(chars int) string {
	base := fmt.Sprintf("[Pasted Content %d chars]", chars)
	suffix := 0
	for _, el := range e.elems {
		if el.label == base {
			suffix = max(suffix, 1)
		} else if rest, ok := strings.CutPrefix(el.label, base[:len(base)-1]+" #"); ok {
			var k int
			if _, err := fmt.Sscanf(rest, "%d]", &k); err == nil {
				suffix = max(suffix, k)
			}
		}
	}
	if suffix == 0 {
		return base
	}
	return fmt.Sprintf("[Pasted Content %d chars #%d]", chars, suffix+1)
}

func (e *Editor) insertElement(el element) {
	e.snap(1)
	e.elems = append(e.elems, el)
	e.insert(el.label)
}

// spans locates each element's label in the buffer, in buffer order.
// Elements whose label is gone are skipped.
func (e *Editor) spans() []span {
	var out []span
	for i, el := range e.elems {
		if at := runeIndex(e.buf, []rune(el.label)); at >= 0 {
			out = append(out, span{at, at + utf8.RuneCountInString(el.label), i})
		}
	}
	slices.SortFunc(out, func(a, b span) int { return a.start - b.start })
	return out
}

func runeIndex(buf, sub []rune) int {
	for i := 0; i+len(sub) <= len(buf); i++ {
		if slices.Equal(buf[i:i+len(sub)], sub) {
			return i
		}
	}
	return -1
}

// reconcile forgets elements whose labels were deleted.
func (e *Editor) reconcile() {
	keep := e.elems[:0]
	for _, el := range e.elems {
		if runeIndex(e.buf, []rune(el.label)) >= 0 {
			keep = append(keep, el)
		}
	}
	clear(e.elems[len(keep):])
	e.elems = keep
}

// snap moves the cursor out of a placeholder: to its start if dir < 0,
// else to its end.
func (e *Editor) snap(dir int) {
	for _, sp := range e.spans() {
		if e.pos > sp.start && e.pos < sp.end {
			if dir < 0 {
				e.pos = sp.start
			} else {
				e.pos = sp.end
			}
			return
		}
	}
}

func (e *Editor) insert(s string) {
	rs := []rune(s)
	e.buf = append(e.buf[:e.pos], append(rs, e.buf[e.pos:]...)...)
	e.pos += len(rs)
}

// deleteRange deletes [from, to), widened to whole placeholders.
func (e *Editor) deleteRange(from, to int) {
	from, to = max(from, 0), min(to, len(e.buf))
	if from >= to {
		return
	}
	for _, sp := range e.spans() {
		if sp.start < to && sp.end > from {
			from, to = min(from, sp.start), max(to, sp.end)
		}
	}
	e.buf = append(e.buf[:from], e.buf[to:]...)
	e.pos = from
	e.reconcile()
}

func (e *Editor) lineStart() int {
	i := e.pos
	for i > 0 && e.buf[i-1] != '\n' {
		i--
	}
	return i
}

func (e *Editor) lineEnd() int {
	i := e.pos
	for i < len(e.buf) && e.buf[i] != '\n' {
		i++
	}
	return i
}

func (e *Editor) wordLeft() int {
	i := e.pos
	for i > 0 && unicode.IsSpace(e.buf[i-1]) {
		i--
	}
	for i > 0 && !unicode.IsSpace(e.buf[i-1]) {
		i--
	}
	return i
}

func (e *Editor) wordRight() int {
	i := e.pos
	for i < len(e.buf) && unicode.IsSpace(e.buf[i]) {
		i++
	}
	for i < len(e.buf) && !unicode.IsSpace(e.buf[i]) {
		i++
	}
	return i
}

// moveLine moves the cursor to the same column on the previous (-1) or next
// (+1) logical line. Returns false if there is no such line.
func (e *Editor) moveLine(dir int) bool {
	start, end := e.lineStart(), e.lineEnd()
	col := e.pos - start
	if dir < 0 {
		if start == 0 {
			return false
		}
		e.pos = start - 1
		ps := e.lineStart()
		e.pos = ps + min(col, e.pos-ps)
		return true
	}
	if end == len(e.buf) {
		return false
	}
	e.pos = end + 1
	ne := e.lineEnd()
	e.pos = min(e.pos+col, ne)
	return true
}

func (e *Editor) save() draft {
	return draft{slices.Clone(e.buf), slices.Clone(e.elems)}
}

func (e *Editor) restore(d draft) {
	e.buf, e.elems = slices.Clone(d.buf), slices.Clone(d.elems)
	e.pos = len(e.buf)
}

func (e *Editor) browseHistory(dir int) {
	if len(e.history) == 0 {
		return
	}
	if e.histIdx == len(e.history) {
		e.draft = e.save()
	}
	e.histIdx = min(max(e.histIdx+dir, 0), len(e.history))
	if e.histIdx == len(e.history) {
		e.restore(e.draft)
	} else {
		e.restore(e.history[e.histIdx])
	}
}

// Commit records the current text in history, clears the editor and returns
// the trimmed text ("" if blank) with large pastes expanded, and the
// attachments in it.
func (e *Editor) Commit() (string, []Attachment) {
	if strings.TrimSpace(string(e.buf)) == "" {
		return "", nil
	}
	var b strings.Builder
	var att []Attachment
	last := 0
	for _, sp := range e.spans() {
		el := e.elems[sp.el]
		if el.att != nil {
			att = append(att, *el.att)
			continue
		}
		b.WriteString(string(e.buf[last:sp.start]))
		b.WriteString(el.paste)
		last = sp.end
	}
	b.WriteString(string(e.buf[last:]))
	e.history = append(e.history, e.save())
	e.histIdx = len(e.history)
	e.buf, e.pos, e.elems = nil, 0, nil
	return strings.TrimSpace(b.String()), att
}

// paste inserts pasted text: large pastes become a placeholder, and
// OnPaste may take the rest.
func (e *Editor) paste(p string) {
	p = strings.ReplaceAll(strings.ReplaceAll(p, "\r\n", "\n"), "\r", "\n")
	if n := utf8.RuneCountInString(p); n > LargePaste {
		e.insertElement(element{label: e.pasteLabel(n), paste: p})
		return
	}
	if e.OnPaste != nil && e.OnPaste(p) {
		return
	}
	e.snap(1)
	e.insert(p)
}

func (e *Editor) HandleInput(data string) {
	if strings.HasPrefix(data, PastePrefix) {
		e.paste(data[len(PastePrefix):])
		return
	}
	switch Key(data) {
	case "enter":
		text, att := e.Commit()
		if e.OnSubmit != nil {
			e.OnSubmit(text, att)
		}
	case "ctrl+enter", "ctrl+g":
		text, att := e.Commit()
		switch {
		case e.OnSendNow != nil:
			e.OnSendNow(text, att)
		case e.OnSubmit != nil:
			e.OnSubmit(text, att)
		}
	case "alt+enter", "ctrl+j":
		e.snap(1)
		e.insert("\n")
	case "backspace":
		e.deleteRange(e.pos-1, e.pos)
	case "delete", "ctrl+d":
		p := e.pos
		e.deleteRange(p, p+1)
	case "left", "ctrl+b":
		e.pos = max(0, e.pos-1)
		e.snap(-1)
	case "right", "ctrl+f":
		e.pos = min(len(e.buf), e.pos+1)
		e.snap(1)
	case "home", "ctrl+a":
		e.pos = e.lineStart()
		e.snap(-1)
	case "end", "ctrl+e":
		e.pos = e.lineEnd()
		e.snap(1)
	case "word-left":
		e.pos = e.wordLeft()
		e.snap(-1)
	case "word-right":
		e.pos = e.wordRight()
		e.snap(1)
	case "ctrl+w", "alt+backspace":
		e.deleteRange(e.wordLeft(), e.pos)
	case "ctrl+u":
		e.deleteRange(e.lineStart(), e.pos)
	case "ctrl+k":
		e.deleteRange(e.pos, e.lineEnd())
	case "up":
		if !e.moveLine(-1) {
			e.browseHistory(-1)
		}
		e.snap(1)
	case "down":
		if !e.moveLine(1) {
			e.browseHistory(1)
		}
		e.snap(1)
	default:
		if Printable(data) {
			e.snap(1)
			e.insert(data)
		}
	}
}

const (
	placeholderOn  = "\x1b[38;5;6m" // FG(6)
	placeholderOff = "\x1b[39m"
)

func (e *Editor) Render(width int) []string {
	border := func(s string) string {
		if e.Rule != nil {
			return e.Rule(s)
		}
		return s
	}
	// Horizontal rules above and below, one column of padding before the
	// prompt.
	width = max(width, 4)
	promptW := VisibleWidth(e.Prompt)
	cw := max(1, width-2-promptW)

	// Placeholders are drawn in color.
	inSpan := make([]bool, len(e.buf))
	for _, sp := range e.spans() {
		for i := sp.start; i < sp.end; i++ {
			inSpan[i] = true
		}
	}
	var rows []string
	var row strings.Builder
	rowW := 0
	styled := false
	flush := func() {
		if styled {
			row.WriteString(placeholderOff)
			styled = false
		}
		rows = append(rows, row.String())
		row.Reset()
		rowW = 0
	}
	for i := 0; i <= len(e.buf); i++ {
		if i == e.pos && e.focused {
			if rowW >= cw {
				flush()
			}
			row.WriteString(CursorMarker)
		}
		if i == len(e.buf) {
			break
		}
		r := e.buf[i]
		if r == '\n' {
			flush()
			continue
		}
		w := uniseg.StringWidth(string(r))
		if rowW+w > cw {
			flush()
		}
		if inSpan[i] != styled {
			styled = inSpan[i]
			if styled {
				row.WriteString(placeholderOn)
			} else {
				row.WriteString(placeholderOff)
			}
		}
		row.WriteRune(r)
		rowW += w
	}
	flush()

	indent := strings.Repeat(" ", promptW)
	rule := border(strings.Repeat("─", width))
	out := make([]string, 0, len(rows)+2)
	out = append(out, rule)
	for i, r := range rows {
		lead := indent
		if i == 0 {
			lead = e.Prompt
		}
		out = append(out, " "+lead+r)
	}
	return append(out, rule)
}

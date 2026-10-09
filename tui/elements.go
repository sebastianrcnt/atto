package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rivo/uniseg"
	"github.com/sebastianrcnt/atto/ui"
)

// Elements adapts portable UI data to Component. Drafts, selections and
// disclosures belong to this client and survive unrelated worker redraws.
// Call SetTree before rendering untrusted data; it validates before layout.
type Elements struct {
	Tree     *ui.Node
	Site     ui.Site
	ID       string
	Rev      int64
	OnAction func(ui.Action)
	OnEscape func()
	Engine   func(ui.Node, int) []string
	Theme    func(ui.ThemeKey, string) string
	focused  bool
	focus    int
	controls []ui.Node
	open     map[string]bool
	drafts   map[string]string
	values   map[string]string
	selected map[string]int
	hits     []elementHit
}
type elementHit struct {
	key        string
	x, y, w, h int
}

func (e *Elements) SetTree(site ui.Site, id string, rev int64, n *ui.Node) error {
	if n != nil {
		if err := ui.ValidateDisplay(site, id, *n); err != nil {
			return err
		}
	}
	e.Site, e.ID, e.Rev, e.Tree = site, id, rev, n
	if e.open == nil {
		e.open = map[string]bool{}
		e.drafts = map[string]string{}
		e.values = map[string]string{}
		e.selected = map[string]int{}
	}
	e.controls = nil
	keys := map[string]bool{}
	var walk func(ui.Node)
	walk = func(n ui.Node) {
		if n.Key != "" {
			keys[n.Key] = true
		}
		if n.Type == "Input" {
			v := propString(n, "value")
			if old, ok := e.values[n.Key]; !ok || old != v {
				e.drafts[n.Key] = v
				e.values[n.Key] = v
			}
		}
		if n.Type == "Select" {
			v := propString(n, "value")
			if old, ok := e.values[n.Key]; !ok || old != v {
				e.values[n.Key] = v
				for i, o := range options(n) {
					if !o.Disabled && (v == "" || v == o.Value) {
						e.selected[n.Key] = i
						break
					}
				}
			}
		}
		if n.Type == "Collapse" {
			if _, ok := e.open[n.Key]; !ok {
				e.open[n.Key] = propBool(n, "defaultOpen")
			}
		}
		if n.Props["disabled"] != true && (n.Type == "Button" || n.Type == "Input" || n.Type == "Select" || n.Type == "Collapse") {
			e.controls = append(e.controls, n)
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	if n != nil {
		walk(*n)
	}
	for k := range e.values {
		if !keys[k] {
			delete(e.values, k)
			delete(e.drafts, k)
			delete(e.selected, k)
		}
	}
	for k := range e.open {
		if !keys[k] {
			delete(e.open, k)
		}
	}
	if e.focus >= len(e.controls) {
		e.focus = 0
	}
	return nil
}
func (e *Elements) SetFocused(v bool) { e.focused = v }
func (e *Elements) action(n ui.Node, kind ui.EventType, value *string) {
	if e.OnAction != nil {
		e.OnAction(ui.Action{Site: e.Site, ID: e.ID, Rev: e.Rev, Key: n.Key, Type: kind, Value: value})
	}
}
func (e *Elements) HandleInput(data string) {
	if !e.focused {
		return
	}
	if data == "\x1b" {
		if e.OnEscape != nil {
			e.OnEscape()
		}
		return
	}
	if len(e.controls) == 0 {
		return
	}
	if data == "\t" {
		e.focus = (e.focus + 1) % len(e.controls)
		return
	}
	if data == "\x1b[Z" {
		e.focus = (e.focus + len(e.controls) - 1) % len(e.controls)
		return
	}
	n := e.controls[e.focus]
	switch n.Type {
	case "Input":
		v := e.drafts[n.Key]
		if data == "\r" || data == "\n" {
			e.action(n, ui.Submit, &v)
			return
		}
		if data == "\x7f" || data == "\b" {
			g := uniseg.NewGraphemes(v)
			last := 0
			for g.Next() {
				start, _ := g.Positions()
				last = start
			}
			v = v[:last]
		} else if !strings.HasPrefix(data, "\x1b") {
			v += strings.ReplaceAll(strings.ReplaceAll(ui.CleanText(data), "\n", ""), "\t", "")
		}
		limit := propInt(n, "maxLength", 4096)
		if len([]rune(v)) <= limit {
			e.drafts[n.Key] = v
			for _, kind := range n.Events {
				if kind == ui.InputEvent {
					e.action(n, kind, &v)
				}
			}
		}
		return
	case "Select":
		opts := options(n)
		i := e.selected[n.Key]
		delta := 0
		if data == "\x1b[A" || data == "\x10" {
			delta = -1
		}
		if data == "\x1b[B" || data == "\x0e" {
			delta = 1
		}
		if delta != 0 {
			for range len(opts) {
				i = (i + delta + len(opts)) % len(opts)
				if !opts[i].Disabled {
					break
				}
			}
			e.selected[n.Key] = i
			return
		}
		if data == "\r" || data == "\n" {
			if len(opts) > 0 && !opts[i].Disabled {
				e.action(n, ui.SelectEvent, &opts[i].Value)
			}
			return
		}
	}
	if data == "\x1b[A" || data == "\x1b[D" {
		e.focus = (e.focus + len(e.controls) - 1) % len(e.controls)
		return
	}
	if data == "\x1b[B" || data == "\x1b[C" {
		e.focus = (e.focus + 1) % len(e.controls)
		return
	}
	if data == "\r" || data == "\n" || data == " " {
		e.activate(n)
		return
	}
	for i, c := range e.controls {
		if c.Type == "Button" && propString(c, "hotkey") == data {
			e.focus = i
			e.activate(c)
			return
		}
	}
}
func (e *Elements) activate(n ui.Node) {
	if n.Type == "Collapse" {
		e.open[n.Key] = !e.open[n.Key]
	} else if n.Type == "Button" {
		e.action(n, ui.Press, nil)
	}
}
func (e *Elements) Click(line int) bool { return e.ClickAt(0, line) }
func (e *Elements) ClickAt(x, y int) bool {
	for _, h := range e.hits {
		if y >= h.y && y < h.y+h.h && x >= h.x && x < h.x+h.w {
			for i, n := range e.controls {
				if n.Key == h.key {
					e.focus = i
					e.focused = true
					e.activate(n)
					return true
				}
			}
		}
	}
	return false
}
func (e *Elements) Render(width int) []string {
	if e.Tree == nil || width <= 0 {
		return nil
	}
	e.hits = nil
	return e.render(*e.Tree, min(width, 512), 0, 0, ui.ColorText, "")
}
func propString(n ui.Node, k string) string { v, _ := n.Props[k].(string); return ui.CleanText(v) }
func propBool(n ui.Node, k string) bool     { v, _ := n.Props[k].(bool); return v }
func propInt(n ui.Node, k string, def int) int {
	switch v := n.Props[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return def
}
func options(n ui.Node) []ui.Option {
	b, _ := json.Marshal(n.Props["options"])
	var out []ui.Option
	_ = json.Unmarshal(b, &out)
	return out
}
func (e *Elements) style(color ui.ThemeKey, s string) string {
	if e.Theme != nil {
		return e.Theme(color, s)
	}
	switch color {
	case ui.Muted, ui.Border:
		return Dim(s)
	case ui.Accent, ui.DiffHunk:
		return FG(6, s)
	case ui.Success, ui.DiffAdd:
		return FG(2, s)
	case ui.Warning:
		return FG(3, s)
	case ui.Error, ui.DiffRemove:
		return FG(1, s)
	case ui.Surface:
		return BG(237, s)
	}
	return s
}

// expandElementTabs uses four-cell stops and never splits a grapheme cluster.
func expandElementTabs(s string) string {
	var out strings.Builder
	col := 0
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		v := g.Str()
		if v == "\t" {
			n := 4 - col%4
			out.WriteString(strings.Repeat(" ", n))
			col += n
		} else {
			out.WriteString(v)
			col += g.Width()
		}
	}
	return out.String()
}
func (e *Elements) render(n ui.Node, w, x, y int, color, background ui.ThemeKey) []string {
	if w <= 0 {
		return nil
	}
	if c := propString(n, "color"); c != "" {
		color = ui.ThemeKey(c)
	}
	if c := propString(n, "backgroundColor"); c != "" {
		background = ui.ThemeKey(c)
	}
	if propBool(n, "disabled") {
		color = ui.Muted
	}
	focused := e.focused && len(e.controls) > 0 && e.controls[e.focus].Key == n.Key
	var out []string
	switch n.Type {
	case "Box":
		return e.box(n, w, x, y, color, background)
	case "engine":
		if e.Engine != nil {
			return e.Engine(n, w)
		}
		out = Wrap(ui.PlainText(n), w)
	case "Text":
		text := propString(n, "text")
		for _, c := range n.Children {
			text += ui.PlainText(c)
		}
		text = expandElementTabs(text)
		if propString(n, "wrap") == "truncate" {
			for l := range strings.SplitSeq(text, "\n") {
				out = append(out, Truncate(l, w, "…"))
			}
		} else {
			out = Wrap(text, w)
		}
		if propBool(n, "bold") {
			for i := range out {
				out[i] = Bold(out[i])
			}
		}
		if propBool(n, "italic") {
			for i := range out {
				out[i] = Italic(out[i])
			}
		}
		if propBool(n, "underline") {
			for i := range out {
				out[i] = "\x1b[4m" + out[i] + "\x1b[24m"
			}
		}
	case "Markdown":
		out = Markdown(expandElementTabs(propString(n, "text")), w)
	case "Code", "Diff":
		if path := propString(n, "path"); path != "" {
			out = append(out, e.style(ui.Muted, Truncate(path, w, "…")))
		}
		for i, line := range strings.Split(propString(n, "source"), "\n") {
			line = expandElementTabs(line)
			if propBool(n, "lineNumbers") {
				line = fmt.Sprintf("%4d │ %s", i+propInt(n, "startLine", 1), line)
			}
			lines := []string{Truncate(line, w, "…")}
			if propString(n, "wrap") == "wrap" {
				lines = Wrap(line, w)
			}
			for _, l := range lines {
				c := color
				if n.Type == "Diff" {
					switch {
					case strings.HasPrefix(line, "diff "), strings.HasPrefix(line, "index "), strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"):
						c = ui.Muted
					case strings.HasPrefix(line, "@@"):
						c = ui.DiffHunk
					case strings.HasPrefix(line, "+"):
						c = ui.DiffAdd
					case strings.HasPrefix(line, "-"):
						c = ui.DiffRemove
					}
				}
				out = append(out, e.style(c, l))
			}
		}
		color = ""
	case "Link":
		out = Wrap(ui.PlainText(n), w)
		for i := range out {
			out[i] = "\x1b[4m" + out[i] + "\x1b[24m"
		}
	case "Button":
		label := propString(n, "label")
		if h := propString(n, "hotkey"); h != "" {
			label = h + ": " + label
		}
		if !propBool(n, "plain") {
			label = "[ " + label + " ]"
		}
		if focused {
			color = ui.Accent
			label = Bold(label)
		}
		out = []string{Truncate(label, w, "…")}
	case "Input":
		label := propString(n, "label")
		if label != "" {
			out = append(out, Truncate(label, w, "…"))
		}
		v := e.drafts[n.Key]
		if v == "" && !focused {
			v = propString(n, "placeholder")
		}
		v = expandElementTabs(v)
		if VisibleWidth(v) > w-1 {
			g := uniseg.NewGraphemes(v)
			var clusters []string
			for g.Next() {
				clusters = append(clusters, g.Str())
			}
			for len(clusters) > 0 && VisibleWidth(strings.Join(clusters, "")) > w-1 {
				clusters = clusters[1:]
			}
			v = strings.Join(clusters, "")
		}
		if focused {
			v += CursorMarker
			color = ui.Accent
		}
		out = append(out, Truncate(v, w, "…"), e.style(ui.Muted, "enter "+defaultString(propString(n, "submitLabel"), "submit")+"  tab next  esc return"))
	case "Select":
		if l := propString(n, "label"); l != "" {
			out = append(out, Truncate(l, w, "…"))
		}
		for i, o := range options(n) {
			prefix := "  "
			c := color
			if i == e.selected[n.Key] {
				prefix = "› "
				if focused {
					c = ui.Accent
				}
			}
			if o.Disabled {
				c = ui.Muted
			}
			line := prefix + ui.CleanText(o.Label)
			if o.Description != "" {
				line += "  " + e.style(ui.Muted, ui.CleanText(o.Description))
			}
			out = append(out, e.style(c, Truncate(line, w, "…")))
		}
		color = ""
	case "List":
		out = e.list(n, w)
	case "Progress":
		size := min(propInt(n, "width", 20), max(1, w-8))
		label := propString(n, "label")
		if v, ok := n.Props["value"].(float64); ok {
			filled := int(v * float64(size))
			out = []string{fmt.Sprintf("[%s%s] %.0f%% %s", strings.Repeat("#", filled), strings.Repeat("-", size-filled), v*100, label)}
		} else {
			out = []string{"⠋ " + label}
		}
	case "Collapse":
		open := e.open[n.Key]
		marker := "▸ "
		if open {
			marker = "▾ "
		}
		out = []string{Truncate(marker+propString(n, "title"), w, "…")}
		if focused {
			out[0] = e.style(ui.Accent, Bold(out[0]))
		}
		var body []string
		for _, c := range n.Children {
			body = append(body, e.render(c, max(1, w-2), x+2, y+len(out)+len(body), color, background)...)
		}
		hidden := 0
		if !open {
			preview := min(len(body), propInt(n, "previewLines", 0))
			hidden = len(body) - preview
			body = body[:preview]
		}
		for _, l := range body {
			out = append(out, "  "+l)
		}
		if hidden > 0 {
			out = append(out, e.style(ui.Muted, fmt.Sprintf("  + %d lines (click or ctrl+t to expand)", hidden)))
		} else if open {
			out = append(out, e.style(ui.Muted, "  − Show less (click)"))
		}
	case "Image":
		out = []string{"[image: " + propString(n, "alt") + "]"}
	default:
		out = Wrap(expandElementTabs(ui.PlainText(n)), w)
	}
	if maxLines := propInt(n, "maxLines", 0); maxLines > 0 && len(out) > maxLines {
		out = out[:maxLines]
		out[maxLines-1] = Truncate(out[maxLines-1], max(1, w-1), "") + "…"
	}
	if n.Key != "" && (n.Type == "Button" || n.Type == "Input" || n.Type == "Select" || n.Type == "Collapse") {
		if n.Type == "Collapse" {
			e.hits = append(e.hits, elementHit{n.Key, x, y, w, 1})
			if len(out) > 1 {
				e.hits = append(e.hits, elementHit{n.Key, x, y + len(out) - 1, w, 1})
			}
		} else {
			e.hits = append(e.hits, elementHit{n.Key, x, y, w, len(out)})
		}
	}
	for i, l := range out {
		l = Truncate(l, w, "…")
		if color != "" {
			l = e.style(color, l)
		}
		if background != "" {
			l = e.style(background, l+strings.Repeat(" ", max(0, w-VisibleWidth(l))))
		}
		out[i] = l
	}
	return out
}
func defaultString(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
func (e *Elements) list(n ui.Node, w int) []string {
	b, _ := json.Marshal(n.Props["rows"])
	var rows []ui.Row
	_ = json.Unmarshal(b, &rows)
	if len(rows) == 0 {
		return Wrap(defaultString(propString(n, "emptyText"), "No items"), w)
	}
	if propString(n, "mode") != "table" {
		var out []string
		for _, r := range rows {
			out = append(out, Wrap("• "+expandElementTabs(ui.CleanText(r.Cells[0])), w)...)
		}
		return out
	}
	b, _ = json.Marshal(n.Props["columns"])
	var cols []ui.Column
	_ = json.Unmarshal(b, &cols)
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = c.Width
		if widths[i] == 0 {
			widths[i] = VisibleWidth(ui.CleanText(c.Label))
			for _, r := range rows {
				widths[i] = max(widths[i], VisibleWidth(expandElementTabs(ui.CleanText(r.Cells[i]))))
			}
		}
	}
	budget := max(0, w-2*(len(cols)-1))
	for {
		sum := 0
		largest := 0
		for i, v := range widths {
			sum += v
			if v > widths[largest] {
				largest = i
			}
		}
		if sum <= budget || widths[largest] == 0 {
			break
		}
		widths[largest]--
	}
	line := func(cells []string) string {
		var parts []string
		for i, c := range cells {
			s := Truncate(expandElementTabs(ui.CleanText(c)), widths[i], "…")
			pad := strings.Repeat(" ", max(0, widths[i]-VisibleWidth(s)))
			if cols[i].Align == "end" {
				s = pad + s
			} else {
				s += pad
			}
			parts = append(parts, s)
		}
		return Truncate(strings.Join(parts, "  "), w, "…")
	}
	labels := make([]string, len(cols))
	for i, c := range cols {
		labels[i] = c.Label
	}
	out := []string{Bold(line(labels))}
	for _, r := range rows {
		out = append(out, line(r.Cells))
	}
	return out
}
func (e *Elements) box(n ui.Node, w, x, y int, color, background ui.ThemeKey) []string {
	if fixed := propInt(n, "width", 0); fixed > 0 {
		w = min(w, fixed)
	}
	border := propString(n, "borderStyle")
	bordered := border != "" && border != "none"
	edge := 0
	if bordered {
		edge = 1
	}
	pad := min(propInt(n, "padding", 0), max(0, (w-2*edge-1)/2))
	inner := max(0, w-2*edge-2*pad)
	gap := propInt(n, "gap", 0)
	var body []string
	if propString(n, "flexDirection") == "row" {
		widths := make([]int, len(n.Children))
		remaining := max(0, inner-gap*max(0, len(n.Children)-1))
		weight := 0.0
		for i, c := range n.Children {
			if fixed := propInt(c, "width", 0); fixed > 0 {
				widths[i] = min(fixed, remaining)
				remaining -= widths[i]
			} else {
				grow, _ := c.Props["grow"].(float64)
				if grow <= 0 {
					grow = 1
				}
				weight += grow
			}
		}
		for i, c := range n.Children {
			if propInt(c, "width", 0) == 0 {
				grow, _ := c.Props["grow"].(float64)
				if grow <= 0 {
					grow = 1
				}
				widths[i] = int(float64(remaining) * grow / weight)
			}
		}
		var cells [][]string
		height := 0
		offset := 0
		for i, c := range n.Children {
			lines := e.render(c, widths[i], x+edge+pad+offset, y+edge+pad, color, background)
			height = max(height, len(lines))
			cells = append(cells, lines)
			offset += widths[i] + gap
		}
		for row := 0; row < height; row++ {
			var parts []string
			for i, lines := range cells {
				s := ""
				if row < len(lines) {
					s = lines[row]
				}
				s += strings.Repeat(" ", max(0, widths[i]-VisibleWidth(s)))
				parts = append(parts, s)
			}
			body = append(body, Truncate(strings.Join(parts, strings.Repeat(" ", gap)), inner, ""))
		}
	} else {
		for i, c := range n.Children {
			if i > 0 {
				body = append(body, make([]string, gap)...)
			}
			body = append(body, e.render(c, inner, x+edge+pad, y+edge+pad+len(body), color, background)...)
		}
	}
	body = append(make([]string, pad), body...)
	body = append(body, make([]string, pad)...)
	if h := propInt(n, "height", 0); h > 0 {
		h = max(0, h-2*edge)
		if len(body) > h {
			body = body[:h]
		} else {
			body = append(body, make([]string, h-len(body))...)
		}
	}
	left, right, top, bottom, horiz := "│", "│", "┌┐", "└┘", "─"
	switch border {
	case "round":
		top, bottom = "╭╮", "╰╯"
	case "double":
		left, right, top, bottom, horiz = "║", "║", "╔╗", "╚╝", "═"
	case "ascii":
		left, right, top, bottom, horiz = "|", "|", "++", "++", "-"
	}
	var out []string
	if bordered {
		r := []rune(top)
		out = append(out, e.style(ui.Border, string(r[0])+strings.Repeat(horiz, max(0, w-2))+string(r[1])))
	}
	for _, s := range body {
		s = strings.Repeat(" ", pad) + Truncate(s, inner, "")
		s += strings.Repeat(" ", max(0, w-2*edge-VisibleWidth(s)))
		if background != "" {
			s = e.style(background, s)
		}
		if bordered {
			s = e.style(ui.Border, left) + s + e.style(ui.Border, right)
		}
		out = append(out, Truncate(s, w, ""))
	}
	if bordered {
		r := []rune(bottom)
		out = append(out, e.style(ui.Border, string(r[0])+strings.Repeat(horiz, max(0, w-2))+string(r[1])))
	}
	return out
}

// PaneLayout enforces the catalog minima while retaining editor/status room.
func PaneLayout(width, height int, o ui.OpenOptions) (side bool, columns, rows int) {
	columns = o.Columns
	if columns <= 0 {
		columns = 40
	}
	side = o.Placement != "abovePrompt" && width >= 120 && width-columns >= 72 && columns >= 32
	if side {
		columns = min(columns, width-72)
		rows = max(1, height-4)
	} else {
		columns = width
		rows = min(defaultRows(o.Rows), max(1, (height-4)/3))
	}
	return
}
func defaultRows(n int) int {
	if n == 0 {
		return 8
	}
	return n
}

// ExpandAll applies native Ctrl+T to the existing client-local disclosures.
func (e *Elements) ExpandAll(open bool) {
	for key := range e.open {
		e.open[key] = open
	}
}

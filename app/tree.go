package app

import (
	"encoding/json"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

// treePicker is pi's session tree selector, ported: every entry of the
// session as a tree, the active branch marked with •, branch points drawn
// with ├─/└─ connectors that fold (⊟/⊞). Enter moves the active leaf to the
// selected entry (see App.navigateTree). Keys follow pi:
//
//	↑/↓ move · ←/→ page · option+←/→ (ctrl+←/→) fold or jump between branches
//	ctrl+x copy · shift+l label · shift+t label time
//	filters ctrl+d default, ctrl+t no tools, ctrl+u user, ctrl+l labeled, ctrl+a all
//	ctrl+o / shift+ctrl+o cycle filters · type to search · esc clears, then closes
//
// pi pans long rows horizontally; here they are truncated.
type treePicker struct {
	flat     []*flatNode // every node, depth first, active branch first
	visible  []*flatNode // after filter, search and folds
	byID     map[string]*flatNode
	selected int
	leaf     string
	active   map[string]bool // IDs on the path to the leaf
	calls    map[string]string
	maxRows  int

	filter        treeFilter
	query         string
	showLabelTime bool
	folded        map[string]bool
	lastSelected  string
	multipleRoots bool
	visParent     map[string]string // nearest visible ancestor ("" for a root)
	visChildren   map[string][]string

	label *labelInput // set while editing a label

	onSelect        func(id string)
	onCancel        func()
	onCopy          func(text string)
	onCopyEntry     func(id string)
	onSearch        func(query string)
	searchRequested string
	searchMatches   map[string]bool
	onLabel         func(id, label string)
}

type treeFilter int

const (
	filterDefault treeFilter = iota
	filterNoTools
	filterUser
	filterLabeled
	filterAll
)

var filterTags = []string{"", " [no-tools]", " [user]", " [labeled]", " [all]"}

type gutter struct {
	pos  int
	show bool
}

// flatNode is a node with its drawing state in the current view.
type flatNode struct {
	n             *session.Node
	indent        int
	showConnector bool
	isLast        bool
	gutters       []gutter
	virtualRoot   bool // a root drawn under the virtual root of several roots
}

func newTreePicker(roots []*session.Node, leaf string, maxRows int) *treePicker {
	p := &treePicker{leaf: leaf, maxRows: maxRows, folded: map[string]bool{}, active: map[string]bool{},
		calls: map[string]string{}, byID: map[string]*flatNode{}}
	p.flatten(roots)
	for id := leaf; id != ""; {
		f := p.byID[id]
		if f == nil || p.active[id] {
			break
		}
		p.active[id] = true
		id = f.n.Entry.Parent
	}
	p.applyFilter()
	p.selected = p.nearestVisible(leaf)
	if p.selected < len(p.visible) {
		p.lastSelected = p.visible[p.selected].n.Entry.ID
	}
	return p
}

// flatten lists the tree depth first, with the subtree holding the active
// leaf first at every branch point (as pi orders it).
func (p *treePicker) flatten(roots []*session.Node) {
	holds := map[*session.Node]bool{}
	var mark func(n *session.Node) bool
	mark = func(n *session.Node) bool {
		h := n.Entry.ID == p.leaf
		for _, c := range n.Children {
			if mark(c) {
				h = true
			}
		}
		holds[n] = h
		return h
	}
	for _, r := range roots {
		mark(r)
	}
	order := func(ns []*session.Node) []*session.Node {
		var first, rest []*session.Node
		for _, n := range ns {
			if holds[n] {
				first = append(first, n)
			} else {
				rest = append(rest, n)
			}
		}
		return append(first, rest...)
	}
	stack := order(roots)
	for i, j := 0, len(stack)-1; i < j; i, j = i+1, j-1 {
		stack[i], stack[j] = stack[j], stack[i]
	}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if m := n.Entry.Message; n.Entry.Type == session.TypeMessage && m != nil {
			for _, tc := range m.ToolCalls {
				p.calls[tc.ID] = toolCommand(tc.Function.Arguments)
			}
		}
		f := &flatNode{n: n}
		p.flat = append(p.flat, f)
		if _, ok := p.byID[n.Entry.ID]; !ok {
			p.byID[n.Entry.ID] = f
		}
		kids := order(n.Children)
		for _, kid := range slices.Backward(kids) {
			stack = append(stack, kid)
		}
	}
}

// toolCommand pulls the command out of shell tool arguments.
func toolCommand(args string) string {
	var a struct {
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(args), &a) != nil {
		return args
	}
	return a.Command
}

// settingsEntry reports bookkeeping entries that the default view hides.
func settingsEntry(e session.Entry) bool {
	switch e.Type {
	case session.TypeLabel, session.TypeModel, session.TypeEffort, session.TypeName, session.TypeGoal, session.TypeBranch, session.TypeBlockDisplay, session.TypeExtText:
		return true
	}
	return false
}

func (p *treePicker) passes(f *flatNode) bool {
	e := f.n.Entry
	m := e.Message
	role := ""
	if e.Type == session.TypeMessage && m != nil {
		role = m.Role
	}
	// Assistant messages that only call tools are shown through their
	// results, except at the leaf so the current position stays visible.
	if role == "assistant" && e.ID != p.leaf && strings.TrimSpace(m.Content) == "" {
		return false
	}
	ok := true
	switch p.filter {
	case filterUser:
		ok = role == "user"
	case filterNoTools:
		ok = !settingsEntry(e) && role != "tool"
	case filterLabeled:
		ok = f.n.Label != ""
	case filterAll:
	default:
		ok = !settingsEntry(e)
	}
	if !ok {
		return false
	}
	if p.query != "" && p.searchMatches != nil {
		return p.searchMatches[e.ID]
	}
	if p.query != "" {
		text := strings.ToLower(p.searchText(f.n))
		for tok := range strings.FieldsSeq(strings.ToLower(p.query)) {
			if !strings.Contains(text, tok) {
				return false
			}
		}
	}
	return true
}

func (p *treePicker) applyFilter() {
	if p.onSearch != nil && p.query != p.searchRequested {
		p.searchRequested = p.query
		p.searchMatches = nil
		p.onSearch(p.query)
	}
	if p.selected < len(p.visible) {
		p.lastSelected = p.visible[p.selected].n.Entry.ID
	}
	p.visible = p.visible[:0]
	for _, f := range p.flat {
		if p.passes(f) {
			p.visible = append(p.visible, f)
		}
	}
	if len(p.folded) > 0 {
		skip := map[string]bool{}
		for _, f := range p.flat {
			if par := f.n.Entry.Parent; par != "" && (p.folded[par] || skip[par]) {
				skip[f.n.Entry.ID] = true
			}
		}
		kept := p.visible[:0]
		for _, f := range p.visible {
			if !skip[f.n.Entry.ID] {
				kept = append(kept, f)
			}
		}
		p.visible = kept
	}
	p.layout()
	if p.lastSelected != "" {
		p.selected = p.nearestVisible(p.lastSelected)
	} else {
		p.selected = min(p.selected, max(0, len(p.visible)-1))
	}
	if p.selected < len(p.visible) {
		p.lastSelected = p.visible[p.selected].n.Entry.ID
	}
}

// nearestVisible finds id, or its nearest visible ancestor, in the view.
func (p *treePicker) nearestVisible(id string) int {
	if len(p.visible) == 0 {
		return 0
	}
	at := map[string]int{}
	for i, f := range p.visible {
		at[f.n.Entry.ID] = i
	}
	for seen := map[string]bool{}; id != "" && !seen[id]; {
		seen[id] = true
		if i, ok := at[id]; ok {
			return i
		}
		f := p.byID[id]
		if f == nil {
			break
		}
		id = f.n.Entry.Parent
	}
	return len(p.visible) - 1
}

// layout recomputes indentation, connectors and gutters for the visible
// tree, where hidden entries hand their children to the nearest visible
// ancestor. Indentation follows pi: a branch point indents its children,
// the first generation after a branch indents once more, and single-child
// chains stay flat.
func (p *treePicker) layout() {
	p.visParent, p.visChildren = map[string]string{}, map[string][]string{}
	if len(p.visible) == 0 {
		return
	}
	vis := map[string]*flatNode{}
	for _, f := range p.visible {
		vis[f.n.Entry.ID] = f
	}
	var roots []string
	for _, f := range p.visible {
		anc := ""
		for id, seen := f.n.Entry.Parent, map[string]bool{}; id != "" && !seen[id]; {
			seen[id] = true
			if vis[id] != nil {
				anc = id
				break
			}
			g := p.byID[id]
			if g == nil {
				break
			}
			id = g.n.Entry.Parent
		}
		p.visParent[f.n.Entry.ID] = anc
		if anc == "" {
			roots = append(roots, f.n.Entry.ID)
		} else {
			p.visChildren[anc] = append(p.visChildren[anc], f.n.Entry.ID)
		}
	}
	p.multipleRoots = len(roots) > 1

	type item struct {
		id                                  string
		indent                              int
		justBranched, showConnector, isLast bool
		gutters                             []gutter
		virtualRoot                         bool
	}
	var stack []item
	for i, root := range slices.Backward(roots) {
		in := 0
		if p.multipleRoots {
			in = 1
		}
		stack = append(stack, item{root, in, p.multipleRoots, p.multipleRoots, i == len(roots)-1, nil, p.multipleRoots})
	}
	for len(stack) > 0 {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		f := vis[it.id]
		f.indent, f.showConnector, f.isLast, f.gutters, f.virtualRoot = it.indent, it.showConnector, it.isLast, it.gutters, it.virtualRoot
		kids := p.visChildren[it.id]
		multi := len(kids) > 1
		childIndent := it.indent
		if multi || (it.justBranched && it.indent > 0) {
			childIndent++
		}
		childGutters := it.gutters
		if it.showConnector && !it.virtualRoot {
			pos := max(0, p.displayIndent(it.indent)-1)
			childGutters = append(append([]gutter(nil), it.gutters...), gutter{pos, !it.isLast})
		}
		for i, kid := range slices.Backward(kids) {
			stack = append(stack, item{kid, childIndent, multi, multi, i == len(kids)-1, childGutters, false})
		}
	}
}

func (p *treePicker) displayIndent(indent int) int {
	if p.multipleRoots {
		return max(0, indent-1)
	}
	return indent
}

// foldable: the node has visible children and starts a segment (a root,
// or a child of a branch point).
func (p *treePicker) foldable(id string) bool {
	if len(p.visChildren[id]) == 0 {
		return false
	}
	par, ok := p.visParent[id]
	if !ok || par == "" {
		return true
	}
	return len(p.visChildren[par]) > 1
}

// segmentStart finds the next branch segment start: up walks the visible
// parents, down follows first children to the next branch point.
func (p *treePicker) segmentStart(up bool) int {
	if p.selected >= len(p.visible) {
		return p.selected
	}
	at := map[string]int{}
	for i, f := range p.visible {
		at[f.n.Entry.ID] = i
	}
	id := p.visible[p.selected].n.Entry.ID
	if !up {
		for {
			kids := p.visChildren[id]
			if len(kids) == 0 {
				return at[id]
			}
			if len(kids) > 1 {
				return at[kids[0]]
			}
			id = kids[0]
		}
	}
	for {
		par := p.visParent[id]
		if par == "" {
			return at[id]
		}
		if len(p.visChildren[par]) > 1 && at[id] < p.selected {
			return at[id]
		}
		id = par
	}
}

func (p *treePicker) setFilter(f treeFilter) {
	p.filter = f
	p.folded = map[string]bool{}
	p.applyFilter()
}

func (p *treePicker) toggleFilter(f treeFilter) {
	if p.filter == f {
		f = filterDefault
	}
	p.setFilter(f)
}

func (p *treePicker) HandleInput(data string) {
	if p.label != nil {
		p.label.HandleInput(data)
		return
	}
	n := len(p.visible)
	cur := ""
	if p.selected < n {
		cur = p.visible[p.selected].n.Entry.ID
	}
	switch data {
	case "\x1b[1;3D", "\x1b[1;9D": // alt+left in xterm / iTerm2
		data = "\x1bb"
	case "\x1b[1;3C", "\x1b[1;9C":
		data = "\x1bf"
	case "\x1b[111;6u", "\x1b[27;6;111~": // shift+ctrl+o (CSI u / modifyOtherKeys)
		p.setFilter((p.filter + 4) % 5)
		return
	case "L": // shift+l
		if cur != "" {
			p.label = &labelInput{text: p.visible[p.selected].n.Label}
			p.label.onDone = func(save bool, text string) {
				p.label = nil
				if save {
					text = strings.TrimSpace(text)
					f := p.visible[p.selected]
					f.n.Label, f.n.LabelTime = text, time.Now()
					if p.onLabel != nil {
						p.onLabel(cur, text)
					}
				}
			}
		}
		return
	case "T": // shift+t
		p.showLabelTime = !p.showLabelTime
		return
	}
	switch tui.Key(data) {
	case "up":
		if n > 0 {
			p.selected = (p.selected - 1 + n) % n
		}
	case "down":
		if n > 0 {
			p.selected = (p.selected + 1) % n
		}
	case "word-left": // fold, or jump up to the start of the branch
		if cur != "" && p.foldable(cur) && !p.folded[cur] {
			p.folded[cur] = true
			p.applyFilter()
		} else {
			p.selected = p.segmentStart(true)
		}
	case "word-right":
		if p.folded[cur] {
			delete(p.folded, cur)
			p.applyFilter()
		} else {
			p.selected = p.segmentStart(false)
		}
	case "left":
		p.selected = max(0, p.selected-p.maxRows)
	case "right":
		p.selected = max(0, min(n-1, p.selected+p.maxRows))
	case "enter":
		if cur != "" && p.onSelect != nil {
			p.onSelect(cur)
		}
	case "ctrl+x":
		if p.onCopyEntry != nil && cur != "" {
			p.onCopyEntry(cur)
			return
		}
		if p.onCopy != nil && cur != "" {
			p.onCopy(p.copyText(p.visible[p.selected].n))
		}
	case "escape", "ctrl+c":
		if p.query != "" {
			p.query = ""
			p.folded = map[string]bool{}
			p.applyFilter()
		} else if p.onCancel != nil {
			p.onCancel()
		}
	case "ctrl+d":
		p.setFilter(filterDefault)
	case "ctrl+t":
		p.toggleFilter(filterNoTools)
	case "ctrl+u":
		p.toggleFilter(filterUser)
	case "ctrl+l":
		p.toggleFilter(filterLabeled)
	case "ctrl+a":
		p.toggleFilter(filterAll)
	case "ctrl+o":
		p.setFilter((p.filter + 1) % 5)
	case "backspace":
		if r := []rune(p.query); len(r) > 0 {
			p.query = string(r[:len(r)-1])
			p.folded = map[string]bool{}
			p.applyFilter()
		}
	default:
		if tui.Printable(data) {
			p.query += data
			p.folded = map[string]bool{}
			p.applyFilter()
		}
	}
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func (p *treePicker) searchText(n *session.Node) string {
	e := n.Entry
	parts := []string{n.Label}
	switch e.Type {
	case session.TypeMessage:
		if m := e.Message; m != nil {
			parts = append(parts, m.Role, m.Content)
			if m.Role == "tool" {
				parts = append(parts, p.calls[m.ToolCallID])
			}
		}
	case session.TypeCompaction:
		parts = append(parts, "compaction")
	case session.TypeBranchSummary:
		parts = append(parts, "branch summary", e.Summary)
	case session.TypeBashExecution:
		if x := e.Bash; x != nil {
			parts = append(parts, "bash", x.Command, x.Output)
		}
	case session.TypeModel:
		parts = append(parts, "model", e.Model)
	case session.TypeEffort:
		parts = append(parts, "thinking", e.Effort)
	case session.TypeName:
		parts = append(parts, "title", e.Name)
	case session.TypeLabel:
		parts = append(parts, "label", e.Label)
	case session.TypeGoal:
		parts = append(parts, "goal")
	case session.TypeBranch:
		parts = append(parts, "branch")
	case session.TypeBlockDisplay:
		parts = append(parts, "display", e.Ext)
	case session.TypeExtText:
		parts = append(parts, "display", e.Ext, e.Title)
	}
	return strings.Join(parts, " ")
}

// copyText is what ctrl+x copies: the full message, command or notes.
func (p *treePicker) copyText(n *session.Node) string {
	e := n.Entry
	switch e.Type {
	case session.TypeMessage:
		if m := e.Message; m != nil {
			if m.Role == "tool" && strings.TrimSpace(m.Content) == "" {
				return p.calls[m.ToolCallID]
			}
			return m.Content
		}
	case session.TypeCompaction:
		return e.Notes
	case session.TypeBranchSummary:
		return e.Summary
	case session.TypeBashExecution:
		if x := e.Bash; x != nil {
			return agent.BashExecutionText(*x)
		}
	}
	return ""
}

func (p *treePicker) entryText(n *session.Node) string {
	e := n.Entry
	switch e.Type {
	case session.TypeMessage:
		m := e.Message
		if m == nil {
			return ""
		}
		switch m.Role {
		case "user":
			if after, ok := strings.CutPrefix(m.Content, events.Prefix); ok {
				return tui.FG(5, "[event]: ") + clipRunes(oneLine(after), 200)
			}
			return tui.FG(6, "user: ") + clipRunes(oneLine(m.Content), 200)
		case "assistant":
			if t := clipRunes(oneLine(m.Content), 200); t != "" {
				return tui.FG(2, "assistant: ") + t
			}
			return tui.FG(2, "assistant: ") + tui.Dim("(no content)")
		case "tool":
			cmd := oneLine(p.calls[m.ToolCallID])
			short := clipRunes(cmd, 50)
			if short != cmd {
				short += "..."
			}
			s := tui.Dim("[bash: " + short + "]")
			if t := e.Tool; t != nil && (t.ExitCode != 0 || t.TimedOut) && !t.Canceled {
				s = tui.FG(1, "[bash: "+short+"]")
			}
			return s
		}
		return tui.Dim("[" + m.Role + "]")
	case session.TypeCompaction:
		return tui.FG(6, fmt.Sprintf("[compaction: %dk tokens]", (e.TokensBefore+500)/1000))
	case session.TypeBranchSummary:
		return tui.FG(5, "[branch summary]: ") + clipRunes(oneLine(e.Summary), 200)
	case session.TypeBashExecution:
		x := e.Bash
		if x == nil {
			return ""
		}
		s := tui.FG(5, "[bash: "+clipRunes(oneLine(x.Command), 50)+"]")
		if x.Exclude {
			s += tui.Dim(" (not sent to the model)")
		}
		return s
	case session.TypeModel:
		return tui.Dim("[model: " + e.Model + "]")
	case session.TypeEffort:
		return tui.Dim("[thinking: " + e.Effort + "]")
	case session.TypeName:
		return tui.Dim("[title: " + e.Name + "]")
	case session.TypeGoal:
		return tui.Dim("[goal]")
	case session.TypeBranch:
		return tui.Dim("[branch]")
	case session.TypeBlockDisplay:
		return tui.Dim("[display: " + e.Ext + "]")
	case session.TypeExtText:
		return tui.Dim("[" + e.Ext + ": " + clipRunes(oneLine(e.Title), 80) + "]")
	case session.TypeLabel:
		if e.Label == "" {
			return tui.Dim("[label: (cleared)]")
		}
		return tui.Dim("[label: " + e.Label + "]")
	}
	return ""
}

func labelTime(t time.Time) string {
	now := time.Now()
	switch {
	case t.Year() == now.Year() && t.YearDay() == now.YearDay():
		return t.Format("15:04")
	case t.Year() == now.Year():
		return t.Format("1/2 15:04")
	}
	return t.Format("06/1/2 15:04")
}

// treeHelp lists the keys like pi's hint line, wrapped to width.
func treeHelp(width int) []string {
	branch := "ctrl+←/→"
	if runtime.GOOS == "darwin" {
		branch = "option+←/→"
	}
	items := []string{"↑/↓ move", "←/→ page", branch + " branch", "ctrl+x copy", "shift+l label",
		"shift+t label time", "filters ctrl+d/t/u/l/a", "cycle ctrl+o/shift+ctrl+o"}
	var out []string
	line := ""
	for _, it := range items {
		switch {
		case line == "":
			line = "  " + it
		case tui.VisibleWidth(line+" · "+it) <= width:
			line += " · " + it
		default:
			out = append(out, line)
			line = "  " + it
		}
	}
	if line != "" {
		out = append(out, line)
	}
	for i, l := range out {
		out[i] = tui.Truncate(tui.Dim(l), width, "…")
	}
	return out
}

func (p *treePicker) Render(width int) []string {
	rule := tui.Dim(strings.Repeat("─", width))
	out := []string{rule, tui.Bold("  Session Tree")}
	out = append(out, treeHelp(width)...)
	out = append(out, tui.Truncate(tui.Dim("  Type to search:")+" "+tui.FG(6, p.query), width, "…"), rule)
	if p.label != nil {
		out = append(out, p.label.Render(width)...)
		return append(out, rule)
	}
	if len(p.visible) == 0 {
		out = append(out, tui.Dim("  No entries found"), tui.Dim("  (0/0)"+filterTags[p.filter]))
		return append(out, rule)
	}
	start := max(0, min(p.selected-p.maxRows/2, len(p.visible)-p.maxRows))
	end := min(start+p.maxRows, len(p.visible))
	for i := start; i < end; i++ {
		out = append(out, p.row(p.visible[i], i == p.selected, width))
	}
	tags := filterTags[p.filter]
	if p.showLabelTime {
		tags += " [+label time]"
	}
	out = append(out, tui.Dim(fmt.Sprintf("  (%d/%d)%s", p.selected+1, len(p.visible), tags)), rule)
	return out
}

// row draws one node: cursor, gutters and connector, fold and active-path
// markers, label, then the entry.
func (p *treePicker) row(f *flatNode, selected bool, width int) string {
	id := f.n.Entry.ID
	cursor := "  "
	if selected {
		cursor = tui.FG(6, "› ")
	}
	ind := p.displayIndent(f.indent)
	connector := f.showConnector && !f.virtualRoot
	connPos := -1
	if connector {
		connPos = ind - 1
	}
	var b strings.Builder
	for i := 0; i < ind*3; i++ {
		level, pos := i/3, i%3
		var g *gutter
		for j := range f.gutters {
			if f.gutters[j].pos == level {
				g = &f.gutters[j]
				break
			}
		}
		switch {
		case g != nil:
			if pos == 0 && g.show {
				b.WriteString("│")
			} else {
				b.WriteString(" ")
			}
		case connector && level == connPos:
			switch pos {
			case 0:
				if f.isLast {
					b.WriteString("└")
				} else {
					b.WriteString("├")
				}
			case 1:
				switch {
				case p.folded[id]:
					b.WriteString("⊞")
				case p.foldable(id):
					b.WriteString("⊟")
				default:
					b.WriteString("─")
				}
			default:
				b.WriteString(" ")
			}
		default:
			b.WriteString(" ")
		}
	}
	line := tui.Dim(b.String())
	if p.folded[id] && !connector {
		line += tui.FG(6, "⊞ ")
	}
	if p.active[id] {
		line += tui.FG(6, "• ")
	}
	if l := f.n.Label; l != "" {
		line += tui.FG(3, "["+l+"] ")
		if p.showLabelTime && !f.n.LabelTime.IsZero() {
			line += tui.Dim(labelTime(f.n.LabelTime.Local()) + " ")
		}
	}
	text := p.entryText(f.n)
	if selected {
		text = tui.Bold(text)
	}
	line = tui.Truncate(cursor+line+text, width, "…")
	if selected {
		line = band(line, width)
	}
	return line
}

// labelInput edits a label on one line (enter saves, esc cancels; empty
// removes the label). title and hint replace the label's wording when it
// asks for something else.
type labelInput struct {
	text        string
	title, hint string
	placeholder string // for /remote's clients
	onDone      func(save bool, text string)
}

func (l *labelInput) HandleInput(data string) {
	switch tui.Key(data) {
	case "enter":
		l.onDone(true, l.text)
	case "escape", "ctrl+c":
		l.onDone(false, "")
	case "backspace":
		if r := []rune(l.text); len(r) > 0 {
			l.text = string(r[:len(r)-1])
		}
	default:
		if tui.Printable(data) {
			l.text += data
		} else if after, ok := strings.CutPrefix(data, tui.PastePrefix); ok {
			l.text += oneLine(after)
		}
	}
}

func (l *labelInput) Render(width int) []string {
	title, hint := l.title, l.hint
	if title == "" {
		title, hint = "Label (empty to remove):", "enter save  esc cancel"
	}
	return []string{
		tui.Dim("  " + title),
		tui.Truncate("  "+tui.FG(6, "› ")+l.text+tui.CursorMarker, width, "…"),
		tui.Dim("  " + hint),
	}
}

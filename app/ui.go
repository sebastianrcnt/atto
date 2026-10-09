package app

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/tui"
	"github.com/sebastianrcnt/atto/ui"
)

// Site components hold only client-local interaction state. All shared trees
// and action revisions come from the worker's UI snapshot/event stream.
func (a *App) applyUI(snap *ui.Snapshot) {
	if a.elements == nil {
		a.elements = map[ui.Match]*tui.Elements{}
	}
	seen := map[ui.Match]bool{}
	if snap != nil {
		for _, i := range snap.Instances {
			m := ui.Match{Site: i.Site, ID: i.ID}
			seen[m] = true
			e := a.elements[m]
			if e == nil {
				e = &tui.Elements{}
				a.elements[m] = e
				e.OnAction = a.uiAction
				e.OnEscape = func() { a.focusedSite = nil; a.ui.SetFocus(a.editor) }
			}
			if e.Rev != i.Rev {
				_ = e.SetTree(i.Site, i.ID, i.Rev, i.Tree)
			}
		}
	}
	for m, e := range a.elements {
		if !seen[m] {
			if a.focusedSite == e {
				a.focusedSite = nil
				a.ui.SetFocus(a.editor)
			}
			delete(a.elements, m)
		}
	}
	a.ui.Side = nil
	a.ui.SideColumns = 0
	if panes := a.liveUI(ui.Pane); len(panes) > 0 {
		a.ui.Side = &paneDock{a}
		a.ui.SideColumns = max(32, panes[0].Options.Columns)
		if panes[0].Options.Placement == "abovePrompt" {
			a.ui.Side = nil
		}
	}
}
func (a *App) uiAction(action ui.Action) {
	p := map[string]any{"site": action.Site, "id": action.ID, "key": action.Key, "type": action.Type, "rev": action.Rev}
	if action.Value != nil {
		p["value"] = *action.Value
	}
	a.rpc("ui/event", p, func(raw json.RawMessage, err error) {
		if err != nil {
			a.errorNotice(err)
			a.reread()
		}
	})
}
func (a *App) liveUI(site ui.Site) []ui.Instance {
	var out []ui.Instance
	if a.view.Info.UI != nil {
		for _, i := range a.view.Info.UI.Instances {
			if i.Site == site {
				out = append(out, i)
			}
		}
	}
	return out
}
func (a *App) focusUI(e *tui.Elements) {
	if e == nil || a.editor.Text() != "" || a.modal != nil {
		return
	}
	a.activateSiteFocus(e)
}
func (a *App) activateSiteFocus(e *tui.Elements) {
	a.focusedSite = e
	for index, i := range a.liveUI(ui.Pane) {
		if i.ID == e.ID {
			a.paneTab = index
			if i.Options.CloseOnEscape {
				e.OnEscape = func() {
					a.uiAction(ui.Action{Site: ui.Pane, ID: e.ID, Rev: e.Rev, Key: "$site", Type: ui.CloseEvent})
					a.focusedSite = nil
					a.ui.SetFocus(a.editor)
				}
			}
			break
		}
	}
	a.ui.SetFocus(e)
}
func (a *App) focusFirstUI() bool {
	for _, i := range append(a.liveUI(ui.Pane), a.liveUI(ui.Band)...) {
		if e := a.elements[ui.Match{Site: i.Site, ID: i.ID}]; e != nil {
			a.focusUI(e)
			return true
		}
	}
	return false
}
func (a *App) uiNotification(n server.Notification) {
	var item struct {
		ui.Instance
		ActionsEnabled bool `json:"actionsEnabled"`
	}
	if json.Unmarshal(n.Params, &item) == nil && ui.IsItem(item.Site) && n.Method == "ui/render" {
		for _, w := range a.view.Items {
			if w.ID == item.ID {
				w.UIDisplay = &server.UIDisplay{Rev: item.Rev, Tree: item.Tree, ActionsEnabled: item.ActionsEnabled}
				a.applyUIItem(w)
				break
			}
		}
	}

	a.applyUI(a.view.Info.UI)
	if p := a.view.Info.Prompt; p != nil && a.modal == nil {
		a.promptOpened(*p)
	}
	var p struct {
		Site  ui.Site `json:"site"`
		ID    string  `json:"id"`
		Focus string  `json:"focusClientId"`
	}
	if json.Unmarshal(n.Params, &p) == nil && a.conn != nil && p.Focus == a.conn.id {
		a.focusUI(a.elements[ui.Match{Site: p.Site, ID: p.ID}])
	}
}
func (a *App) renderPortable(width int) []string {
	columns, _ := a.ui.Size()
	if a.conn != nil && columns != a.uiCapabilityWidth {
		a.uiCapabilityWidth = columns
		a.rpc("ui/capabilities", map[string]any{"surface": "terminal", "width": columns, "elements": ui.Catalog()}, nil)
	}
	a.portableHits = nil
	var out []string
	for _, i := range a.liveUI(ui.Band) {
		if e := a.elements[ui.Match{Site: i.Site, ID: i.ID}]; e != nil {
			lines := e.Render(width)
			a.portableHits = append(a.portableHits, portableHit{element: e, start: len(out), end: len(out) + len(lines)})
			out = append(out, lines...)
		}
	}
	w, h := a.ui.Size()
	for index, i := range a.liveUI(ui.Pane) {
		side, _, rows := tui.PaneLayout(w, h, i.Options)
		if side && a.ui.Mode == tui.Fullscreen {
			continue
		}
		if index != a.paneTab {
			continue
		}
		lines := a.paneLines(i, width, rows)
		a.portableHits = append(a.portableHits, portableHit{element: a.elements[ui.Match{Site: i.Site, ID: i.ID}], start: len(out), end: len(out) + len(lines), pane: true})
		out = append(out, lines...)
	}
	for _, i := range a.liveUI(ui.Toast) {
		if e := a.elements[ui.Match{Site: i.Site, ID: i.ID}]; e != nil {
			lines := e.Render(width)
			if len(lines) > 0 {
				out = append(out, tui.Truncate(lines[0], width, "…"))
			}
		}
	}
	return out
}
func (a *App) paneLines(i ui.Instance, width, rows int) []string {
	e := a.elements[ui.Match{Site: i.Site, ID: i.ID}]
	if e == nil {
		return nil
	}
	tabs := a.liveUI(ui.Pane)
	var titles []string
	for index, p := range tabs {
		title := ui.CleanText(p.Options.Title)
		if index == a.paneTab {
			title = tui.Bold(title)
		}
		titles = append(titles, title)
	}
	head := tui.Truncate(strings.Join(titles, tui.Dim(" │ "))+tui.Dim(" · tab focus · esc return"), width, "…")
	lines := e.Render(width)
	rows = max(2, rows)
	m := ui.Match{Site: i.Site, ID: i.ID}
	if a.paneScroll == nil {
		a.paneScroll = map[ui.Match]int{}
	}
	offset := min(a.paneScroll[m], max(0, len(lines)-rows+1))
	if a.focusedSite == e {
		if focus := e.FocusLine(); focus >= 0 {
			if focus < offset {
				offset = focus
			} else if focus >= offset+rows-1 {
				offset = focus - rows + 2
			}
		}
	}
	a.paneScroll[m] = offset
	lines = lines[offset:min(len(lines), offset+rows-1)]
	return append([]string{head}, lines...)
}

type paneDock struct{ a *App }

func (p *paneDock) Render(width int) []string {
	panes := p.a.liveUI(ui.Pane)
	if len(panes) == 0 {
		return nil
	}
	index := min(p.a.paneTab, len(panes)-1)
	_, h := p.a.ui.Size()
	return p.a.paneLines(panes[index], width, max(1, h))
}
func (p *paneDock) Click(line int) bool {
	panes := p.a.liveUI(ui.Pane)
	if len(panes) == 0 {
		return false
	}
	if line == 0 {
		p.a.paneTab = (p.a.paneTab + 1) % len(panes)
		return true
	}
	e := p.a.elements[ui.Match{Site: ui.Pane, ID: panes[min(p.a.paneTab, len(panes)-1)].ID}]
	p.a.activateSiteFocus(e)
	offset := p.a.paneScroll[ui.Match{Site: ui.Pane, ID: e.ID}]
	return e.Click(line - 1 + offset)
}

// Additional provider slots retain generic priority-aware layout. Built-in
// slots use the native status compositor in ui_status.go.
func (a *App) renderAdditionalUIStatus(width int) []string {
	items := a.liveUI(ui.Status)
	var extra []ui.Instance
	for _, i := range items {
		if !isBuiltinStatus(i.ID) {
			extra = append(extra, i)
		}
	}
	items = extra
	sequence := map[string]int{}
	for index, i := range items {
		sequence[i.ID] = index
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Options.Priority > items[j].Options.Priority })
	type rendered struct {
		instance ui.Instance
		text     string
		order    int
	}
	var rows [2][]rendered
	used := [2]int{}
	var out []string
	for index, i := range items {
		e := a.elements[ui.Match{Site: i.Site, ID: i.ID}]
		if e == nil {
			continue
		}
		lines := e.Render(width)
		if len(lines) == 0 || lines[0] == "" {
			continue
		}
		text := lines[0]
		w := tui.VisibleWidth(text)
		row := -1
		for r := range 2 {
			extra := 0
			if used[r] > 0 {
				extra = 3
			}
			if used[r]+w+extra <= width {
				row = r
				used[r] += w + extra
				break
			}
		}
		if row < 0 {
			if used[0] == 0 {
				text = tui.Truncate(text, width, "…")
				row = 0
				used[0] = width
			} else {
				continue
			}
		}
		rows[row] = append(rows[row], rendered{i, text, index})
	}
	for _, row := range rows {
		if len(row) == 0 {
			continue
		}
		sort.SliceStable(row, func(i, j int) bool { return sequence[row[i].instance.ID] < sequence[row[j].instance.ID] })
		var start, end []string
		for _, r := range row {
			if r.instance.Options.Align == "end" {
				end = append(end, r.text)
			} else {
				start = append(start, r.text)
			}
		}
		left, right := strings.Join(start, tui.Dim(" · ")), strings.Join(end, tui.Dim(" · "))
		gap := max(1, width-tui.VisibleWidth(left)-tui.VisibleWidth(right))
		if right == "" {
			gap = 0
		}
		out = append(out, tui.Truncate(left+strings.Repeat(" ", gap)+right, width, "…"))
	}
	return out
}
func (a *App) portableBlock(w server.Item) *tui.Elements {
	if a.uiBlocks == nil {
		a.uiBlocks = map[string]*tui.Elements{}
	}
	e := a.uiBlocks[w.ID]
	if e == nil {
		e = &tui.Elements{OnAction: a.uiAction}
		a.uiBlocks[w.ID] = e
		a.add(e)
	}
	tree := w.UITree
	if !w.ActionsEnabled {
		tree = passiveUITree(tree)
	}
	_ = e.SetTree(ui.Transcript, w.UIID, w.UIRev, tree)
	return e
}

func (p *paneDock) Scroll(delta int) {
	panes := p.a.liveUI(ui.Pane)
	if len(panes) == 0 {
		return
	}
	i := panes[min(p.a.paneTab, len(panes)-1)]
	m := ui.Match{Site: i.Site, ID: i.ID}
	if p.a.paneScroll == nil {
		p.a.paneScroll = map[ui.Match]int{}
	}
	p.a.paneScroll[m] = max(0, p.a.paneScroll[m]+delta)
}

type portableHit struct {
	element    *tui.Elements
	start, end int
	pane       bool
}
type portableFooter struct{ a *App }

func (p portableFooter) Render(width int) []string { return p.a.renderPortable(width) }
func (p portableFooter) ClickAt(column, line int) bool {
	for _, hit := range p.a.portableHits {
		if line >= hit.start && line < hit.end {
			local := line - hit.start
			if hit.pane && local == 0 {
				panes := p.a.liveUI(ui.Pane)
				if len(panes) > 0 {
					p.a.paneTab = (p.a.paneTab + 1) % len(panes)
					return true
				}
			}
			p.a.activateSiteFocus(hit.element)
			if hit.pane {
				local--
				local += p.a.paneScroll[ui.Match{Site: ui.Pane, ID: hit.element.ID}]
			}
			return hit.element.ClickAt(column, local)
		}
	}
	return false
}
func (p *paneDock) ClickAt(column, line int) bool {
	if line == 0 {
		return p.Click(line)
	}
	panes := p.a.liveUI(ui.Pane)
	if len(panes) == 0 {
		return false
	}
	i := panes[min(p.a.paneTab, len(panes)-1)]
	e := p.a.elements[ui.Match{Site: ui.Pane, ID: i.ID}]
	p.a.activateSiteFocus(e)
	return e.ClickAt(column, line-1+p.a.paneScroll[ui.Match{Site: ui.Pane, ID: i.ID}])
}

type inputFooter struct{ a *App }

func (p inputFooter) Render(width int) []string { return p.a.renderInput(width) }
func (p inputFooter) ClickAt(column, line int) bool {
	if p.a.modal == nil || line < 1 {
		return false
	}
	if c, ok := p.a.modal.(tui.CellClickable); ok {
		return c.ClickAt(column, line-1)
	}
	if c, ok := p.a.modal.(tui.Clickable); ok {
		return c.Click(line - 1)
	}
	return false
}

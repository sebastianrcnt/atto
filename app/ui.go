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
	a.focusedSite = e
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
	a.applyUI(a.view.Info.UI)
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
	var out []string
	for _, i := range a.liveUI(ui.Band) {
		if e := a.elements[ui.Match{Site: i.Site, ID: i.ID}]; e != nil {
			out = append(out, e.Render(width)...)
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
		out = append(out, a.paneLines(i, width, rows)...)
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
		title := p.Options.Title
		if index == a.paneTab {
			title = tui.Bold(title)
		}
		titles = append(titles, title)
	}
	head := tui.Truncate(strings.Join(titles, tui.Dim(" │ "))+tui.Dim(" · tab focus · esc return"), width, "…")
	lines := e.Render(width)
	rows = max(1, rows)
	if len(lines) > rows-1 {
		lines = lines[:rows-1]
	}
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
	p.a.focusUI(e)
	return e.Click(line - 1)
}

// renderUIStatus is priority-aware and passive. One slot is one clipped row;
// high-priority goal/activity are retained before low-priority contributions.
func (a *App) renderUIStatus(width int) []string {
	items := a.liveUI(ui.Status)
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
		sort.SliceStable(row, func(i, j int) bool { return row[i].instance.Rev < row[j].instance.Rev })
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
	if flags := a.extensionStatus(); len(flags) > 0 {
		out = append(out, tui.Truncate(strings.Join(flags, tui.Dim(" · ")), width, "…"))
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
	_ = e.SetTree(ui.Transcript, w.UIID, w.UIRev, w.UITree)
	return e
}

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sebastianrcnt/atto/daemon"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

// The agent command center, after codex's: every atto session, running
// (the daemon's panes) or saved, grouped by project, with tabs for what
// they are doing and the selected one's details on the right. ← on an
// empty prompt (or /agents) opens it, → or Enter goes to the selected
// session: the daemon shows that pane on this terminal, or opens a saved
// session in a new one; ← or Esc comes back. n starts a new session in the
// selected one's project, / searches. "atto agents" shows it by itself.
// Agents other agents started are not listed: they belong to their
// session (atto agent list).

// listPanes lists the daemon's panes; tests replace it.
var listPanes = daemon.List

// listSaved lists saved sessions, newest first; tests replace it.
var listSaved = func() []session.Summary {
	l, _ := session.List("", false)
	return l
}

// centerRefresh is how often the open center reloads.
const centerRefresh = 2 * time.Second

// centerSaved caps the saved sessions listed.
const centerSaved = 200

// centerRows is how many list rows show at once.
const centerRows = 24

// Center tabs, as codex's (and A2A's task states).
const (
	tabAll = iota
	tabNeedsYou
	tabWorking
	tabReady
	tabInactive
)

var tabNames = []string{"All", "Needs you", "Working", "Ready", "Inactive"}

// centerItem is a session in the center.
type centerItem struct {
	id, title, cwd string
	branch, prompt string
	pane           *daemon.Pane // nil: saved, not running
	current        bool         // this atto's own session
	tab            int          // tabNeedsYou .. tabInactive
	updated        time.Time
}

func (it centerItem) status() string {
	switch it.tab {
	case tabNeedsYou:
		return "Needs you"
	case tabWorking:
		return "Working"
	case tabReady:
		return "Ready"
	}
	return "Inactive"
}

type agentCenter struct {
	a        *App // nil: on its own (atto agents)
	onClose  func()
	onSwitch func(pane int)
	// onOpen opens a saved session (cwd is its directory); onNew starts a
	// session in cwd.
	onOpen func(id, cwd string)
	onNew  func(cwd string)

	items  []centerItem
	tab    int
	sel    int // index into shown()
	top    int // first shown row
	search string
	typing bool // the search box has focus
	msgs   map[string]string
}

func (a *App) cmdAgents(string) { a.openAgents(tabAll) }

func (a *App) openAgents(tab int) {
	c := &agentCenter{a: a, tab: tab, onClose: a.closeModal}
	c.onSwitch = func(id int) {
		if a.pane.on {
			a.ui.Emit(daemon.MarkerSeq("switch", strconv.Itoa(id)))
			return
		}
		a.notice("This atto runs outside the daemon: show that session from a shell with atto attach %d.", id)
	}
	c.onOpen = func(id, cwd string) {
		if a.pane.on {
			a.ui.Emit(daemon.MarkerSeq("open", id, cwd))
			return
		}
		if path, err := session.Find(id); err == nil {
			a.resume(path)
		}
	}
	c.onNew = func(cwd string) {
		if a.pane.on {
			a.ui.Emit(daemon.MarkerSeq("new", cwd))
			return
		}
		a.cmdClear("")
	}
	c.reload()
	c.selectCurrent()
	a.openModal(c)
	go func() { // reload while open; ends once the center is gone
		t := time.NewTicker(centerRefresh)
		defer t.Stop()
		for range t.C {
			gone := make(chan bool, 1)
			a.ui.Do(func() {
				if a.modal != c {
					gone <- true
					return
				}
				c.reload()
				gone <- false
			})
			if <-gone {
				return
			}
		}
	}()
}

// reload gathers the sessions: the daemon's panes, this one (when atto
// runs directly), then the saved ones, newest first.
func (c *agentCenter) reload() {
	var keep string
	if sh := c.shown(); c.sel < len(sh) {
		keep = sh[c.sel].id
	}
	panes, _ := listPanes()
	self, _ := strconv.Atoi(os.Getenv(daemon.EnvPane))
	var items []centerItem
	seen := map[string]bool{}
	for i := range panes {
		p := panes[i]
		it := centerItem{id: p.Session, title: p.Name, cwd: p.Cwd, pane: &p, updated: p.Active,
			current: c.a != nil && (p.ID == self || (p.Session != "" && p.Session == c.a.sess.ID))}
		switch p.State {
		case "working":
			it.tab = tabWorking
		case "waiting":
			it.tab = tabNeedsYou
		default:
			it.tab = tabReady
		}
		items = append(items, it)
		seen[p.Session] = true
	}
	if a := c.a; a != nil && !seen[a.sess.ID] {
		it := centerItem{id: a.sess.ID, title: a.sessName, cwd: a.cwd, current: true, updated: time.Now(), tab: tabReady}
		switch {
		case a.busy:
			it.tab = tabWorking
		case a.goal.Held() && a.goal.Active():
			it.tab = tabNeedsYou
		}
		items = append(items, it)
		seen[a.sess.ID] = true
	}
	for i, s := range listSaved() {
		if i >= centerSaved {
			break
		}
		if s.AgentOf != "" || s.External {
			continue
		}
		title := s.Name
		if title == "" {
			title = s.Preview
		}
		if seen[s.ID] {
			for j := range items { // fill in what the pane doesn't know
				if items[j].id == s.ID {
					if items[j].title == "" {
						items[j].title = title
					}
					items[j].branch, items[j].prompt = s.Branch, s.Preview
				}
			}
			continue
		}
		items = append(items, centerItem{id: s.ID, title: title, cwd: s.Cwd, branch: s.Branch, prompt: s.Preview, updated: s.Updated, tab: tabInactive})
	}
	c.items = groupByProject(items)
	c.sel = 0
	for i, it := range c.shown() {
		if keep != "" && it.id == keep {
			c.sel = i
		}
	}
}

// groupByProject orders items by project, the projects by their latest
// session, each project's sessions newest first.
func groupByProject(items []centerItem) []centerItem {
	latest := map[string]time.Time{}
	for _, it := range items {
		if it.updated.After(latest[it.cwd]) {
			latest[it.cwd] = it.updated
		}
	}
	slices.SortStableFunc(items, func(x, y centerItem) int {
		if x.cwd != y.cwd {
			if c := latest[y.cwd].Compare(latest[x.cwd]); c != 0 {
				return c
			}
			return strings.Compare(x.cwd, y.cwd)
		}
		return y.updated.Compare(x.updated)
	})
	return items
}

// shown is the items the tab and the search let through.
func (c *agentCenter) shown() []centerItem {
	q := strings.ToLower(c.search)
	var out []centerItem
	for _, it := range c.items {
		if c.tab != tabAll && it.tab != c.tab {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(it.title+" "+it.cwd+" "+it.prompt), q) {
			continue
		}
		out = append(out, it)
	}
	return out
}

func (c *agentCenter) selectCurrent() {
	for i, it := range c.shown() {
		if it.current {
			c.sel = i
		}
	}
}

func (c *agentCenter) close() { c.onClose() }

func (c *agentCenter) HandleInput(data string) {
	key := tui.Key(data)
	if c.typing {
		switch key {
		case "escape":
			c.search, c.typing = "", false
		case "enter", "down", "up":
			c.typing = false
		case "backspace":
			if r := []rune(c.search); len(r) > 0 {
				c.search = string(r[:len(r)-1])
			}
		default:
			if r := []rune(data); len(r) == 1 && r[0] >= ' ' {
				c.search += data
			}
		}
		c.sel, c.top = 0, 0
		return
	}
	if key == "" { // printable: the letter itself
		key = data
	}
	sh := c.shown()
	switch key {
	case "escape":
		if c.search != "" {
			c.search, c.sel, c.top = "", 0, 0
			return
		}
		c.close()
	case "left", "q":
		c.close()
	case "up", "k":
		c.sel = max(c.sel-1, 0)
	case "down", "j":
		c.sel = min(c.sel+1, max(len(sh)-1, 0))
	case "tab":
		c.tab, c.sel, c.top = (c.tab+1)%len(tabNames), 0, 0
	case "shift+tab":
		c.tab, c.sel, c.top = (c.tab+len(tabNames)-1)%len(tabNames), 0, 0
	case "/":
		c.typing = true
	case "n":
		cwd := ""
		if c.sel < len(sh) {
			cwd = sh[c.sel].cwd
		} else if c.a != nil {
			cwd = c.a.cwd
		}
		c.close()
		c.onNew(cwd)
	case "enter", "right":
		if c.sel >= len(sh) {
			return
		}
		it := sh[c.sel]
		c.close()
		switch {
		case it.current:
		case it.pane != nil:
			c.onSwitch(it.pane.ID)
		default:
			c.onOpen(it.id, it.cwd)
		}
	}
}

func (c *agentCenter) Render(width int) []string {
	sh := c.shown()
	counts := make([]int, len(tabNames))
	for _, it := range c.items {
		counts[tabAll]++
		counts[it.tab]++
	}
	head := tui.Bold("Agent command center")
	if c.typing || c.search != "" {
		head += tui.Dim("  search: ") + c.search
		if c.typing {
			head += "▏"
		}
	}
	out := []string{tui.Truncate(head, width, "…")}
	var tabs []string
	for i, n := range tabNames {
		t := fmt.Sprintf(" %s %d ", n, counts[i])
		if i == c.tab {
			t = tui.BG(238, tui.Bold(t))
		} else {
			t = tui.Dim(t)
		}
		tabs = append(tabs, t)
	}
	out = append(out, tui.Truncate(strings.Join(tabs, " ")+tui.Dim("   tab/shift+tab filter"), width, "…"), tui.Dim(strings.Repeat("─", max(width, 0))))

	detailW := 0
	if width >= 100 {
		detailW = min(width*2/5, 60)
	}
	listW := width
	if detailW > 0 {
		listW = width - detailW - 3
	}
	list := c.renderList(sh, listW)
	if detailW > 0 && c.sel < len(sh) {
		detail := c.renderDetail(sh[c.sel], detailW)
		for i := 0; i < max(len(list), len(detail)); i++ {
			l, d := "", ""
			if i < len(list) {
				l = list[i]
			}
			if i < len(detail) {
				d = detail[i]
			}
			l += strings.Repeat(" ", max(0, listW-tui.VisibleWidth(l)))
			out = append(out, l+tui.Dim(" │ ")+d)
		}
	} else {
		out = append(out, list...)
	}
	hint := "↑↓ move · enter/→ open · ← back · n new · / search"
	if c.a == nil {
		hint = "↑↓ move · enter open · ← quit · n new · / search"
	}
	return append(out, "", tui.Truncate(tui.Dim(hint), width, "…"))
}

func (c *agentCenter) renderList(sh []centerItem, width int) []string {
	if len(sh) == 0 {
		msg := "No sessions here."
		if c.search != "" {
			msg = "No session matches."
		}
		return []string{tui.Dim(msg)}
	}
	if c.sel < c.top {
		c.top = c.sel
	}
	if c.sel >= c.top+centerRows {
		c.top = c.sel - centerRows + 1
	}
	statusW, ageW := 10, 8
	titleW := max(width-statusW-ageW-7, 10)
	var out []string
	group := ""
	for i, it := range sh {
		visible := i >= c.top && i < c.top+centerRows
		if it.cwd != group {
			group = it.cwd
			if visible {
				n := 0
				for _, x := range sh {
					if x.cwd == group {
						n++
					}
				}
				if len(out) > 0 {
					out = append(out, "")
				}
				out = append(out, tui.Truncate(tui.Dim(fmt.Sprintf("%s  %d", shortPath(group), n)), width, "…"))
			}
		}
		if !visible {
			continue
		}
		title := firstLine(it.title)
		if title == "" {
			title = "(new session)"
		}
		if it.current {
			title += " (here)"
		}
		mark := "○"
		switch it.tab {
		case tabWorking:
			mark = tui.FG(2, "●")
		case tabNeedsYou:
			mark = tui.FG(3, "●")
		case tabReady:
			mark = tui.FG(6, "●")
		}
		name := tui.Truncate(title, titleW, "…")
		row := " " + mark + " " + name + strings.Repeat(" ", max(1, titleW-tui.VisibleWidth(name)+2))
		row += fmt.Sprintf("%-*s %*s", statusW, it.status(), ageW, ago(it.updated))
		if i == c.sel {
			row = tui.FG(6, "›") + tui.Bold(row)
		} else {
			row = " " + row
		}
		out = append(out, tui.Truncate(row, width, "…"))
	}
	if rest := len(sh) - (c.top + centerRows); rest > 0 {
		out = append(out, "", tui.Dim(fmt.Sprintf("   %d more ↓", rest)))
	}
	return out
}

// renderDetail is the right column: what the selected session is about
// and where.
func (c *agentCenter) renderDetail(it centerItem, width int) []string {
	wrap := func(s string) []string { return tui.Wrap(s, max(width, 10)) }
	title := firstLine(it.title)
	if title == "" {
		title = "(new session)"
	}
	out := []string{tui.Bold("Task details"), ""}
	out = append(out, wrap(title)...)
	out = append(out, tui.Dim(it.status()), "")
	if msg := c.lastMessage(it.id); msg != "" {
		out = append(out, tui.Dim("Last message"))
		lines := wrap(msg)
		if len(lines) > 8 {
			lines = append(lines[:8], "…")
		}
		out = append(out, lines...)
		out = append(out, "")
	}
	out = append(out, tui.Dim("Project"))
	out = append(out, wrap(shortPath(it.cwd))...)
	if it.branch != "" {
		out = append(out, "", tui.Dim("Branch"), it.branch)
	}
	if it.pane != nil {
		shown := "detached"
		if n := it.pane.Clients; n > 0 {
			shown = fmt.Sprintf("shown on %d terminal(s)", n)
		}
		out = append(out, "", tui.Dim("Pane"), fmt.Sprintf("#%d · %s", it.pane.ID, shown))
	}
	if it.prompt != "" {
		out = append(out, "", tui.Dim("Prompt"))
		lines := wrap(firstLine(it.prompt))
		if len(lines) > 3 {
			lines = append(lines[:3], "…")
		}
		out = append(out, lines...)
	}
	return out
}

// lastMessage is a session's last answer, read once per session while the
// center is open.
func (c *agentCenter) lastMessage(id string) string {
	if id == "" {
		return ""
	}
	if c.msgs == nil {
		c.msgs = map[string]string{}
	}
	m, ok := c.msgs[id]
	if !ok {
		m = strings.TrimSpace(session.LastAssistant(id))
		c.msgs[id] = m
	}
	return m
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

// ago is a short age: 5m ago, 3h ago, 2d ago.
func ago(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// AgentsPick is what the center by itself was left with.
type AgentsPick struct {
	Pane    int    // attach to this pane
	Session string // or open this saved session
	New     bool   // or start a new session
	Cwd     string // in this directory
}

// RunAgents shows the agent center on this terminal by itself and returns
// what was picked (the zero value for nothing).
func RunAgents() (AgentsPick, error) {
	ui := tui.New(tui.NewProcessTerminal())
	done := make(chan struct{})
	var once sync.Once
	quit := func() { once.Do(func() { close(done) }) }
	var pick AgentsPick
	c := &agentCenter{onClose: quit,
		onSwitch: func(id int) { pick = AgentsPick{Pane: id} },
		onOpen:   func(id, cwd string) { pick = AgentsPick{Session: id, Cwd: cwd} },
		onNew:    func(cwd string) { pick = AgentsPick{New: true, Cwd: cwd} },
	}
	c.reload()
	ui.Body.Children = []tui.Component{c}
	ui.SetFocus(c)
	ui.OnInput = func(data string) bool {
		if tui.Key(data) == "ctrl+c" {
			quit()
			return true
		}
		return false
	}
	if err := ui.Start(); err != nil {
		return pick, err
	}
	t := time.NewTicker(centerRefresh)
	defer t.Stop()
	for {
		select {
		case <-done:
			ui.Stop()
			if pick.Cwd != "" {
				if abs, err := filepath.Abs(pick.Cwd); err == nil {
					pick.Cwd = abs
				}
			}
			return pick, nil
		case <-t.C:
			ui.Do(c.reload)
		}
	}
}

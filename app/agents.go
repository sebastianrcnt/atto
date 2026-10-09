package app

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

// The agent command center, after codex's: every atto session, running
// (daemon workers) or saved, grouped by project, with tabs for what
// they are doing and the selected one's details on the right. ← on an
// empty prompt (or /agents) opens it, → or Enter goes to the selected
// session in this same TUI; ← or Esc comes back. n starts a new session in the
// selected one's project, / searches. "atto agents" shows it by itself.
// Agents are equal sessions, shown as a tree under the session (or shell
// parent) that started them. Space folds/unfolds a tree; filtering reveals
// matching agents with their ancestors. Opening a locked agent shows its
// transcript read-only, with the usual ctrl+r refresh. Ctrl+C closes the
// center only, never interrupting the underlying session's work.

// centerRows is how many list rows the inline renderer shows at once.
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
	id, title, cwd                 string
	branch, prompt                 string
	live                           bool // a worker is running
	current                        bool // this atto's own session
	tab                            int  // tabNeedsYou .. tabInactive
	updated                        time.Time
	parent, agentPath, role, model string
	external, archived             bool
	closed                         bool
	// shell: an agent started from a shell, the root of a tree of its own;
	// virtual: a display group with no session behind it (the heading those
	// agents are shown under); isAgent: a managed agent, whatever its parent.
	shell, virtual, isAgent bool
	projectRoot             string
	spawnedBy               *session.SpawnedBy
	turn                    *agentstate.Turn
	// Tree-only presentation fields, populated by centerTree.
	depth           int
	prefix, project string
	children        bool
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
	a       *App // nil: on its own (atto agents)
	onClose func()
	// onOpen opens a saved session (cwd is its directory); onNew starts a
	// session in cwd.
	onOpen func(id, cwd string)
	onNew  func(cwd string)

	scope           string // resume picker: this directory only
	resume          bool
	archiveView     bool
	picking         bool
	items           []centerItem
	tab             int
	sel             int // index into shown()
	top             int // first shown row
	search          string
	typing          bool // the search box has focus
	flat            bool // not grouped by project (g)
	msgs            map[string]string
	loaded          bool
	collapsed       map[string]bool // explicit and default folds, retained across refreshes
	client          *server.Client
	release         func()
	confirm, notice string
	pending         centerItem
	pendingMethod   string
}

func (a *App) cmdAgents(string) { a.openAgents(tabAll) }

func (a *App) openAgents(tab int) {
	c := &agentCenter{a: a, tab: tab, onClose: a.closeModal}
	c.onOpen = func(id, cwd string) { a.resumeID(id) }
	c.onNew = func(cwd string) {
		if !a.workers() {
			a.cmdClear("")
			return
		}
		a.openThread("", map[string]any{"cwd": cwd}, func(info server.ThreadInfo, old *conn) { a.switchTo(info, "new", old, nil) })
	}
	c.connect()
	c.apply(nil)
	c.selectCurrent()
	a.openModal(c)
	a.ui.Screen = c // fullscreen: the center takes the whole screen
	go c.watch(a.ui, func() bool { return a.modal == c }, a.quit)
}

// listCenter is the protocol boundary; tests can replace it with wire fixtures.
var listCenter = func(client *server.Client) ([]server.ThreadSummary, error) {
	var out struct {
		Threads []server.ThreadSummary `json:"threads"`
	}
	err := client.Call(context.Background(), "thread/list", map[string]any{"includeAgents": true, "includeClosedAgents": true, "includeArchived": true}, &out)
	return out.Threads, err
}

func (c *agentCenter) connect() {
	if c.client != nil {
		return
	}
	if c.a != nil && c.a.conn != nil && c.a.conn.own != nil {
		c.client = c.a.conn.c
		return
	}
	c.client, c.release = centerClient()
}

// watch refreshes once on opening; refreshes afterwards are explicit (r).
func (c *agentCenter) watch(ui *tui.TUI, active func() bool, quit <-chan struct{}) {
	c.connect()
	rows, err := listCenter(c.client)
	ui.Do(func() {
		if active() {
			c.accept(rows, err)
			c.selectCurrent()
		}
	})
}
func (c *agentCenter) accept(rows []server.ThreadSummary, err error) {
	if err != nil {
		c.notice = err.Error()
	} else {
		c.apply(rows)
	}
	c.loaded = true
}
func (c *agentCenter) reload() { c.connect(); rows, err := listCenter(c.client); c.accept(rows, err) }
func (c *agentCenter) refresh() {
	c.loaded = false
	if c.a == nil {
		c.reload()
		return
	}
	go func() {
		rows, err := listCenter(c.client)
		c.a.ui.Do(func() {
			if c.a.modal == c {
				c.accept(rows, err)
			}
		})
	}()
}

func (c *agentCenter) apply(rows []server.ThreadSummary) {
	var keep string
	if sh := c.shown(); c.sel < len(sh) {
		keep = sh[c.sel].id
	}
	items := make([]centerItem, 0, len(rows))
	c.msgs = map[string]string{}
	for _, row := range rows {
		it := centerItem{id: row.ID, title: row.Name, cwd: row.Cwd, branch: row.Branch, prompt: row.Preview, model: row.Model, updated: row.Updated, live: row.Loaded, archived: row.Archived, external: row.External, tab: tabInactive, current: c.a != nil && c.a.threadID == row.ID}
		if it.title == "" {
			it.title = row.Preview
		}
		if row.External {
			it.title = "agents started from a shell"
		}
		if row.Loaded {
			it.tab = tabReady
		}
		if row.Busy {
			it.tab = tabWorking
		}
		if row.OpenPrompt || row.GoalWaiting {
			it.tab = tabNeedsYou
		}
		if m := row.Agent; m != nil {
			it.isAgent = true
			it.closed = m.Lifecycle == agentstate.Closed
			it.parent, it.title, it.role, it.agentPath = m.ParentThreadID, m.Name, m.Role, m.Path
			if it.title == "" {
				it.title = row.Name
			}
			it.spawnedBy, it.projectRoot, it.turn = m.SpawnedBy, m.Project, &m.LastTurn
			it.shell = m.ParentThreadID == "" && m.Origin == session.OriginExternal
			if !row.Loaded && !row.Archived && m.Lifecycle != agentstate.Closed {
				switch m.LastTurn.Status {
				case agentstate.Running, agentstate.Queued:
					it.tab = tabWorking
				case agentstate.Idle, agentstate.Done:
					it.tab = tabReady
				}
			}
			if m.Lifecycle == agentstate.Closed {
				it.tab = tabInactive
			}
		}
		c.msgs[row.ID] = row.LastMessage
		items = append(items, it)
	}
	if c.archiveView {
		items = slices.DeleteFunc(items, func(it centerItem) bool { return !it.archived })
	}
	if c.scope != "" {
		items = slices.DeleteFunc(items, func(it centerItem) bool {
			return !session.SameDir(it.cwd, c.scope) || it.archived && !c.archiveView || it.parent != "" || it.isAgent
		})
	}
	c.items = items
	c.reorder()
	// Fold only completed agent subtrees, never a subtree containing an open agent.
	if c.collapsed == nil {
		c.collapsed = map[string]bool{}
	}
	tree := centerTree(items)
	for i, it := range tree {
		if !it.children {
			continue
		}
		if _, set := c.collapsed[it.id]; set {
			continue
		}
		hasAgent, finished := it.isAgent, !it.isAgent || it.closed
		for j := i + 1; j < len(tree) && tree[j].depth > it.depth; j++ {
			kid := tree[j]
			if kid.isAgent {
				hasAgent = true
				if !kid.closed || kid.tab == tabWorking {
					finished = false
				}
			}
		}
		if hasAgent && finished {
			c.collapsed[it.id] = true
		}
	}
	c.sel = 0
	for i, it := range c.shown() {
		if keep != "" && it.id == keep {
			c.sel = i
		}
	}
}

// reorder sorts the items: by project, or newest first when flat.
func (c *agentCenter) reorder() {
	if c.resume {
		slices.SortStableFunc(c.items, func(x, y centerItem) int {
			if x.live != y.live {
				if x.live {
					return -1
				}
				return 1
			}
			return y.updated.Compare(x.updated)
		})
		return
	}
	if c.flat {
		slices.SortStableFunc(c.items, func(x, y centerItem) int { return y.updated.Compare(x.updated) })
		return
	}
	c.items = groupByProject(c.items)
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

// shown filters the tree, retaining ancestors as context. Filters bypass
// folds so a working or matching agent cannot disappear under a parent.
func (c *agentCenter) shown() []centerItem {
	tree := centerTree(c.items)
	q := strings.ToLower(c.search)
	filtering := c.tab != tabAll || q != ""
	include := map[string]bool{}
	parents := map[string]string{}
	for _, it := range tree {
		parents[it.id] = it.parent
	}
	for _, it := range tree {
		if it.virtual { // a heading follows its agents
			continue
		}
		if c.tab != tabAll && it.tab != c.tab {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(it.title+" "+it.cwd+" "+it.prompt+" "+it.agentPath+" "+it.role+" "+it.model), q) {
			continue
		}
		for id := it.id; id != "" && !include[id]; id = parents[id] {
			include[id] = true
		}
	}
	var out []centerItem
	folded := -1
	for _, it := range tree {
		if !include[it.id] {
			continue
		}
		if !filtering {
			if folded >= 0 && it.depth > folded {
				continue
			}
			folded = -1
			if c.collapsed[it.id] {
				folded = it.depth
			}
		}
		out = append(out, it)
	}
	return out
}

// centerTree builds a stable preorder forest from recorded parent IDs.
// Missing parents become roots; a visited set also makes corrupt cycles safe.
// Worktree children keep their own cwd, but group under the root's project.
func centerTree(items []centerItem) []centerItem {
	items = centerShellParents(items)
	ids := map[string]bool{}
	children := map[string][]centerItem{}
	for _, it := range items {
		ids[it.id] = true
	}
	var roots []centerItem
	for _, it := range items {
		if it.parent != "" && ids[it.parent] && it.parent != it.id {
			children[it.parent] = append(children[it.parent], it)
		} else {
			roots = append(roots, it)
		}
	}
	seen := map[string]bool{}
	var out []centerItem
	var walk func(centerItem, int, string, string, string)
	walk = func(it centerItem, depth int, prefix, project, path string) {
		if seen[it.id] {
			return
		}
		seen[it.id] = true
		it.depth, it.prefix, it.project, it.children = depth, prefix, project, len(children[it.id]) > 0
		if it.parent != "" {
			name := it.title
			if name == "" {
				name = it.id
			}
			it.agentPath = path + "/" + agent.FirstLine(name)
		}
		out = append(out, it)
		childPath := path
		if it.agentPath != "" {
			childPath = it.agentPath
		}
		kids := children[it.id]
		for i, kid := range kids {
			edge := "├─ "
			if i == len(kids)-1 {
				edge = "└─ "
			}
			base := strings.TrimSuffix(strings.TrimSuffix(prefix, "├─ "), "└─ ")
			if depth > 0 {
				if strings.HasSuffix(prefix, "└─ ") {
					base += "   "
				} else {
					base += "│  "
				}
			}
			walk(kid, depth+1, base+edge, project, childPath)
		}
	}
	for _, it := range roots {
		walk(it, 0, "", it.cwd, agentstate.RootPath)
	}
	for _, it := range items {
		if !seen[it.id] {
			walk(it, 0, "", it.cwd, agentstate.RootPath)
		}
	}
	return out
}

func (c *agentCenter) selectCurrent() {
	if c.resume {
		var latest time.Time
		for i, it := range c.shown() {
			if it.updated.After(latest) {
				latest = it.updated
				c.sel = i
			}
		}
		return
	}
	for i, it := range c.shown() {
		if it.current {
			c.sel = i
		}
	}
}

func (c *agentCenter) close() {
	if c.release != nil {
		c.release()
		c.release = nil
	}
	c.onClose()
}

func (c *agentCenter) HandleInput(data string) {
	key := tui.Key(data)
	if key == "ctrl+c" {
		c.close()
		return
	}
	if c.confirm != "" {
		if data == "y" || key == "enter" {
			c.confirm = ""
			c.mutate(c.pending, c.pendingMethod, true)
		} else if key == "escape" || data == "n" {
			c.confirm = ""
		}
		return
	}
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
	case "g":
		c.flat, c.sel, c.top = !c.flat, 0, 0
		c.reorder()
	case " ":
		if c.sel < len(sh) && sh[c.sel].children {
			if c.collapsed == nil {
				c.collapsed = map[string]bool{}
			}
			id := sh[c.sel].id
			c.collapsed[id] = !c.collapsed[id]
			c.top = 0
		}
	case "v":
		c.archiveView = !c.archiveView
		c.sel, c.top = 0, 0
		c.refresh()
	case "r", "ctrl+r":
		c.refresh()
	case "a", "d":
		if c.sel >= len(sh) || sh[c.sel].virtual {
			return
		}
		it := sh[c.sel]
		method := "thread/archive"
		action := "archive"
		if key == "d" {
			method, action = "thread/delete", "delete"
		} else if it.archived {
			method, action = "thread/unarchive", "unarchive"
		}
		if it.isAgent && it.turn != nil && it.turn.Status.Active() {
			c.notice = "Agent has a running turn: interrupt it before archiving/deleting."
			return
		}
		if key == "d" || it.live {
			c.pending, c.pendingMethod = it, method
			c.confirm = "Confirm " + action + "? y / Enter confirms · Esc cancels"
			if it.live {
				c.confirm = "Stop it and " + action + "? y / Enter confirms · Esc cancels"
			}
		} else {
			c.mutate(it, method, false)
		}
	case "n":
		c.picking = true
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
		if it.virtual { // a heading is no session
			return
		}
		c.picking = true
		c.close()
		switch {
		case it.current && it.live:
		default:
			c.onOpen(it.id, it.cwd)
		}
	}
}

// Render is the center as a modal (the inline renderer); fullscreen, it
// takes the whole screen (RenderScreen, as TUI.Screen).
func (c *agentCenter) Render(width int) []string { return c.RenderScreen(width, centerRows+8) }

// RenderScreen draws the center at the terminal's size: the title and
// tabs, the list (with the selected session's details on the right when
// there is room) and the keys at the bottom.
func (c *agentCenter) RenderScreen(width, height int) []string {
	sh := c.shown()
	counts := make([]int, len(tabNames))
	for _, it := range c.items {
		counts[tabAll]++
		counts[it.tab]++
	}
	group := "Project"
	if c.flat {
		group = "None"
	}
	head := tui.Bold("Agent command center") + tui.Dim("  Group: "+group+"  g")
	if c.archiveView {
		head += tui.Dim(" · Archived")
	}
	if c.typing || c.search != "" {
		head = tui.Bold("Search: ") + c.search
		if c.typing {
			head += "▏"
		}
	}
	var tabs strings.Builder
	for i, n := range tabNames {
		t := fmt.Sprintf(" %s %d ", n, counts[i])
		if i == c.tab {
			t = "\x1b[7m" + t + "\x1b[27m"
		} else {
			t = tui.Dim(t)
		}
		tabs.WriteString(t + " ")
	}
	tabLine := tabs.String()
	if tui.VisibleWidth(tabLine) > width {
		tabLine = tui.Truncate(tabLine, width, "›")
	}
	out := []string{tui.Truncate(head, width, "…"), tabLine, tui.Dim(strings.Repeat("─", max(width, 0)))}

	keys := "esc back  ↑/↓ move  enter open  space fold  n new  / search  tab filter"
	if c.a == nil {
		keys = "esc quit  ↑/↓ move  enter open  space fold  n new  / search  tab filter"
	}
	if width < 60 {
		keys = "esc ←  ↑↓  enter →  space fold  n new  / find  tab"
	}
	keys += "  r refresh  a archive  d delete  v archived"
	message := c.notice
	if c.confirm != "" {
		message = c.confirm
	}
	footer := []string{tui.Truncate(message, width, "…"), tui.Truncate(tui.Dim(keys), width, "…")}
	bodyH := max(height-len(out)-len(footer), 3)

	detailW := 0
	if width >= 100 {
		detailW = min(width*2/5, 60)
	}
	listW := width
	if detailW > 0 {
		listW = width - detailW - 3
	}
	list := c.renderList(sh, listW, bodyH)
	var detail []string
	if detailW > 0 && c.sel < len(sh) {
		detail = c.renderDetail(sh[c.sel], detailW)
	}
	for i := range bodyH {
		l := ""
		if i < len(list) {
			l = list[i]
		}
		if detailW > 0 {
			d := ""
			if i < len(detail) {
				d = detail[i]
			}
			l += strings.Repeat(" ", max(0, listW-tui.VisibleWidth(l))) + tui.Dim(" │ ") + d
		}
		out = append(out, l)
	}
	return append(out, footer...)
}

// renderList is the list, bodyH rows at most: project headers (unless
// flat), one row per session, scrolled to keep the selection in view.
func (c *agentCenter) renderList(sh []centerItem, width, bodyH int) []string {
	if len(sh) == 0 {
		msg := "No sessions here."
		if c.search != "" {
			msg = "No session matches."
		}
		return []string{tui.Dim(" " + msg)}
	}
	wide := width >= 60
	statusW, ageW := 10, 8
	titleW := width - 4
	if wide {
		titleW = width - statusW - ageW - 7
	}
	var lines []string
	selLine := 0
	group := "\x00"
	for i, it := range sh {
		if !c.flat && it.project != group {
			group = it.project
			n := 0
			for _, x := range sh {
				if x.project == group {
					n++
				}
			}
			if len(lines) > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, tui.Truncate(tui.Dim(fmt.Sprintf(" %s  %d", core.ShortPath(group), n)), width, "…"))
		}
		title := agent.FirstLine(it.title)
		if title == "" {
			title = "(new session)"
		}
		if it.agentPath != "" {
			title = strings.TrimPrefix(it.agentPath, "/root/")
			if it.role != "" {
				title += " · " + it.role
			}
			if it.model != "" {
				title += " · " + it.model
			}
			if it.branch != "" {
				title += " · " + it.branch
			}
		}
		if it.live {
			title += " (live)"
		}
		if it.current {
			title += " (here)"
		}
		fold := ""
		if it.children {
			fold = "▾ "
			if c.collapsed[it.id] && c.tab == tabAll && c.search == "" {
				fold = "▸ "
			}
		}
		title = it.prefix + fold + title
		mark := "○"
		switch it.tab {
		case tabWorking:
			mark = "●"
		case tabNeedsYou:
			mark = "◆"
		case tabReady:
			mark = "●"
		}
		name := tui.Truncate(title, max(titleW, 8), "…")
		row := "  " + mark + " " + name
		if wide {
			row += strings.Repeat(" ", max(1, titleW-tui.VisibleWidth(name)+2))
			age := ""
			if !it.updated.IsZero() {
				age = session.RelTime(it.updated)
			}
			row += fmt.Sprintf("%-*s %*s", statusW, it.status(), ageW, age)
		}
		if i == c.sel {
			selLine = len(lines)
			row = "\x1b[7m" + row + strings.Repeat(" ", max(0, width-tui.VisibleWidth(row))) + "\x1b[27m"
		} else {
			switch it.tab {
			case tabWorking:
				row = strings.Replace(row, mark, tui.FG(2, mark), 1)
			case tabNeedsYou:
				row = strings.Replace(row, mark, tui.FG(3, mark), 1)
			case tabReady:
				row = strings.Replace(row, mark, tui.FG(6, mark), 1)
			}
		}
		lines = append(lines, tui.Truncate(row, width, "…"))
	}
	// Scroll so the selection shows, with its project's header when it fits.
	if selLine < c.top+1 {
		c.top = max(0, selLine-1)
	}
	if selLine >= c.top+bodyH-1 {
		c.top = selLine - bodyH + 2
	}
	c.top = max(0, min(c.top, len(lines)-bodyH))
	end := min(len(lines), c.top+bodyH)
	out := slices.Clone(lines[c.top:end])
	if end < len(lines) && len(out) > 0 {
		out[len(out)-1] = tui.Dim(fmt.Sprintf("   ↓ %d more", len(lines)-end))
	}
	if c.top > 0 && len(out) > 0 {
		out[0] = tui.Dim("   ↑ more")
	}
	return out
}

// renderDetail is the right column: what the selected session is about
// and where.
func (c *agentCenter) renderDetail(it centerItem, width int) []string {
	wrap := func(s string) []string { return tui.Wrap(s, max(width, 10)) }
	title := agent.FirstLine(it.title)
	if title == "" {
		title = "(new session)"
	}
	out := []string{tui.Bold("Task details"), ""}
	if it.agentPath != "" {
		title = agent.FirstLine(it.prompt)
	}
	out = append(out, wrap(title)...)
	if it.agentPath != "" {
		out = append(out, wrap(it.agentPath)...)
		out = append(out, wrap(strings.Join(slices.DeleteFunc([]string{it.role, it.model}, func(s string) bool { return s == "" }), " · "))...)
		if it.spawnedBy != nil { // tracking, not proof
			text := "Started by: " + it.spawnedBy.Brief()
			if it.spawnedBy.ToolCallID != "" {
				text += " · call " + it.spawnedBy.ToolCallID
			}
			out = append(out, wrap(text)...)
		}
		if it.turn != nil {
			t := it.turn
			if t.PromptTokens+t.OutputTokens > 0 {
				out = append(out, wrap(fmt.Sprintf("Tokens: %d in · %d cached · %d out", t.PromptTokens, t.CachedTokens, t.OutputTokens))...)
			}
			if d := t.Duration(); d > 0 {
				out = append(out, wrap("Duration: "+tui.FormatDuration(d))...)
			}
		}
	}
	out = append(out, tui.Dim(it.status()), "")
	if msg := c.lastMessage(it.id); msg != "" {
		label := "Last message"
		if it.agentPath != "" {
			label = "Last answer / report"
		}
		out = append(out, tui.Dim(label))
		lines := wrap(msg)
		if len(lines) > 8 {
			lines = append(lines[:8], "…")
		}
		out = append(out, lines...)
		out = append(out, "")
	}
	out = append(out, tui.Dim("Project"))
	out = append(out, wrap(core.ShortPath(it.cwd))...)
	if it.branch != "" {
		out = append(out, "", tui.Dim("Branch"))
		out = append(out, wrap(it.branch)...)
	}
	if it.live {
		out = append(out, "", tui.Dim("Live worker"))
	}
	if it.prompt != "" {
		out = append(out, "", tui.Dim("Prompt"))
		lines := wrap(agent.FirstLine(it.prompt))
		if len(lines) > 3 {
			lines = append(lines[:3], "…")
		}
		out = append(out, lines...)
	}
	return out
}

// lastMessage is loaded with the summary snapshot, never during rendering.
func (c *agentCenter) lastMessage(id string) string { return c.msgs[id] }

// AgentsPick is what the center by itself was left with.
type AgentsPick struct {
	Session string // or open this saved session
	New     bool   // or start a new session
	Cwd     string // in this directory
}

// RunAgents shows the agent center on this terminal by itself and returns
// what was picked (the zero value for nothing).
func RunAgents() (AgentsPick, error) {
	terminal := tui.NewProcessTerminal()
	ui := tui.New(terminal)
	done := make(chan struct{})
	var once sync.Once
	quit := func() { once.Do(func() { close(done) }) }
	var pick AgentsPick
	c := &agentCenter{onClose: quit,
		onOpen: func(id, cwd string) { pick = AgentsPick{Session: id, Cwd: cwd} },
		onNew:  func(cwd string) { pick = AgentsPick{New: true, Cwd: cwd} },
	}
	defer func() {
		if c.release != nil {
			c.release()
			c.release = nil
		}
	}()
	c.reload()
	ui.Screen = c
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
	stopSignals := watchTerminalExit(func() { terminal.InterruptOutput(); quit() })
	defer stopSignals()
	// The standalone center already refreshed before starting the UI.
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
		}
	}
}

func (c *agentCenter) mutate(it centerItem, method string, stop bool) {
	c.connect()
	run := func() error {
		return c.client.Call(context.Background(), method, map[string]any{"threadId": it.id, "stop": stop}, nil)
	}
	if c.a == nil {
		if err := run(); err != nil {
			c.notice = err.Error()
		} else {
			c.notice = ""
			c.reload()
		}
		return
	}
	go func() {
		err := run()
		c.a.ui.Do(func() {
			if c.a.modal != c {
				return
			}
			if err != nil {
				c.notice = err.Error()
			} else {
				c.notice = ""
				c.refresh()
			}
		})
	}()
}

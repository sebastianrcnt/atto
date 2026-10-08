package app

import (
	"fmt"
	"github.com/sebastianrcnt/atto/core"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/agentstate"
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
// Agents are equal sessions, shown as a tree under the session (or shell
// parent) that started them. Space folds/unfolds a tree; filtering reveals
// matching agents with their ancestors. Opening a locked agent shows its
// transcript read-only, with the usual ctrl+r refresh. Ctrl+C closes the
// center only, never interrupting the underlying session's work.

// listPanes lists the daemon's panes; tests replace it.
var listPanes = daemon.List
var listWorkers = daemon.Workers

// listSaved lists saved sessions, newest first; tests replace it.
var listSaved = func() []session.Summary {
	active, _ := session.ListAll("", false)
	archived, _ := session.ListAll("", true)
	// Closing agents archives their transcripts. Keep those visible as
	// Inactive, along with any archived ancestors needed to connect them.
	needed := map[string]bool{}
	for _, s := range active {
		if s.AgentOf != "" {
			needed[s.AgentOf] = true
		}
	}
	for _, s := range archived {
		if s.AgentOf != "" {
			needed[s.ID], needed[s.AgentOf] = true, true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, s := range archived {
			if needed[s.ID] && s.AgentOf != "" && !needed[s.AgentOf] {
				needed[s.AgentOf], changed = true, true
			}
		}
	}
	for _, s := range archived {
		if needed[s.ID] {
			active = append(active, s)
		}
	}
	slices.SortStableFunc(active, func(x, y session.Summary) int { return y.Updated.Compare(x.Updated) })
	return active
}

// centerRefresh is how often the open center reloads.
const centerRefresh = 2 * time.Second

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
	pane                           *daemon.Pane // nil: saved, not running
	current                        bool         // this atto's own session
	tab                            int          // tabNeedsYou .. tabInactive
	updated                        time.Time
	parent, agentPath, role, model string
	external, archived             bool
	turn                           *agentstate.Turn
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
	a        *App // nil: on its own (atto agents)
	onClose  func()
	onSwitch func(pane int)
	// onOpen opens a saved session (cwd is its directory); onNew starts a
	// session in cwd.
	onOpen func(id, cwd string)
	onNew  func(cwd string)

	items     []centerItem
	tab       int
	sel       int // index into shown()
	top       int // first shown row
	search    string
	typing    bool // the search box has focus
	flat      bool // not grouped by project (g)
	msgs      map[string]string
	loaded    bool
	collapsed map[string]bool // expanded by default; retained across refreshes
}

func (a *App) cmdAgents(string) { a.openAgents(tabAll) }

func (a *App) openAgents(tab int) {
	c := &agentCenter{a: a, tab: tab, onClose: a.closeModal}
	c.onSwitch = func(id int) {
		if a.pane.on {
			a.ui.Emit(daemon.MarkerSeq("switch", strconv.Itoa(id)))
			return
		}
		if a.workers() {
			if panes, err := listPanes(); err == nil {
				for _, p := range panes {
					if p.ID == id && p.Session != "" {
						a.closeModal()
						a.resumeID(p.Session)
						return
					}
				}
			}
		}
		a.notice("This atto runs outside the daemon: show that session from a shell with atto attach %d.", id)
	}
	c.onOpen = func(id, cwd string) {
		if a.pane.on {
			a.ui.Emit(daemon.MarkerSeq("open", id, cwd))
			return
		}
		if path, err := session.Find(id); err == nil {
			a.requestResume(path)
		}
	}
	c.onNew = func(cwd string) {
		if a.pane.on {
			a.ui.Emit(daemon.MarkerSeq("new", cwd))
			return
		}
		a.cmdClear("")
	}
	c.apply(centerSnapshot{})
	c.selectCurrent()
	a.openModal(c)
	a.ui.Screen = c // fullscreen: the center takes the whole screen
	go c.watch(a.ui, func() bool { return a.modal == c }, a.quit)
}

type centerSnapshot struct {
	panes   []daemon.Pane
	workers []daemon.Worker
	saved   []session.Summary
	agents  []centerAgent
}

type centerAgent struct {
	state agentstate.State
	turn  agentstate.Turn
}

func scanCenter() centerSnapshot {
	panes, _ := listPanes()
	workers, _ := listWorkers()
	snapshot := centerSnapshot{panes: panes, workers: workers, saved: listSaved()}
	for _, s := range agentstate.ListAll() {
		snapshot.agents = append(snapshot.agents, centerAgent{s, s.Latest()})
	}
	// A parent may have no user message yet (for example a shell using
	// -session explicitly). Default-style listings omit those sessions,
	// but an existing recorded parent should still anchor its agents.
	seen := map[string]bool{}
	var parents []string
	for _, s := range snapshot.saved {
		seen[s.ID] = true
		parents = append(parents, s.AgentOf)
	}
	for _, agent := range snapshot.agents {
		parents = append(parents, agent.state.Parent)
	}
	for len(parents) > 0 {
		id := parents[0]
		parents = parents[1:]
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if path, err := session.Find(id); err == nil {
			if s, err := session.Summarize(path); err == nil {
				snapshot.saved = append(snapshot.saved, s)
				parents = append(parents, s.AgentOf)
			}
		}
	}
	return snapshot
}

// watch scans off the UI goroutine and swaps completed snapshots in.
func (c *agentCenter) watch(ui *tui.TUI, active func() bool, quit <-chan struct{}) {
	t := time.NewTicker(centerRefresh)
	defer t.Stop()
	for {
		alive := false
		ui.Do(func() { alive = active() })
		if !alive {
			return
		}
		snapshot := scanCenter()
		ui.Do(func() {
			if active() {
				first := !c.loaded
				c.apply(snapshot)
				c.loaded = true
				if first {
					c.selectCurrent()
				}
			}
		})
		select {
		case <-quit:
			return
		case <-t.C:
		}
	}
}

// reload gathers the sessions: the daemon's panes, this one (when atto
// runs directly), then the saved ones, newest first.
func (c *agentCenter) reload() { c.apply(scanCenter()); c.loaded = true }

func (c *agentCenter) apply(snapshot centerSnapshot) {
	var keep string
	if sh := c.shown(); c.sel < len(sh) {
		keep = sh[c.sel].id
	}
	panes := snapshot.panes
	self, _ := strconv.Atoi(os.Getenv(daemon.EnvPane))
	var items []centerItem
	seen := map[string]bool{}
	for i := range panes {
		p := panes[i]
		it := centerItem{id: p.Session, title: p.Name, cwd: p.Cwd, pane: &p, updated: p.Active,
			current: c.a != nil && (p.ID == self || (p.Session != "" && p.Session == c.a.threadID))}
		switch p.State {
		case "working":
			it.tab = tabWorking
		case "waiting":
			it.tab = tabNeedsYou
		default:
			it.tab = tabReady
		}
		if it.id == "" {
			it.id = fmt.Sprintf("pane:%d", p.ID)
		}
		if seen[it.id] {
			// Multiple panes may view one agent transcript. Prefer this
			// terminal's pane, otherwise a working/waiting pane over an idle one.
			j := slices.IndexFunc(items, func(x centerItem) bool { return x.id == it.id })
			if it.current || (!items[j].current && items[j].tab == tabReady && it.tab != tabReady) {
				items[j] = it
			}
			continue
		}
		items = append(items, it)
		seen[it.id] = true
	}
	for _, w := range snapshot.workers {
		if seen[w.Session] {
			for i := range items {
				if items[i].id == w.Session {
					if w.Busy {
						items[i].tab = tabWorking
					} else if items[i].tab == tabWorking {
						items[i].tab = tabReady
					}
				}
			}
			continue
		}
		state := tabReady
		if w.Busy {
			state = tabWorking
		}
		items = append(items, centerItem{id: w.Session, cwd: w.Cwd, updated: w.Started, tab: state, current: c.a != nil && c.a.threadID == w.Session})
		seen[w.Session] = true
	}
	if a := c.a; a != nil && !seen[a.threadID] {
		it := centerItem{id: a.threadID, title: a.sessName, cwd: a.cwd, current: true, updated: time.Now(), tab: tabReady}
		switch {
		case a.busy:
			it.tab = tabWorking
		case a.goalHeld() && a.goalActive():
			it.tab = tabNeedsYou
		}
		items = append(items, it)
		seen[a.threadID] = true
	}
	c.msgs = make(map[string]string)
	for _, s := range snapshot.saved {
		c.msgs[s.ID] = s.LastMessage
		title := s.Name
		if s.External {
			title = "agents started from a shell"
		}
		if title == "" {
			title = s.Preview
		}
		if seen[s.ID] {
			for j := range items { // fill in what the pane doesn't know
				if items[j].id == s.ID {
					if items[j].title == "" {
						items[j].title = title
					}
					items[j].branch, items[j].prompt, items[j].model = s.Branch, s.Preview, s.Model
					items[j].parent, items[j].external, items[j].archived = s.AgentOf, s.External, s.Archived
				}
			}
			continue
		}
		items = append(items, centerItem{id: s.ID, title: title, cwd: s.Cwd, branch: s.Branch, prompt: s.Preview, model: s.Model, updated: s.Updated, tab: tabInactive, parent: s.AgentOf, external: s.External, archived: s.Archived})
	}
	// Enrich from the same state and Latest turn used by atto agent list.
	// State can precede a session's first write, or outlive its parent.
	for _, agent := range snapshot.agents {
		st, turn := agent.state, agent.turn
		if st.Session == "" {
			continue
		}
		j := slices.IndexFunc(items, func(it centerItem) bool { return it.id == st.Session })
		if j < 0 {
			items = append(items, centerItem{id: st.Session, cwd: st.Cwd, updated: st.Created})
			j = len(items) - 1
		}
		it := &items[j]
		it.parent, it.title, it.prompt = st.Parent, st.Name, st.Task
		it.role, it.model, it.turn = st.Preset, st.Model, &turn
		if st.Branch != "" {
			it.branch = st.Branch
		}
		// A live pane's waiting/working state is more precise than turn state.
		viewer := it.current && c.a != nil && c.a.readOnly != ""
		if it.pane != nil && it.pane.State != "waiting" && turn.Status.Active() {
			// A pane may be a read-only viewer of the headless turn. Its idle
			// frontend does not make the actual agent idle.
			it.tab = tabWorking
		}
		if it.pane == nil && (!it.current || viewer) && !it.archived {
			switch turn.Status {
			case agentstate.Running, agentstate.Queued:
				it.tab = tabWorking
			case agentstate.Idle, agentstate.Done:
				it.tab = tabReady
			default:
				it.tab = tabInactive
			}
		}
	}
	c.items = items
	c.reorder()
	c.sel = 0
	for i, it := range c.shown() {
		if keep != "" && it.id == keep {
			c.sel = i
		}
	}
}

// reorder sorts the items: by project, or newest first when flat.
func (c *agentCenter) reorder() {
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
	for i, it := range c.shown() {
		if it.current {
			c.sel = i
		}
	}
}

func (c *agentCenter) close() { c.onClose() }

func (c *agentCenter) HandleInput(data string) {
	key := tui.Key(data)
	// Ctrl+C belongs to the center while it has focus, even in search.
	// Do not pass it through to the session's turn or shell cancellation.
	if key == "ctrl+c" {
		c.close()
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
	footer := []string{"", tui.Truncate(tui.Dim(keys), width, "…")}
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
			title = it.agentPath
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
	if it.pane != nil {
		shown := "detached"
		if n := it.pane.Clients; n > 0 {
			shown = fmt.Sprintf("shown on %d terminal(s)", n)
		}
		out = append(out, "", tui.Dim("Pane"))
		out = append(out, wrap(fmt.Sprintf("#%d · %s", it.pane.ID, shown))...)
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
	go c.watch(ui, func() bool { return true }, done)
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

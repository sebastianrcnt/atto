package app

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sebastianrcnt/atto/daemon"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/subagent"
	"github.com/sebastianrcnt/atto/tui"
)

// The agent center (← on an empty prompt, or /agents), after codex's
// agents overview: every atto the daemon runs, with its session, goal and
// subagents. Enter on another session shows it on this terminal (the
// daemon moves the terminal to that pane); enter on a subagent shows its
// latest report, read-only. ← or Esc goes back. "atto agents" shows the
// same list on its own (RunAgents), from any shell: Enter attaches.

// listPanes lists the daemon's panes; tests replace it.
var listPanes = daemon.List

// centerRefresh is how often the open center reloads.
const centerRefresh = 2 * time.Second

// centerRow is a line of the center: a session (pane) or a subagent.
type centerRow struct {
	pane    daemon.Pane
	current bool   // this atto's own session
	title   string // name, else first message
	goal    string // goal status, "" for none
	sub     *subagent.State
}

type agentCenter struct {
	a        *App // nil: on its own (atto agents)
	onClose  func()
	onSwitch func(pane int)
	rows     []centerRow
	sel      int
	direct   bool     // this atto isn't in the daemon
	view     []string // a subagent's report being read, nil: the list
	scroll   int
}

func (a *App) cmdAgents(string) { a.openAgents() }

func (a *App) openAgents() {
	c := &agentCenter{a: a, direct: !a.pane.on, onClose: a.closeModal}
	c.onSwitch = func(id int) { a.ui.Emit(daemon.MarkerSeq("switch", strconv.Itoa(id))) }
	c.reload()
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
				if c.view == nil {
					c.reload()
				}
				gone <- false
			})
			if <-gone {
				return
			}
		}
	}()
}

func (c *agentCenter) close() { c.onClose() }

// reload gathers the rows: the daemon's panes (just this session when
// atto runs directly), each followed by its subagents.
func (c *agentCenter) reload() {
	a := c.a
	var panes []daemon.Pane
	if !c.direct {
		panes, _ = listPanes()
	}
	self, _ := strconv.Atoi(os.Getenv(daemon.EnvPane))
	if len(panes) == 0 && a != nil {
		panes = []daemon.Pane{{ID: self, Cwd: a.cwd, Session: a.sess.ID, Name: a.sessName, Clients: 1}}
	}
	var rows []centerRow
	for _, p := range panes {
		r := centerRow{pane: p, current: a != nil && (p.ID == self || p.Session == a.sess.ID)}
		r.title, r.goal = p.Name, ""
		if r.current {
			r.title = a.sessName
			r.goal = tui.StripEscapes(a.goalIndicator())
		} else if p.Session != "" {
			if g, err := goal.Load(p.Session); err == nil && g != nil {
				r.goal = "Goal " + g.Status.Label()
			}
		}
		if r.title == "" && p.Session != "" {
			if path, err := session.Find(p.Session); err == nil {
				if s, err := session.Summarize(path); err == nil {
					r.title = s.Name
					if r.title == "" {
						r.title = s.Preview
					}
				}
			}
		}
		rows = append(rows, r)
		if p.Session == "" {
			continue
		}
		for _, s := range subagent.List(p.Session) {
			r := centerRow{pane: p, sub: &s}
			rows = append(rows, r)
		}
	}
	c.rows = rows
	c.sel = min(c.sel, max(len(rows)-1, 0))
}

func (c *agentCenter) HandleInput(data string) {
	if c.view != nil {
		switch tui.Key(data) {
		case "escape", "left", "q":
			c.view, c.scroll = nil, 0
		case "up", "k":
			c.scroll = max(c.scroll-1, 0)
		case "down", "j":
			c.scroll++
		case "pageup":
			c.scroll = max(c.scroll-10, 0)
		case "pagedown", "space":
			c.scroll += 10
		}
		return
	}
	switch tui.Key(data) {
	case "escape", "left", "q":
		c.close()
	case "up", "k":
		c.sel = max(c.sel-1, 0)
	case "down", "j":
		c.sel = min(c.sel+1, len(c.rows)-1)
	case "enter", "right":
		if c.sel >= len(c.rows) {
			return
		}
		r := c.rows[c.sel]
		switch {
		case r.sub != nil:
			c.view, c.scroll = c.report(*r.sub), 0
		case r.current:
			c.close()
		default:
			c.onSwitch(r.pane.ID)
			c.close()
		}
	}
}

// report is what the center shows for a subagent: its status line and
// latest answer.
func (c *agentCenter) report(s subagent.State) []string {
	t := s.Latest()
	head := fmt.Sprintf("%s · %s · %s · turn %d %s", s.Name, s.Preset, s.Model, t.N, t.Status)
	if d := t.Duration(); d > 0 {
		head += " · " + tui.FormatDuration(d)
	}
	lines := []string{head}
	if s.Branch != "" {
		lines = append(lines, "worktree "+shortPath(s.Worktree)+" · branch "+s.Branch)
	}
	if t.Error != "" {
		lines = append(lines, "error: "+t.Error)
	}
	lines = append(lines, "", "task: "+firstLine(s.Task), "")
	if msg := session.LastAssistant(s.Session); msg != "" {
		lines = append(lines, strings.Split(msg, "\n")...)
	} else {
		lines = append(lines, "(no answer yet)")
	}
	return lines
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

func (c *agentCenter) Render(width int) []string {
	if c.view != nil {
		return c.renderView(width)
	}
	out := []string{tui.Bold(" Agents")}
	if c.a == nil && len(c.rows) == 0 {
		out = append(out, tui.Dim(" no atto is running in the daemon now"))
	}
	if c.direct {
		out = append(out, tui.Dim(" atto runs directly here, not in the daemon: only this session is shown"))
	}
	out = append(out, "")
	for i, r := range c.rows {
		var line string
		if r.sub != nil {
			t := r.sub.Latest()
			line = fmt.Sprintf("    └ %s  %s", r.sub.Name, tui.Dim(fmt.Sprintf("%s · %s · %s", r.sub.Preset, r.sub.Model, t.Status)))
			if d := t.Duration(); d > 0 {
				line += tui.Dim(" " + tui.FormatDuration(d))
			}
		} else {
			title := r.title
			if title == "" {
				title = "(new session)"
			}
			shown := "detached"
			switch {
			case r.current:
				shown = "this terminal"
			case r.pane.Clients == 1:
				shown = "1 terminal"
			case r.pane.Clients > 1:
				shown = fmt.Sprintf("%d terminals", r.pane.Clients)
			}
			id := ""
			if r.pane.ID > 0 {
				id = fmt.Sprintf("#%d ", r.pane.ID)
			}
			meta := []string{shortPath(r.pane.Cwd), shown}
			if r.goal != "" {
				meta = append(meta, r.goal)
			}
			line = " " + id + firstLine(title) + "  " + tui.Dim(strings.Join(meta, " · "))
		}
		if i == c.sel {
			line = tui.FG(6, "›") + line
		} else {
			line = " " + line
		}
		out = append(out, tui.Truncate(line, width, "…"))
	}
	hint := "↑↓ select · enter open · ← back"
	if c.a == nil {
		hint = "↑↓ select · enter attach · ← quit"
	}
	if c.direct {
		hint = "↑↓ select · enter open a subagent · ← back"
	}
	return append(out, "", tui.Truncate(tui.Dim(" "+hint), width, "…"))
}

// viewHeight is how many report lines show at once.
const viewHeight = 20

func (c *agentCenter) renderView(width int) []string {
	var body []string
	for i, l := range c.view {
		if i == 0 {
			body = append(body, tui.Bold(" "+l))
			continue
		}
		for _, w := range tui.Wrap(l, max(width-2, 10)) {
			body = append(body, " "+w)
		}
	}
	c.scroll = min(c.scroll, max(len(body)-viewHeight, 0))
	end := min(c.scroll+viewHeight, len(body))
	out := body[c.scroll:end]
	more := ""
	if end < len(body) {
		more = fmt.Sprintf(" · %d more lines ↓", len(body)-end)
	}
	return append(slices.Clone(out), "", tui.Truncate(tui.Dim(" ↑↓ scroll · ← back"+more), width, "…"))
}

// RunAgents shows the agent center on this terminal by itself and returns
// the pane picked to attach to, 0 for none.
func RunAgents() (int, error) {
	ui := tui.New(tui.NewProcessTerminal())
	done := make(chan struct{})
	var once sync.Once
	quit := func() { once.Do(func() { close(done) }) }
	picked := 0
	c := &agentCenter{onClose: quit, onSwitch: func(id int) { picked = id }}
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
		return 0, err
	}
	t := time.NewTicker(centerRefresh)
	defer t.Stop()
	for {
		select {
		case <-done:
			ui.Stop()
			return picked, nil
		case <-t.C:
			ui.Do(func() {
				if c.view == nil {
					c.reload()
				}
			})
		}
	}
}

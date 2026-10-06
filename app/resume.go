package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sebastianrcnt/atto/core"

	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

// resumePicker lists saved sessions the way Claude Code's resume screen
// does: a title with the position, a search box, then newest first, filtered
// to the current directory, two lines per session (title, then when, branch
// and size). Type to search; ctrl+a (or tab) toggles this directory / all
// projects, ctrl+b only the current git branch, shift+tab active / archived,
// ctrl+x archives (or unarchives) the selection, ctrl+r renames it and space
// previews it while the search is empty.
// Navigation and the search text are the shared SelectList's; this draws
// the rows itself, with scroll arrows in the marker column.
type resumePicker struct {
	cwd        string
	all        bool
	archived   bool
	branchOnly bool
	branch     string // the git branch of cwd; "" outside a repository
	list       *tui.SelectList
	current    string // path of the open session, marked in the list
	mode       resumeMode

	renaming  session.Summary // the session being renamed
	name      string          // the name typed so far
	renameErr error

	previewing session.Summary
	preview    []previewMsg
	previewOff int // lines scrolled up from the end

	onPick    func(session.Summary)
	onArchive func(session.Summary)
	onRename  func(s session.Summary, name string) error
	onCancel  func()
}

type resumeMode int

const (
	modeList resumeMode = iota
	modePreview
	modeRename
)

// previewMsg is one message shown in the preview.
type previewMsg struct{ role, text string }

const (
	resumeVisible = 5  // sessions shown at once
	previewRows   = 15 // lines of a preview
	previewMsgs   = 6  // messages in a preview
)

func newResumePicker(cwd, current string) *resumePicker {
	p := &resumePicker{cwd: cwd, current: current, branch: session.GitBranch(cwd)}
	p.list = &tui.SelectList{
		MaxVisible: resumeVisible,
		Filterable: true,
	}
	p.list.OnSelect = func(it tui.SelectItem) { p.onPick(it.Data.(session.Summary)) }
	p.list.OnCancel = func() { p.onCancel() }
	p.load()
	return p
}

// load refreshes the sessions from disk; the selection stays in range.
func (p *resumePicker) load() {
	dir := p.cwd
	if p.all {
		dir = ""
	}
	sums, _ := session.List(dir, p.archived)
	if p.branchOnly {
		kept := sums[:0]
		for _, s := range sums {
			if s.Branch == p.branch {
				kept = append(kept, s)
			}
		}
		sums = kept
	}
	p.list.Items = nil
	for _, s := range sums {
		// The label is what searching matches: name, first message and ID.
		p.list.Items = append(p.list.Items, tui.SelectItem{
			Label: strings.Join(strings.Fields(s.Name+" "+s.Preview), " "),
			Value: s.ID,
			Data:  s,
		})
	}
	switch {
	case len(sums) == 0 && p.archived:
		p.list.Empty = "No archived sessions"
	case len(sums) == 0 && p.branchOnly:
		p.list.Empty = fmt.Sprintf("No sessions on branch %s · Ctrl+B shows all branches", p.branch)
	case len(sums) == 0 && !p.all:
		// The default filter is this directory; say when other places have
		// sessions, rather than looking like there are none.
		if all, _ := session.List("", false); len(all) > 0 {
			p.list.Empty = fmt.Sprintf("No sessions in this directory · Ctrl+A shows all %d", len(all))
		} else {
			p.list.Empty = "No saved sessions"
		}
	case len(sums) == 0:
		p.list.Empty = "No saved sessions"
	default:
		p.list.Empty = "No results for your search"
	}
}

// reload applies a changed filter: back to the first session.
func (p *resumePicker) reload() {
	p.list.Selected = 0
	p.load()
}

func (p *resumePicker) HandleInput(data string) {
	switch p.mode {
	case modePreview:
		p.previewInput(data)
		return
	case modeRename:
		p.renameInput(data)
		return
	}
	switch tui.Key(data) {
	case "tab", "ctrl+a":
		p.all = !p.all
		p.reload()
	case "shift+tab":
		p.archived = !p.archived
		p.reload()
	case "ctrl+b":
		if p.branch != "" {
			p.branchOnly = !p.branchOnly
			p.reload()
		}
	case "ctrl+x":
		if it, ok := p.list.Current(); ok {
			p.onArchive(it.Data.(session.Summary))
			p.load()
		}
	case "ctrl+r":
		if it, ok := p.list.Current(); ok {
			p.renaming = it.Data.(session.Summary)
			p.name, p.renameErr = p.renaming.Name, nil
			p.mode = modeRename
		}
	default:
		if data == " " && p.list.Query() == "" {
			p.startPreview()
			return
		}
		p.list.HandleInput(data)
	}
}

// startPreview shows the last few exchanges of the selected session.
func (p *resumePicker) startPreview() {
	it, ok := p.list.Current()
	if !ok {
		return
	}
	p.previewing = it.Data.(session.Summary)
	p.preview, p.previewOff = nil, 0
	_, entries, err := session.Load(p.previewing.Path)
	if err != nil {
		p.preview = []previewMsg{{"error", err.Error()}}
	}
	for _, e := range session.Active(entries) {
		m := e.Message
		if e.Type != session.TypeMessage || m == nil || strings.TrimSpace(m.Content) == "" {
			continue
		}
		if m.Role == "user" || m.Role == "assistant" {
			p.preview = append(p.preview, previewMsg{m.Role, strings.TrimSpace(m.Content)})
		}
	}
	if n := len(p.preview); n > previewMsgs {
		p.preview = p.preview[n-previewMsgs:]
	}
	p.mode = modePreview
}

func (p *resumePicker) previewInput(data string) {
	switch tui.Key(data) {
	case "up", "ctrl+p":
		p.previewOff++
	case "down", "ctrl+n":
		p.previewOff = max(0, p.previewOff-1)
	case "pageup":
		p.previewOff += previewRows
	case "pagedown":
		p.previewOff = max(0, p.previewOff-previewRows)
	case "enter":
		p.onPick(p.previewing)
	case "escape", "ctrl+c":
		p.mode = modeList
	default:
		if data == " " {
			p.mode = modeList
		}
	}
}

func (p *resumePicker) renameInput(data string) {
	switch tui.Key(data) {
	case "enter":
		name := strings.TrimSpace(p.name)
		if name == "" {
			return
		}
		if p.renameErr = p.onRename(p.renaming, name); p.renameErr == nil {
			p.mode = modeList
			p.load()
		}
	case "escape", "ctrl+c":
		p.mode = modeList
	case "ctrl+u":
		p.name = ""
	case "backspace":
		if r := []rune(p.name); len(r) > 0 {
			p.name = string(r[:len(r)-1])
		}
	default:
		if tui.Printable(data) {
			p.name += strings.ReplaceAll(data, "\n", " ")
		}
	}
}

func (p *resumePicker) Render(width int) []string {
	switch p.mode {
	case modePreview:
		return p.renderPreview(width)
	case modeRename:
		return p.renderRename(width)
	}
	items := p.list.Visible()
	title := "Resume session"
	if p.archived {
		title = "Archived sessions"
	}
	head := tui.FG(6, title)
	if n := len(items); n > 0 {
		head += tui.Dim(fmt.Sprintf(" (%d of %d)", p.list.Selected+1, n))
	}
	out := []string{head}
	out = append(out, inputBox(width, p.list.Query(), "Search…")...)
	group := "All projects"
	if !p.all {
		group = filepath.Base(p.cwd)
	}
	if p.branchOnly {
		group += " · " + p.branch
	}
	out = append(out, tui.Truncate(tui.Dim(group), width, "…"))
	out = append(out, "")
	if len(items) == 0 {
		out = append(out, tui.Truncate(tui.Dim(p.list.Empty), width, "…"), "")
	}
	start, end := tui.Window(p.list.Selected, len(items), resumeVisible)
	for i := start; i < end; i++ {
		mark := ""
		switch {
		case i == end-1 && end < len(items):
			mark = "↓"
		case i == start && start > 0:
			mark = "↑"
		}
		out = append(out, p.row(items[i], i == p.list.Selected, mark, width)...)
		out = append(out, "")
	}
	return append(out, p.footer(width)...)
}

// footer is the dim key hints, wrapped between hints to fit the width.
func (p *resumePicker) footer(width int) []string {
	all := "Ctrl+A to show all projects"
	if p.all {
		all = "Ctrl+A to show only this directory"
	}
	hints := []string{all}
	if p.branch != "" {
		if p.branchOnly {
			hints = append(hints, "Ctrl+B to show all branches")
		} else {
			hints = append(hints, "Ctrl+B to only show current branch")
		}
	}
	archive, view := "Ctrl+X to archive", "Shift+Tab for archived"
	if p.archived {
		archive, view = "Ctrl+X to unarchive", "Shift+Tab for active"
	}
	hints = append(hints, "Space to preview", "Ctrl+R to rename", archive, view, "Type to search", "Esc to cancel")
	return wrapHints(hints, width)
}

// wrapHints joins hints with " · " and breaks lines between hints.
func wrapHints(hints []string, width int) []string {
	var out []string
	cur := ""
	for _, h := range hints {
		switch {
		case cur == "":
			cur = h
		case tui.VisibleWidth(cur)+3+tui.VisibleWidth(h) <= width:
			cur += " · " + h
		default:
			out = append(out, tui.Dim(cur))
			cur = h
		}
	}
	if cur != "" {
		out = append(out, tui.Dim(cur))
	}
	return out
}

// inputBox draws a one-line text field in a rounded box with a search icon,
// showing the tail of text when it is too long, or placeholder when empty.
func inputBox(width int, text, placeholder string) []string {
	inner := max(8, width-2) - 2 // inside the borders
	prefix := " ⌕ "
	room := inner - tui.VisibleWidth(prefix) - 1
	var body string
	if text == "" {
		body = tui.CursorMarker + tui.Dim(tui.Truncate(placeholder, room, "…"))
	} else {
		r := []rune(text)
		for len(r) > 1 && tui.VisibleWidth(string(r)) > room {
			r = r[1:]
		}
		body = string(r) + tui.CursorMarker
	}
	line := prefix + body
	pad := strings.Repeat(" ", max(0, inner-tui.VisibleWidth(line)))
	edge := tui.Dim
	return []string{
		edge("╭" + strings.Repeat("─", inner) + "╮"),
		edge("│") + line + pad + edge("│"),
		edge("╰" + strings.Repeat("─", inner) + "╯"),
	}
}

func (p *resumePicker) renderRename(width int) []string {
	out := []string{tui.FG(6, "Rename session")}
	out = append(out, inputBox(width, p.name, "Name")...)
	if p.renameErr != nil {
		out = append(out, tui.Truncate(tui.FG(1, "Error: "+p.renameErr.Error()), width, "…"))
	}
	return append(append(out, ""), wrapHints([]string{"Enter to save", "Esc to cancel"}, width)...)
}

func (p *resumePicker) renderPreview(width int) []string {
	out := []string{tui.FG(6, "Preview") + tui.Dim("  "+rowTitle(p.previewing))}
	out[0] = tui.Truncate(out[0], width, "…")
	var lines []string
	for _, m := range p.preview {
		role := tui.Bold("You")
		if m.role == "assistant" {
			role = tui.FG(6, "Assistant")
		}
		lines = append(lines, role)
		body := tui.Wrap(m.text, max(1, width-2))
		if len(body) > 12 {
			body = append(body[:11], tui.Dim("…"))
		}
		for _, l := range body {
			lines = append(lines, "  "+l)
		}
		lines = append(lines, "")
	}
	if len(lines) == 0 {
		lines = []string{tui.Dim("No messages")}
	}
	p.previewOff = min(p.previewOff, max(0, len(lines)-previewRows))
	end := len(lines) - p.previewOff
	start := max(0, end-previewRows)
	out = append(out, "")
	for _, l := range lines[start:end] {
		out = append(out, tui.Truncate(l, width, "…"))
	}
	return append(append(out, ""), wrapHints([]string{"↑↓ to scroll", "Enter to resume", "Space or Esc to go back"}, width)...)
}

// rowTitle is what names a session: its name, else its first message.
func rowTitle(s session.Summary) string {
	if s.Name != "" {
		return strings.Join(strings.Fields(s.Name), " ")
	}
	if t := strings.Join(strings.Fields(s.Preview), " "); t != "" {
		return t
	}
	return "(no message yet)"
}

// row draws a session as two lines: its title, then when, branch and size.
// mark is a scroll arrow for the marker column ("" for none).
func (p *resumePicker) row(it tui.SelectItem, selected bool, mark string, width int) []string {
	s := it.Data.(session.Summary)
	title := rowTitle(s)
	marker := "  "
	switch {
	case selected:
		marker, title = tui.FG(6, "❯ "), tui.FG(6, title)
	case mark != "":
		marker = tui.Dim(mark + " ")
	}
	meta := "  " + p.rowMeta(s)
	return []string{tui.Truncate(marker+title, width, "…"), tui.Truncate(tui.Dim(meta), width, "…")}
}

// rowMeta is a session's second line: when, branch and size.
func (p *resumePicker) rowMeta(s session.Summary) string {
	parts := []string{session.RelTime(s.Updated)}
	if s.Running > 0 {
		parts = append(parts, "running")
	}
	if s.Branch != "" {
		parts = append(parts, s.Branch)
	}
	parts = append(parts, formatSize(s.Size))
	if s.Path == p.current {
		parts = append(parts, "current")
	}
	if p.all {
		parts = append(parts, "⌁ "+shortPath(s.Cwd))
	}
	return strings.Join(parts, " · ")
}

// formatSize writes a byte count like "792.7KB".
func formatSize(n int64) string {
	const k = 1024
	switch {
	case n < k:
		return fmt.Sprintf("%dB", n)
	case n < k*k:
		return fmt.Sprintf("%.1fKB", float64(n)/k)
	case n < k*k*k:
		return fmt.Sprintf("%.1fMB", float64(n)/(k*k))
	}
	return fmt.Sprintf("%.1fGB", float64(n)/(k*k*k))
}

// cmdResume opens the agent center on its saved sessions (Inactive): the
// center is where sessions are picked now.
// A phone or browser (/remote) gets the picker, which it can show.
func (a *App) cmdResume(arg string) {
	if a.fromRemote {
		a.cmdSessions(arg)
		return
	}
	a.openAgents(tabInactive)
}

// cmdSessions opens the session picker, which also archives, renames and
// previews saved sessions. It works mid-turn too: picking a session
// interrupts the running turn and switches once it has stopped.
func (a *App) cmdSessions(string) {
	p := newResumePicker(a.cwd, a.sess.Path)
	if items := p.list.Items; len(items) > 1 && items[0].Data.(session.Summary).Path == a.sess.Path {
		p.list.Selected = 1 // the open session is first; default to the one before it
	}
	p.onCancel = a.closeModal
	p.onArchive = func(s session.Summary) {
		var err error
		if s.Archived {
			_, err = session.Unarchive(s.Path)
		} else {
			if s.Path == a.sess.Path {
				a.sess.Close() // reopened lazily; a new session starts below
			}
			_, err = session.Archive(s.Path)
			if err == nil && s.Path == a.sess.Path && !a.busy {
				a.reset()
				a.newSession("other")
				a.notice("Archived the current conversation and started a new one.")
			}
		}
		if err != nil {
			a.errorNotice(err)
		}
	}
	p.onRename = func(s session.Summary, name string) error {
		if s.Path != a.sess.Path {
			return session.Rename(s.Path, name)
		}
		a.sessName = name // the open session: its own writer keeps the file in order
		a.editor.Title = a.sessName
		a.sess.Append(session.Entry{Type: session.TypeName, Name: name})
		a.statusTrigger()
		return a.sess.Err()
	}
	p.onPick = func(s session.Summary) {
		a.closeModal()
		if s.Path == a.sess.Path {
			return // already open
		}
		path := s.Path
		if s.Archived {
			var err error
			if path, err = session.Unarchive(s.Path); err != nil {
				a.errorNotice(err)
				return
			}
		}
		if a.busy {
			a.pendingResume = path
			a.cancel()
			return
		}
		a.resume(path)
	}
	a.openModal(p)
}

// resume loads a session file, restores the agent and redraws the
// transcript, then keeps appending to the same file.
func (a *App) resume(path string) {
	saved, file, err := core.Open(path)
	if err != nil {
		a.errorNotice(err)
		return
	}
	if l, ok := session.LockedBy(path); ok && !(l.Kind == session.KindTUI && l.PID == os.Getpid()) { // not our own
		if l.Kind == session.KindTUI { // open in another terminal: two writers would undo each other
			file.Close()
			a.errorNotice(session.LockError(l))
			return
		}
		a.resumeLocked(saved, file, l) // left running in the background
		return
	}
	h := saved.Header
	a.leaveSession("resume")
	a.reset()
	a.closeSession()
	a.sess = file
	a.lockSession()
	core.Bind(a.agent, a.hooks, a.sess, h.Time, true) // the session's own date keeps the prefix cache
	a.setLiveSession(h.ID)
	a.sessionStartHook("resume")
	branch := saved.Branch()
	a.agent.Restore(branch)
	a.agent.SetLongContext(saved.LongContext)
	a.ctxTokens = a.agent.ContextTokens()
	a.usage.fromEntries(saved.Entries, a.models)
	a.recModel, a.recEffort, a.sessName = "", "", saved.Name
	a.editor.Title = a.sessName
	if ref, ok := a.models.Find("", saved.Model); ok {
		a.agent.SetModel(ref)
		a.recModel = saved.Model
		a.modelFrom = core.FromSession
	}
	if saved.Effort != "" {
		a.agent.SetEffort(saved.Effort)
		a.recEffort = saved.Effort
		a.effortFrom = core.FromSession
	}
	a.showLoaded()
	entries := saved.Entries
	a.replay(branch)
	a.restoreGoal(entries)
	if h.Cwd != a.cwd {
		a.notice("Resumed a session from %s; commands run in %s.", shortPath(h.Cwd), shortPath(a.cwd))
	}
	label := h.Time.Local().Format("2006-01-02 15:04")
	if a.sessName != "" {
		label = fmt.Sprintf("%q (%s)", a.sessName, label)
	}
	a.notice("Resumed session %s.", label)
	a.statusTrigger()
}

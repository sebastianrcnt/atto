package app

import (
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

// Going back, after pi. Sessions are trees (see package session): /tree
// shows every entry, and picking one moves the active leaf there, so the
// conversation continues from that point while the old branch stays in
// the file. Picking a user message goes back to just before it and puts
// its text in the editor to edit and resend. /fork copies the path up to a
// user message into a new session instead. Esc twice on an empty prompt
// opens one of them (settings.json "doubleEscapeAction": tree, fork, none).

// doubleEscWindow is pi's: the second Esc must follow within 500ms.
const doubleEscWindow = 500 * time.Millisecond

// doubleEsc detects Esc pressed twice in quick succession.
type doubleEsc struct {
	last time.Time
	now  func() time.Time // for tests
}

// press records an Esc and reports whether it completes a double Esc.
func (d *doubleEsc) press() bool {
	now := time.Now
	if d.now != nil {
		now = d.now
	}
	t := now()
	if !d.last.IsZero() && t.Sub(d.last) < doubleEscWindow {
		d.last = time.Time{}
		return true
	}
	d.last = t
	return false
}

// reset forgets a pending first Esc (another key came in between).
func (d *doubleEsc) reset() { d.last = time.Time{} }

// onEscape handles Esc while idle. Only an empty prompt arms the double
// Esc; the interrupt Esc during a turn never does (codex and pi agree), so
// pressing Esc twice to stop a turn cannot open a picker.
func (a *App) onEscape() bool {
	if strings.TrimSpace(a.editor.Text()) != "" || a.escAction == "none" {
		a.esc.reset()
		return false
	}
	if !a.esc.press() {
		return true
	}
	if a.escAction == "fork" {
		a.cmdFork("")
	} else {
		a.cmdTree("")
	}
	return true
}

// loadSession reads the open session file; a session with no entries yet
// has no file.
func (a *App) loadSession() []session.Entry {
	_, entries, err := session.Load(a.sess.Path)
	if err != nil && !os.IsNotExist(err) {
		a.errorNotice(err)
	}
	return entries
}

// pickerRows sizes the tree like pi: half the terminal, at least 5 rows.
func pickerRows() int {
	_, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || h <= 0 {
		h = 24
	}
	return max(5, h/2)
}

// cmdTree opens the session tree. It works mid-turn: picking an entry
// interrupts the turn and moves once it has stopped, as pi does.
func (a *App) cmdTree(string) {
	entries := a.loadSession()
	if len(entries) == 0 {
		a.notice("No entries in session.")
		return
	}
	p := newTreePicker(session.Tree(entries), session.Leaf(entries), pickerRows())
	p.onCancel = a.closeModal
	p.onSelect = func(id string) {
		a.closeModal()
		a.selectTreeEntry(id)
	}
	p.onLabel = func(id, label string) {
		a.sess.Append(session.Entry{Type: session.TypeLabel, TargetID: id, Label: label})
	}
	p.onCopy = func(text string) {
		if strings.TrimSpace(text) == "" {
			a.notice("Selected entry has no text to copy.")
			return
		}
		a.copyText(text, func(note string) { a.showToast("Selected entry: " + note) })
	}
	a.openModal(p)
}

// navigateTree moves the active leaf to entry id: before it for a user
// message (whose text goes to the editor), onto it otherwise.
func (a *App) navigateTree(id string) { a.moveTo(id, nil) }

// moveTo is navigateTree, first summarizing the branch being left when
// sum is set (see branchsummary.go).
func (a *App) moveTo(id string, sum *summaryRequest) {
	if a.turns.Busy {
		// Queued and pending input belonged to the old branch: back to the
		// editor, as pi does before aborting.
		a.pendingTree, a.pendingSummary, a.pendingResume = id, sum, ""
		a.stashPending()
		a.turns.Cancel(nil)
		return
	}
	entries := a.loadSession()
	if id == session.Leaf(entries) {
		a.notice("Already at this point.")
		return
	}
	leaf, text, ok := session.BranchPoint(entries, id)
	if !ok {
		a.notice("That entry is no longer in the session.")
		return
	}
	if sum != nil {
		if left := session.Abandoned(entries, session.Leaf(entries), leaf); agent.HasBranchContent(left) {
			a.summarizeBranch(id, leaf, text, left, sum.instructions)
			return
		}
	}
	a.finishMove(entries, id, leaf, text, nil)
}

// finishMove moves the leaf to leaf, recording summary (if any) there,
// and shows the branch. id is the entry picked in the tree and text the
// message to edit.
func (a *App) finishMove(entries []session.Entry, id, leaf, text string, summary *session.Entry) {
	a.stashPending()
	if summary != nil {
		a.sess.BranchSummary(leaf, *summary)
	} else {
		a.sess.Branch(leaf)
	}
	if err := a.sess.Err(); err != nil {
		a.errorNotice(err)
		return
	}
	a.showBranch(a.loadSession())
	if text != "" && strings.TrimSpace(a.editor.Text()) == "" {
		a.editor.SetText(text, editorImages(entries, id)...)
	}
	a.notice("Navigated to the selected point. The earlier branch is kept (/tree).")
	a.afterGoingBack()
}

// stashPending moves queued messages and unsent steers to the editor.
func (a *App) stashPending() {
	var texts []string
	var att []tui.Attachment
	for _, s := range a.agent.DrainSteers() {
		if !isEvent(s) {
			texts = append(texts, s)
		}
	}
	for _, q := range a.turns.Queued {
		texts, att = append(texts, q.text), append(att, q.att...)
	}
	if n := a.turns.SendNow; n != nil {
		texts, att = append(texts, n.text), append(att, n.att...)
	}
	a.turns.Queued, a.turns.Steers, a.turns.QueuePaused, a.turns.SendSteersAfterInterrupt, a.turns.SendNow = nil, nil, false, false, nil
	if len(texts) > 0 {
		a.restoreToEditor(texts, att...)
	}
}

// editorImages turns the images of user message id back into editor
// attachments, labeled like the placeholders in its text ("[image 1: …]").
func editorImages(entries []session.Entry, id string) []tui.Attachment {
	var out []tui.Attachment
	for _, e := range entries {
		if e.ID != id || e.Message == nil {
			continue
		}
		for i, im := range e.Message.Images {
			if loaded, err := images.Load(im); err == nil {
				im = loaded
			}
			out = append(out, tui.Attachment{Label: fmt.Sprintf("[image %d: %s]", i+1, images.Label(im)), Value: im})
		}
		break
	}
	return out
}

// showBranch loads the active branch into the agent and redraws the
// transcript from it, like a resume. The agent gets the branch's messages
// exactly as they were recorded, so the next request repeats the old
// prefix byte for byte and hits the cache.
func (a *App) showBranch(entries []session.Entry) {
	branch := session.Active(entries)
	a.agent.Restore(branch)
	a.ctxTokens = a.agent.ContextTokens()
	a.ui.Body.Clear()
	a.ui.Redraw()
	a.ui.ScrollToBottom()
	a.addHeader()
	a.replay(branch)
	a.statusTrigger()
}

// afterGoingBack pauses an active goal (its progress may be gone) and
// points out background jobs, which keep running: going back does not undo
// what commands already did.
func (a *App) afterGoingBack() {
	if g := a.goal.Goal; g != nil && g.Status == goal.Active {
		g.Status, g.Note = goal.Paused, "went back in the session"
		a.goal.Set(g)
		a.notice("The goal is paused. /goal resume to continue.")
	}
	if a.jobCount > 0 {
		a.notice("%d background job(s) keep running (/jobs).", a.jobCount)
	}
	a.notice("Files changed by commands on the old branch stay changed.")
}

// cmdFork lists the session's user messages (on every branch, as pi does)
// and starts a new session from just before the chosen one.
func (a *App) cmdFork(string) {
	entries := a.loadSession()
	sel := &tui.SelectList{Title: "Fork from a message (enter to choose, esc to cancel)", Filterable: true, MaxVisible: 10}
	for _, e := range entries {
		m := e.Message
		if e.Type != session.TypeMessage || m == nil || m.Role != "user" || strings.TrimSpace(m.Content) == "" || isEvent(m.Content) {
			continue
		}
		sel.Items = append(sel.Items, tui.SelectItem{Label: oneLine(m.Content), Value: e.ID})
	}
	if len(sel.Items) == 0 {
		a.notice("No messages to fork from.")
		return
	}
	sel.Selected = len(sel.Items) - 1
	sel.OnCancel = a.closeModal
	sel.OnSelect = func(it tui.SelectItem) {
		a.closeModal()
		if a.turns.Busy {
			a.notice("Still working — press esc to interrupt first.")
			return
		}
		a.fork(it.Value)
	}
	a.openModal(sel)
}

// fork switches to a new session holding the path to just before user
// message id, with its text in the editor. Like /resume, it stops the
// background jobs of the session it leaves.
func (a *App) fork(id string) {
	entries := a.loadSession()
	leaf, text, ok := session.BranchPoint(entries, id)
	if !ok {
		return
	}
	a.stashPending()
	w := session.Fork(a.sess.Path, a.cwd, entries, leaf)
	w.Close()
	if err := w.Err(); err != nil {
		a.errorNotice(err)
		return
	}
	if _, err := os.Stat(w.Path); err == nil {
		a.resume(w.Path)
	} else { // forked before the first message: nothing to copy
		a.reset()
		a.newSession("other")
	}
	if strings.TrimSpace(a.editor.Text()) == "" {
		a.editor.SetText(text, editorImages(entries, id)...)
	}
	a.notice("Forked to a new session.")
}

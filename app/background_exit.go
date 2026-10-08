package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/fsutil"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/shell"
	"github.com/sebastianrcnt/atto/tui"
)

// EXPERIMENTAL, and meant to be easy to revert (git revert the commit that
// added this file). Like codex, leaving atto while a turn is running (or a
// goal will go on) asks what to do: cancel the task, exit, or "Run in
// background", which leaves the turn to a detached "atto _continue <id>"
// (cli/bgrun.go) and exits. While that process runs it holds the session's
// lock (session/lock.go): atto opens such a session read-only.
//
// What happens to the turn when it is sent to the background:
//   - a model request in flight is canceled and its partial step dropped
//     (Agent.DiscardPartial); the background run repeats the request;
//   - a running shell command moves to the background as a job, as Ctrl+B
//     does, and the request after it is canceled instead (if that takes
//     longer than bgMoveWait the command is canceled);
//   - further tool calls of that step do not run: they are recorded as
//     canceled, as Esc does;
//   - messages typed during the turn and not yet delivered are dropped.
//
// settings.json "backgroundExit": false turns the menu off.

// bgMoveWait is how long a running command gets to move to the background.
const bgMoveWait = 10 * time.Second

// bgExit is the state of the exit menu and of a run sent to the background.
type bgExit struct {
	off       bool // settings.json backgroundExit: false
	pending   bool // "Run in background" was picked; the turn is stopping
	awaitTool bool // ... after the command being moved to a job ends
	timer     *time.Timer
	// spawn starts the background run of the session and returns its pid
	// and log file (nil: spawnContinue); tests replace it.
	spawn func(id, path, cwd string) (pid int, log string, err error)
	// line is printed when atto exits after the run was sent away.
	line string
}

// exitRunning reports whether leaving now would stop work: a turn is
// running, or a goal is active and will start the next one.
func (a *App) exitRunning() bool {
	return (a.turns.Busy && a.runKind == "turn") || (a.goal.Active() && !a.goal.Held())
}

// requestQuit is every way of exiting (ctrl+c, ctrl+d, /quit, /exit): the
// menu when work would be lost, else it quits.
func (a *App) requestQuit() {
	if a.bgx.off || a.bgx.pending || a.sess.ReadOnly() != "" || !a.exitRunning() {
		a.doQuit()
		return
	}
	a.exitMenu()
}

// exitMenuAvailable is whether ctrl+d may open the menu while a turn runs.
func (a *App) exitMenuAvailable() bool {
	return !a.bgx.off && a.sess.ReadOnly() == "" && a.exitRunning()
}

const (
	exitCancel = "cancel"
	exitBG     = "background"
	exitDetach = "detach"
	exitQuit   = "exit"
)

type exitMenuModal struct{ list *tui.SelectList }

func (m exitMenuModal) HandleInput(data string) {
	if i := strings.Index("123", data); len(data) == 1 && i >= 0 && i < len(m.list.Items) {
		m.list.Selected = i
		m.list.HandleInput("\r")
		return
	}
	m.list.HandleInput(data)
}

func (m exitMenuModal) Render(width int) []string {
	return append(m.list.Render(width), "", tui.Truncate(tui.Dim("  enter select · esc back"), width, "…"))
}

func (a *App) exitMenu() {
	bg := tui.SelectItem{Label: "2. Run in background", Detail: "Exit atto and leave the task running", Value: exitBG}
	if a.pane.on {
		// In the daemon the task goes on as it is: leave the terminal only.
		bg = tui.SelectItem{Label: "2. Detach", Detail: "Leave atto running; atto attach to return", Value: exitDetach}
	}
	l := &tui.SelectList{Items: []tui.SelectItem{
		{Label: "1. Cancel task", Detail: "Stop the current task and stay in atto", Value: exitCancel},
		bg,
		{Label: "3. Exit", Detail: "Stop the current task and exit atto", Value: exitQuit},
	}}
	l.RenderRow = func(it tui.SelectItem, selected bool, width int) []string {
		label := it.Label + strings.Repeat(" ", max(0, 22-len(it.Label)))
		line := "  " + label
		if selected {
			line = tui.FG(6, "› "+label)
		}
		return []string{tui.Truncate(line+tui.Dim(it.Detail), width, "…")}
	}
	l.OnCancel = a.closeModal
	l.OnSelect = func(it tui.SelectItem) {
		// closeModal lets an idle goal's next turn start: do the choice first.
		switch it.Value {
		case exitCancel:
			a.cancelTask()
			a.closeModal()
		case exitBG:
			a.dismissModal()
			a.runInBackground()
		case exitDetach:
			a.closeModal()
			a.detach()
		case exitQuit:
			a.dismissModal()
			a.doQuit()
		}
	}
	a.openModal(exitMenuModal{l})
}

// dismissModal closes the menu without starting whatever waited for it.
func (a *App) dismissModal() {
	a.promptGone()
	a.ui.Screen = nil
	a.modal = nil
	a.ui.SetFocus(a.editor)
}

// cancelTask stops the running turn; an idle goal is paused.
func (a *App) cancelTask() {
	if a.turns.Busy {
		a.interruptTurn() // afterRun pauses the goal, as for esc
		return
	}
	if g := a.goal.Goal; g != nil && a.goal.Active() {
		g.Status, g.Note = goal.Paused, goal.NoteInterrupted
		a.goal.Set(g)
		a.notice("Goal paused.")
	}
}

// runInBackground stops the turn the way the package comment says and, once
// it has stopped (backgroundAfterRun), hands the session over.
func (a *App) runInBackground() {
	a.bgx.pending = true
	if !a.turns.Busy { // only a goal that would go on
		a.bgx.pending = false
		a.goalSnapshot()
		a.startBackgroundRun()
		return
	}
	a.notice("Stopping the current step to continue in the background…")
	a.agent.DiscardPartial.Store(true)
	if a.agent.Background() {
		a.bgx.awaitTool = true
		a.bgx.timer = time.AfterFunc(bgMoveWait, func() { a.ui.Do(a.bgCancel) })
		return
	}
	a.bgCancel()
}

func (a *App) bgCancel() {
	if a.bgx.pending && a.turns.Cancel != nil {
		a.turns.Cancel(nil)
	}
}

// backgroundEvent follows the agent's events while a command is moving to
// the background: once it has, the turn is stopped.
func (a *App) backgroundEvent(ev any) {
	if _, ok := ev.(agent.ToolEnd); ok && a.bgx.pending && a.bgx.awaitTool {
		a.bgx.awaitTool = false
		a.bgCancel()
	}
}

// goalSnapshot records the goal as it is, so the background run takes up
// its usage where it stands.
func (a *App) goalSnapshot() {
	a.goal.Poll()
	if g := a.goal.Goal; g != nil {
		a.goal.Set(g)
	}
}

// backgroundAfterRun is called when a turn ends (afterRun). It reports
// whether it took over: the turn stopped for the background run, which is
// started now, or the work had already finished.
func (a *App) backgroundAfterRun(err error) bool {
	if !a.bgx.pending {
		return false
	}
	a.bgx.pending, a.bgx.awaitTool = false, false
	if a.bgx.timer != nil {
		a.bgx.timer.Stop()
	}
	a.agent.DiscardPartial.Store(false)
	canceled := errors.Is(err, context.Canceled)
	switch {
	case err != nil && !canceled:
		return false // it failed by itself: say so and stay
	case err == nil:
		// Finished before it could be stopped: only a goal has more to do.
		if a.runKind == "turn" {
			a.goal.EndTurn(nil)
		}
		a.runKind = ""
		if !a.goal.Active() || a.goal.Held() {
			a.notice("The task finished.")
			a.doQuit()
			return true
		}
	default:
		a.goalSnapshot() // not EndTurn: the goal stays active
	}
	a.runKind = ""
	a.startBackgroundRun()
	return true
}

// startBackgroundRun starts the detached run and, if it started, exits.
func (a *App) startBackgroundRun() {
	a.recordSettings()
	a.sess.Close() // flush entries, but retain the TUI lease until handoff
	spawn := a.bgx.spawn
	if spawn == nil {
		spawn = spawnContinue
	}
	_, log, err := spawn(a.sess.ID, a.sess.Path, a.cwd)
	if err != nil {
		// The failed handoff retained our TUI lease.
		a.errorNotice(fmt.Errorf("could not run in the background: %w", err))
		return
	}
	a.closeSession() // old release cannot unlock the transferred lease
	name := a.sessName
	if name == "" {
		name = a.sess.ID
	}
	a.bgx.line = fmt.Sprintf("Running in background: %s · atto resume %s to check · log: %s", name, a.sess.ID, log)
	a.doQuit()
}

// printExit is called as atto exits: after a run was sent away it prints
// the one line and reports true; the session's jobs and goal belong to the
// background run, so nothing else is cleaned up.
func (a *App) printExit() bool {
	if a.bgx.line == "" {
		return false
	}
	fmt.Println(a.bgx.line)
	return true
}

// leaveCore cleans up the session as atto exits (core.Leave), except one
// that is read-only: its jobs and goal belong to the process running it.
func (a *App) leaveCore() int {
	if a.sess.ReadOnly() != "" {
		return 0
	}
	return core.Leave(a.sess.ID)
}

func openBackgroundLog(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	if err := fsutil.PrivateFile(f); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// spawnContinue starts "atto _continue <id>" detached from this terminal,
// its output going to the session's log file, and locks the session for it.
func spawnContinue(id, path, cwd string) (int, string, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, "", err
	}
	log := session.LogPath(path)
	f, err := openBackgroundLog(log)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	cmd := exec.Command(exe, "_continue", id)
	cmd.Dir = cwd
	cmd.Stdout, cmd.Stderr = f, f
	shell.Detach(cmd)
	if err := session.StartBackground(path, cmd); err != nil {
		return 0, "", err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, log, nil
}

// --- read-only sessions ---

// resumeLocked opens a session a background run is writing: its saved
// transcript, read-only. ctrl+r reads it again, and opens it normally once
// the run has finished.
func (a *App) resumeLocked(saved core.Saved, file *session.Writer, l session.LockInfo) {
	a.leaveSession("resume")
	a.reset()
	a.closeSession()
	file.SetReadOnly(session.ReadOnlyMessage(l))
	a.sess = file
	a.setLiveSession("") // the run owns the session's inbox and timers
	a.resetGoal()
	a.recModel, a.recEffort, a.sessName = "", "", saved.Name
	a.editor.Title = a.sessName
	a.replay(saved.Branch())
	a.notice("Opened %s read-only. ctrl+r reads it again.", saved.Header.ID)
	a.statusTrigger()
}

// renderReadOnly is the banner above the input of a read-only session.
func (a *App) renderReadOnly(width int) []string {
	why := a.sess.ReadOnly()
	if why == "" {
		return nil
	}
	return []string{tui.Truncate(tui.Dim("  "+why+" · ctrl+r to refresh"), width, "…")}
}

// readOnlyAllowed are the commands that work on a read-only session.
var readOnlyAllowed = []string{"quit", "exit", "resume", "clear", "tui"}

// refuseReadOnly refuses text typed into a read-only session (restoring it
// to the editor), except the commands of readOnlyAllowed.
func (a *App) refuseReadOnly(text string) bool {
	why := a.sess.ReadOnly()
	if why == "" || strings.TrimSpace(text) == "" {
		return false
	}
	if rest, ok := strings.CutPrefix(text, "/"); ok {
		name, _, _ := strings.Cut(rest, " ")
		for _, c := range readOnlyAllowed {
			if name != "" && strings.HasPrefix(c, name) {
				return false
			}
		}
	}
	a.restoreToEditor([]string{text})
	a.notice("%s", why)
	return true
}

// readOnlyKey handles the keys of a read-only session: ctrl+r refreshes,
// tab (which would queue a message) does nothing.
func (a *App) readOnlyKey(data string) bool {
	if a.sess.ReadOnly() == "" {
		return false
	}
	switch tui.Key(data) {
	case "ctrl+r":
		a.resume(a.sess.Path)
		return true
	case "tab":
		return true
	}
	return false
}

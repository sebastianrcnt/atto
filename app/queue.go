package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/tui"
)

// Pending input follows codex-rs:
//
//   - Enter while a turn runs sends a steer. It is delivered into the running
//     turn after the next tool call (or when the model stops, which continues
//     the turn). Esc interrupts and sends pending steers immediately.
//   - Tab queues a follow-up that starts as a new turn when the current one
//     ends. Shift+Left pulls the last steer not yet delivered, else the last
//     queued message, back into the editor.

const previewLineLimit = 3

func (a *App) steer(text string) {
	a.agent.Steer(text)
	a.pendingSteers = append(a.pendingSteers, text)
	events.Wake(a.sess.ID) // `atto sleep` / `atto job wait` return early
}

// queuedInput is a follow-up waiting for the current turn to end.
type queuedInput struct {
	text   string
	att    []tui.Attachment // images
	remote bool             // sent from /remote
}

func (a *App) enqueue(text string, att []tui.Attachment) {
	a.queued = append(a.queued, queuedInput{text, att, a.fromRemote})
	a.maybeSendNextQueued()
}

// queueFromEditor handles Tab: queue the draft, or submit it when idle.
func (a *App) queueFromEditor() {
	text, att := a.editor.Commit()
	if text == "" {
		return
	}
	if _, _, isShell := parseShell(text); !a.busy || isShell && len(att) == 0 {
		a.submit(text, att) // a shell command runs at once, even during a turn
		return
	}
	a.enqueue(text, att)
}

// sendNowFromEditor handles Ctrl+Enter: while a turn runs, the draft
// interrupts it and goes out as a new turn, after the steers the turn has not
// taken. Otherwise it is Enter, which also covers commands and "!" lines.
func (a *App) sendNowFromEditor(text string, att []tui.Attachment) {
	_, _, isShell := parseShell(text)
	if !a.busy || a.runKind != "turn" || a.sendNow != nil || a.bgx.pending || a.noModel() ||
		strings.HasPrefix(text, "/") || isShell {
		a.submit(text, att)
		return
	}
	if len(att) > 0 && !a.model().Model.Images() {
		a.submit(text, att) // says images are not supported, keeps the draft
		return
	}
	if text == "" && len(a.pendingSteers) == 0 {
		return // nothing to send
	}
	a.ui.ScrollToBottom()
	if text != "" {
		a.sendNow = &queuedInput{text, att, a.fromRemote}
	}
	a.goal.Replace()
	a.sendSteersAfterInterrupt = len(a.pendingSteers) > 0
	a.cancel()
}

// editLastSteer pulls the last steer back into the editor if the turn has
// not taken it yet.
func (a *App) editLastSteer() bool {
	n := len(a.pendingSteers)
	if n == 0 || !a.agent.Unsteer(a.pendingSteers[n-1]) {
		return false
	}
	last := a.pendingSteers[n-1]
	a.pendingSteers = a.pendingSteers[:n-1]
	if cur := a.editor.Text(); strings.TrimSpace(cur) != "" {
		last += "\n" + cur
	}
	a.editor.SetText(last)
	return true
}

func (a *App) editLastQueued() {
	last := a.queued[len(a.queued)-1]
	a.queued = a.queued[:len(a.queued)-1]
	text := last.text
	if cur := a.editor.Text(); strings.TrimSpace(cur) != "" {
		text += "\n" + cur
	}
	a.editor.SetText(text, last.att...)
}

// restoreToEditor puts texts back in front of the current draft; att are
// image attachments whose labels are in texts.
func (a *App) restoreToEditor(texts []string, att ...tui.Attachment) {
	text := strings.Join(texts, "\n\n")
	if cur := a.editor.Text(); strings.TrimSpace(cur) != "" {
		text += "\n\n" + cur
	}
	a.editor.SetText(text, att...)
}

// afterRun settles pending input once a turn or compaction finishes.
func (a *App) afterRun(err error) {
	a.flushShell()
	if a.backgroundAfterRun(err) {
		return
	}
	if a.runKind == "branchSummary" {
		a.runKind = ""
		if a.afterBranchSummary(err) {
			return
		}
	}
	if a.runKind == "turn" {
		a.goal.EndTurn(err)
	}
	if a.runKind == "turn" && err == nil {
		took := "<1s"
		if d := time.Since(a.runStart); d >= time.Second {
			took = tui.FormatDuration(d.Truncate(100 * time.Millisecond))
		}
		a.notice("Worked for %s • %s", took, time.Now().Format("3:04 PM"))
		if a.goal.Held() {
			a.notice(goalWaitingNotice)
		}
		a.notifyIdle()
	}
	a.runKind = ""
	if id := a.pendingTree; id != "" {
		a.dropSendNow()
		sum := a.pendingSummary
		a.pendingTree, a.pendingSummary = "", nil
		a.moveTo(id, sum)
		return
	}
	if p := a.pendingResume; p != "" {
		a.pendingResume = ""
		a.agent.DrainSteers()
		a.pendingSteers, a.sendSteersAfterInterrupt = nil, false
		a.dropSendNow()
		a.resume(p)
		return
	}
	canceled := errors.Is(err, context.Canceled)
	// Goal notes were for the turn that just ended: they never start one.
	leftover := slices.DeleteFunc(a.agent.DrainSteers(), goal.IsMessage)
	a.pendingSteers = nil
	sendSteers := a.sendSteersAfterInterrupt
	a.sendSteersAfterInterrupt = false
	now := a.sendNow
	a.sendNow = nil

	if now != nil {
		// Ctrl+Enter: the steers and the draft, in the order typed. A turn
		// that failed instead keeps them in the editor, as for steers.
		if err == nil || canceled {
			for _, t := range leftover {
				a.fromRemote = a.takeRemoteSteer(t) || a.fromRemote
			}
			a.fromRemote = now.remote || a.fromRemote
			a.startTurn(strings.Join(append(leftover, now.text), "\n\n"), now.att)
			a.fromRemote = false
			return
		}
		a.restoreToEditor(append(leftover, now.text), now.att...)
		leftover = nil
	}

	if len(leftover) > 0 {
		// Steers that raced with the end of the turn, or that the user asked
		// to send right away with Esc, start the next turn. Otherwise (error,
		// Ctrl+C) they go back into the editor.
		if err == nil || (canceled && sendSteers) {
			for _, t := range leftover {
				a.fromRemote = a.takeRemoteSteer(t) || a.fromRemote
			}
			a.startTurn(strings.Join(leftover, "\n\n"), nil)
			a.fromRemote = false
			return
		}
		a.restoreToEditor(leftover)
	}

	if len(a.pendingEvents) > 0 && err == nil {
		a.deliverEvents()
		if a.busy {
			return
		}
	}
	if err != nil && len(a.queued) > 0 {
		a.queuePaused = true
		a.notice("Queued messages paused. Press enter on an empty prompt to resume, or shift+← to edit.")
		return
	}
	a.maybeSendNextQueued()
}

// dropSendNow gives Ctrl+Enter's message back to the editor when the turn
// it interrupted ended in a move to another session or point instead.
func (a *App) dropSendNow() {
	if n := a.sendNow; n != nil {
		a.sendNow = nil
		a.restoreToEditor([]string{n.text}, n.att...)
	}
}

// notifyIdle sends the idle_prompt notification when a long turn has ended
// and atto now waits for the user: nothing queued or pending and no goal
// turn about to start (a goal waiting for the user starts none).
func (a *App) notifyIdle() {
	if time.Since(a.runStart) < notifyAfter {
		return
	}
	if len(a.queued) > 0 || len(a.pendingEvents) > 0 || len(a.pendingSteers) > 0 || (a.goal.Active() && !a.goal.Held()) {
		return
	}
	a.notify("idle_prompt", "atto finished and is waiting for your input")
}

// maybeSendNextQueued starts the next queued follow-up when idle. Queued
// slash commands that don't start a run are executed in order.
func (a *App) maybeSendNextQueued() {
	for !a.busy && a.modal == nil && !a.queuePaused && len(a.queued) > 0 {
		next := a.queued[0]
		a.queued = a.queued[1:]
		if strings.HasPrefix(next.text, "/") {
			a.runCommand(next.text)
			continue
		}
		a.fromRemote = next.remote
		a.startTurn(next.text, next.att)
		a.fromRemote = false
		return
	}
	a.continueGoal()
}

func previewLines(text string, width int, style func(string) string) []string {
	var out []string
	wrapped := tui.Wrap(text, max(1, width-4))
	for i, l := range wrapped {
		if i == previewLineLimit {
			out = append(out, "    "+tui.Dim(style("…")))
			break
		}
		prefix := "    "
		if i == 0 {
			prefix = tui.Dim("  ↳ ")
		}
		out = append(out, prefix+tui.Dim(style(l)))
	}
	return out
}

func (a *App) renderPending(width int) []string {
	a.remotePending()
	if len(a.pendingSteers) == 0 && len(a.queued) == 0 {
		return nil
	}
	plain := func(s string) string { return s }
	out := []string{""}
	if len(a.pendingSteers) > 0 {
		out = append(out, tui.Truncate(tui.Dim("• ")+"Messages to be submitted after next tool call"+
			tui.Dim(" (press esc to interrupt and send immediately)"), width, "…"))
		for _, s := range a.pendingSteers {
			out = append(out, previewLines(s, width, plain)...)
		}
		out = append(out, "    "+tui.FG(6, "shift+←")+tui.Dim(" edit last message"))
	}
	if len(a.queued) > 0 {
		if len(a.pendingSteers) > 0 {
			out = append(out, "")
		}
		header := "Queued follow-up inputs"
		if a.queuePaused {
			header += tui.Dim(" (paused — enter on empty prompt to resume)")
		}
		out = append(out, tui.Truncate(tui.Dim("• ")+header, width, "…"))
		for _, q := range a.queued {
			out = append(out, previewLines(q.text, width, tui.Italic)...)
		}
		out = append(out, "    "+tui.FG(6, "shift+←")+tui.Dim(" edit last queued message"))
	}
	return out
}

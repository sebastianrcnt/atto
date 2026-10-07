package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/fsutil"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/shell"
)

// "Run in background" without the daemon (thread/handoff): the session
// goes on in a detached "atto _continue <id>" (cli/bgrun.go) and the
// runtime lets go of it. This is a restart of execution, kept for the
// in-process runtime only; a daemon worker simply goes on when its
// clients detach.
//
//   - a model request in flight is canceled and its partial step dropped
//     (Agent.DiscardPartial); the background run repeats the request;
//   - a running shell command moves to the background as a job, as Ctrl+B
//     does, and the request after it is canceled instead (if that takes
//     longer than bgMoveWait the command is canceled);
//   - further tool calls of that step do not run: they are recorded as
//     canceled, as Esc does;
//   - messages typed during the turn and not yet delivered are dropped.

// bgMoveWait is how long a running command gets to move to the background.
const bgMoveWait = 10 * time.Second

type handoffState struct {
	pending   bool // the run is stopping for the handoff
	awaitTool bool // ... after the command being moved to a job ends
	timer     *time.Timer
}

// Spawn starts the background run of a session and returns its log file;
// tests replace it.
var Spawn = spawnContinue

// handoff starts "Run in background".
func (t *thread) startHandoff() error {
	if t.readOnly != "" {
		return failure(ReasonReadOnly, "%s", t.readOnly)
	}
	if t.handoff.pending {
		return nil
	}
	t.handoff.pending = true
	if !t.busy { // only a goal that would go on
		t.handoff.pending = false
		t.goalSnapshot()
		t.startBackgroundRun()
		return nil
	}
	t.notice("", "Stopping the current step to continue in the background…")
	t.agent.DiscardPartial.Store(true)
	if t.agent.Background() {
		t.handoff.awaitTool = true
		t.handoff.timer = time.AfterFunc(bgMoveWait, func() { t.do(t.handoffCancel) })
		return nil
	}
	t.handoffCancel()
	return nil
}

func (t *thread) handoffCancel() {
	if t.handoff.pending && t.cancel != nil {
		t.cancel()
	}
}

// handoffEvent follows the agent's events while a command is moving to
// the background: once it has, the turn is stopped.
func (t *thread) handoffEvent(ev any) {
	if _, ok := ev.(agent.ToolEnd); ok && t.handoff.pending && t.handoff.awaitTool {
		t.handoff.awaitTool = false
		t.handoffCancel()
	}
}

// goalSnapshot records the goal as it is, so the background run takes up
// its usage where it stands.
func (t *thread) goalSnapshot() {
	t.goal.Poll()
	if g := t.goal.Goal; g != nil {
		t.goal.Set(g)
	}
}

// handoffAfterRun is called when a run ends (afterRun). It reports
// whether it took over: the run stopped for the handoff, which starts
// now, or the work had already finished.
func (t *thread) handoffAfterRun(err error) bool {
	if !t.handoff.pending {
		return false
	}
	t.handoff.pending, t.handoff.awaitTool = false, false
	if t.handoff.timer != nil {
		t.handoff.timer.Stop()
	}
	t.agent.DiscardPartial.Store(false)
	canceled := errors.Is(err, context.Canceled)
	switch {
	case err != nil && !canceled:
		return false // it failed by itself: say so and stay
	case err == nil:
		// Finished before it could be stopped: only a goal has more to do.
		if t.runKind == "turn" {
			t.goal.EndTurn(nil)
		}
		t.runKind = ""
		if !t.goal.Active() || t.goal.Held() {
			t.notice("", "The task finished.")
			t.publish("thread/handedOff", map[string]any{"finished": true})
			return true
		}
	default:
		t.goalSnapshot() // not EndTurn: the goal stays active
	}
	t.runKind = ""
	t.startBackgroundRun()
	return true
}

// startBackgroundRun starts the detached run and lets the session go.
func (t *thread) startBackgroundRun() {
	t.recordSettings()
	t.sess.Close() // flush entries, but retain the lease until handoff
	log, err := Spawn(t.id, t.sess.Path, t.cwd)
	if err != nil {
		// The failed handoff retained our lease.
		t.errorNotice(fmt.Errorf("could not run in the background: %w", err))
		return
	}
	name := t.name
	if name == "" {
		name = t.id
	}
	line := fmt.Sprintf("Running in background: %s · atto resume %s to check · log: %s", name, t.id, log)
	t.publish("thread/handedOff", map[string]any{"line": line})
	// The background run owns the session, its jobs and its goal now:
	// close without ending any of it.
	go t.s.closeThread(t, closeHandoff)
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

// spawnContinue starts "atto _continue <id>" detached, its output going
// to the session's log file, and hands the session's lock to it.
func spawnContinue(id, path, cwd string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	log := session.LogPath(path)
	f, err := openBackgroundLog(log)
	if err != nil {
		return "", err
	}
	defer f.Close()
	cmd := exec.Command(exe, "_continue", id)
	cmd.Dir = cwd
	cmd.Stdout, cmd.Stderr = f, f
	shell.Detach(cmd)
	if err := session.StartBackground(path, cmd); err != nil {
		return "", err
	}
	_ = cmd.Process.Release()
	return log, nil
}

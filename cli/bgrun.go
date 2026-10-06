package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/hooks"
	"github.com/sebastianrcnt/atto/session"
)

// Experimental: "Run in background" of the TUI's exit menu (see
// app/background_exit.go) leaves the session to a detached
// "atto _continue <id>", which is atto -p continuing the session without a
// new user message.

// RunContinue is the hidden entry point of a background run.
func RunContinue(args []string, _ io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: atto _continue <session id> (started by atto)")
	}
	if config.InAgent() {
		return fmt.Errorf("atto _continue can't be run by an atto agent")
	}
	return RunPrint(PrintOptions{Resume: args[0], Format: "text", Verbose: true, Background: true})
}

// lockForRun holds the writer lease for the lifetime of a saved run.
func lockForRun(path string, background, writes bool) (release func(), err error) {
	if !writes {
		return func() {}, nil
	}
	if background {
		return session.LockKind(path, session.KindBackground)
	}
	return session.Lock(path)
}

type bgMode int

const (
	bgNothing    bgMode = iota // the session has nothing left to do
	bgResumeTurn               // a turn was stopped: the model answers next
	bgGoalTurn                 // the turns are over; the goal asks for another
)

// bgPrepare restores the session's goal as it was left (active again) and
// decides what the background run starts with.
func bgPrepare(d *core.GoalDriver, saved core.Saved) bgMode {
	if d.Restore(saved.Entries) { // an active goal comes back paused: wake it
		g := d.Goal
		g.Status, g.Note = goal.Active, ""
		d.Set(g)
	}
	branch := saved.Branch()
	for _, b := range slices.Backward(branch) {
		if m := b.Message; b.Type == session.TypeMessage && m != nil {
			if m.Role == "user" || m.Role == "tool" {
				return bgResumeTurn
			}
			break
		}
	}
	if d.Active() {
		return bgGoalTurn
	}
	return bgNothing
}

// bgNotify fires the Notification hook when the background run is over.
func bgNotify(hk *hooks.Runner, name string, runErr error) {
	if hk == nil {
		return
	}
	msg := "Background run finished: " + name
	if runErr != nil {
		msg += " (" + runErr.Error() + ")"
	}
	for _, n := range hk.Notification(context.Background(), "background_done", msg) {
		fmt.Fprintln(os.Stderr, n)
	}
}

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/update"

	"github.com/sebastianrcnt/atto/daemon"
)

// pane is this atto's link to the daemon pane it runs in, if any: it
// tells the daemon which session it has open, and asks it to detach the
// terminal (package daemon reads these markers out of the output).
type pane struct {
	on               bool
	sess, nam, state string // last reported
}

// startPane hooks up a pane: repaints on the daemon's request, and says it
// can.
func (a *App) startPane() {
	if os.Getenv(daemon.EnvPane) == "" {
		return
	}
	a.pane.on = true
	watchRedraw(a.ui.ForceRedraw)
	a.ui.Emit(daemon.MarkerSeq("ready"))
}

// paneSync reports the open session to the daemon when it changed; the
// status line calls it on every frame, so it costs a comparison.
func (a *App) paneSync() {
	if !a.pane.on {
		return
	}
	id, name := a.threadID, markerSafe(a.sessName)
	if id != a.pane.sess || name != a.pane.nam {
		a.pane.sess, a.pane.nam = id, name
		a.ui.Emit(daemon.MarkerSeq("session", id, name))
	}
	if st := a.paneState(); st != a.pane.state {
		a.pane.state = st
		a.ui.Emit(daemon.MarkerSeq("state", st))
	}
}

// paneState is what the agent center shows for this session: working
// while a turn runs, waiting when it needs the user (a question or picker
// is open, a goal waits for them), else idle.
func (a *App) paneState() string {
	_, center := a.modal.(*agentCenter)
	switch {
	case a.busy:
		return "working"
	case (a.modal != nil && !center) || (a.goalHeld() && a.goalActive()):
		return "waiting"
	}
	return "idle"
}

// markerSafe drops the control characters that would end a marker early.
func markerSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// detach leaves this atto running in its pane and returns the terminal
// that typed last to its shell.
func (a *App) detach() bool {
	if !a.pane.on {
		return false
	}
	a.ui.Emit(daemon.MarkerSeq("detach"))
	return true
}

func (a *App) cmdDetach(string) {
	if !a.detach() {
		a.notice("This atto isn't running in the atto daemon: it started directly (see atto daemon -h).")
	}
}

// writeCrash keeps a panic of the render loop in ~/.atto/logs, where it
// survives the screen it happened on (a pane's, often nobody's).
func writeCrash(v any, stack []byte) {
	dir := filepath.Join(config.Dir(), "logs")
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	path := filepath.Join(dir, "crash-"+time.Now().Format("20060102-150405")+".log")
	if os.WriteFile(path, fmt.Appendf(nil, "atto %s panicked: %v\n\n%s", update.Describe(), v, stack), 0o600) == nil {
		fmt.Fprintf(os.Stderr, "atto: crashed; details in %s\n", path)
	}
}

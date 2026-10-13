// Package jobs runs background commands for a session. Each job runs under
// a small supervisor process (atto re-executed as `atto _supervise <dir>`)
// that outlives the shell call that started it, captures output to a file,
// records the exit status, and drops an event in the session inbox when the
// job ends — so the agent hears about it without polling (an improvement
// on codex, where the model must poll).
//
// State is plain files under ~/.atto/jobs/<session>/<id>/: job.json and
// output.log. Any process (the TUI, the daemon, `atto job` run by the
// model) can read it.
package jobs

import (
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/shell"
)

const (
	// MaxRunning caps concurrent jobs per session (codex: 64 processes).
	MaxRunning = 64
	// maxLog caps output.log; beyond it output is dropped with a note.
	maxLog = 16 << 20
	// eventTail is how many output lines an exit event carries.
	eventTail = 20
)

type Status string

const (
	Starting Status = "starting"
	Running  Status = "running"
	Exited   Status = "exited"
	Killed   Status = "killed"
	Failed   Status = "failed" // could not start
	Lost     Status = "lost"   // supervisor vanished (crash, reboot)
)

// Monitor makes a job re-run its command every Every until a condition
// holds, then report it.
type Monitor struct {
	Every     time.Duration `json:"every"`
	Until     string        `json:"until,omitempty"`     // regexp on output
	OnChange  bool          `json:"onChange,omitempty"`  // output differs from the first run
	UntilExit *int          `json:"untilExit,omitempty"` // command exits with this code
	Timeout   time.Duration `json:"timeout,omitempty"`   // give up after this long
}

type Job struct {
	Type          string     `json:"kind,omitempty"` // agent turns have an explicit lifecycle kind
	ID            int        `json:"id"`
	Session       string     `json:"session"`
	Name          string     `json:"name"`
	Command       string     `json:"command"`
	Cwd           string     `json:"cwd"`
	Status        Status     `json:"status"`
	ExitCode      *int       `json:"exitCode,omitempty"`
	Error         string     `json:"error,omitempty"`
	SupervisorPID int        `json:"supervisorPid,omitempty"`
	PID           int        `json:"pid,omitempty"`
	Started       time.Time  `json:"started"`
	Ended         *time.Time `json:"ended,omitempty"`
	Monitor       *Monitor   `json:"monitor,omitempty"`
	Notify        *Notify    `json:"notify,omitempty"`
	// Args, if set, is run directly instead of Command through the shell;
	// Command then only describes the job.
	Args []string `json:"args,omitempty"`
	// Quiet: a clean exit posts no event, for a command that tells the
	// session how it went itself (an agent turn).
	Quiet bool `json:"quiet,omitempty"`
	// Silent: no exit event at all, whatever happened. The turn of an agent
	// started from a shell is a job of the agent's own session, which must
	// not be woken by its own turn ending; wait and report tell the caller.
	Silent bool `json:"silent,omitempty"`
	// Control, on a turn run by a worker, is how to ask it to stop (see
	// workerturn.go); such a job is never signalled.
	Control *Control `json:"control,omitempty"`
	// QuietExit delivers the exit event without waking an idle session.
	// A user-interrupted command keeps running between agent turns.
	QuietExit bool `json:"quietExit,omitempty"`
}

func (j Job) Kind() string {
	if j.Type != "" {
		return j.Type
	}
	if j.Monitor != nil {
		return "monitor"
	}
	return "job"
}

// KindLabel is Kind for listings: it also shows that a job notifies.
func (j Job) KindLabel() string {
	if j.Notify != nil {
		return j.Kind() + "+notify"
	}
	return j.Kind()
}

func (j Job) Active() bool { return j.Status == Starting || j.Status == Running }

// Label is the job's name, or its command's first line.
func (j Job) Label() string {
	if j.Name != "" {
		return j.Name
	}
	c := strings.TrimSpace(j.Command)
	if i := strings.IndexByte(c, '\n'); i >= 0 {
		c = c[:i] + " …"
	}
	return c
}

// Runtime is how long the job ran (or has been running).
func (j Job) Runtime() time.Duration {
	end := time.Now()
	if j.Ended != nil {
		end = *j.Ended
	}
	return end.Sub(j.Started).Round(time.Second)
}

// Kill stops an active job and its whole process tree.
func Kill(session string, id int) (Job, error) {
	j, err := Get(session, id)
	if err != nil || !j.Active() {
		return j, err
	}
	if j.Control != nil { // a turn in a worker: the worker is not ours to signal
		return killControlled(session, j)
	}
	// A starting job has no supervisor PID yet: wait for it, and if it
	// never comes, record the job killed so a late supervisor doesn't run it.
	for deadline := time.Now().Add(3 * time.Second); j.SupervisorPID == 0 && j.Active() && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if j, err = load(dirOf(session, id)); err != nil {
			return j, err
		}
	}
	if !j.Active() {
		return j, nil
	}
	if j.SupervisorPID == 0 {
		now := time.Now()
		j.Status, j.Ended = Killed, &now
		return j, save(dirOf(session, id), j)
	}
	if err := shell.Terminate(j.SupervisorPID); err != nil && shell.Alive(j.SupervisorPID) {
		return j, err
	}
	// The supervisor records "killed" on Unix; on Windows it is terminated
	// outright, so record it here if it did not.
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if cur, err := load(dirOf(session, id)); err == nil && !cur.Active() {
			return cur, nil
		}
		if !shell.Alive(j.SupervisorPID) {
			break
		}
	}
	cur, err := load(dirOf(session, id))
	if err == nil && cur.Active() {
		now := time.Now()
		cur.Status, cur.Ended = Killed, &now
		err = save(dirOf(session, id), cur)
	}
	return cur, err
}

// KillAll stops every active job of a session.
func KillAll(session string) int { return KillAllExcept(session, nil) }

// KillAllExcept is KillAll sparing the jobs keep says to.
func KillAllExcept(session string, keep func(Job) bool) int {
	n := 0
	for _, j := range List(session) {
		if j.Active() && (keep == nil || !keep(j)) {
			if _, err := Kill(session, j.ID); err == nil {
				n++
			}
		}
	}
	return n
}

// Wait blocks until the job ends, timeout passes, or the user sends input
// (events.Wake). It returns the job and why it returned.
func Wait(session string, id int, timeout time.Duration) (Job, string, error) {
	start := time.Now()
	for {
		j, err := Get(session, id)
		if err != nil || !j.Active() {
			return j, "done", err
		}
		if timeout > 0 && time.Since(start) >= timeout {
			return j, "timeout", nil
		}
		if events.WokenSince(session, start) {
			return j, "woken", nil
		}
		time.Sleep(200 * time.Millisecond)
	}
}

package jobs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sebastianrcnt/atto/fsutil"
)

// An agent's turn can run in the worker of the agent's own session instead of
// a process of its own. It is still a job, so atto job list shows it with the
// same label and wait and kill apply, but nobody supervises it: its
// "supervisor" is the worker (so the job is lost if the worker dies), the
// worker ends it, and stopping it means asking the worker, which Control says
// how to do.

// Control is how a job that runs inside a process it does not own is asked
// to stop: by writing Content to File. The process polls the file.
type Control struct {
	File    string `json:"file"`
	Content string `json:"content"`
}

// InWorker reports whether the job is a turn that runs inside a worker.
func (j Job) InWorker() bool { return j.Control != nil }

// controlWait is how long Kill waits for a worker to stop a turn it was asked
// to. Tests shorten it.
var controlWait = 3 * time.Second

// StartWorkerTurn records an agent's turn running in the worker with process
// ID workerPID as a job of session owner. silent: its end posts no event
// (a root's turn, which no one is woken by).
func StartWorkerTurn(owner, cwd, name string, workerPID int, ctl Control, silent bool) (Job, error) {
	id, dir, err := reserve(owner)
	if err != nil {
		return Job{}, err
	}
	j := Job{Type: "agent", ID: id, Session: owner, Name: name, Command: "atto agent turn (in the agent session's worker)", Cwd: cwd,
		Status: Running, SupervisorPID: workerPID, PID: workerPID, Started: time.Now(), Quiet: true, Silent: silent, Control: &ctl}
	if err := save(dir, j); err != nil {
		return Job{}, err
	}
	note := fmt.Sprintf("%s: the turn runs in the worker of the agent's session, pid %d (atto agent report shows how it went)\n", name, workerPID)
	_ = os.WriteFile(filepath.Join(dir, "output.log"), []byte(note), 0o600)
	return j, nil
}

// EndWorkerTurn records that a turn run by a worker is over: exited, or
// killed when it was stopped. A job already ended (or lost with its worker)
// stays as it is.
func EndWorkerTurn(owner string, id int, killed bool) error {
	dir := dirOf(owner, id)
	j, err := load(dir)
	if err != nil {
		return err
	}
	if !j.Active() {
		return nil
	}
	now := time.Now()
	j.Ended = &now
	if killed {
		j.Status = Killed
	} else {
		code := 0
		j.Status, j.ExitCode = Exited, &code
	}
	return save(dir, j)
}

// killControlled asks the process that runs j to stop, and waits a little
// for it to say it has.
func killControlled(session string, j Job) (Job, error) {
	if err := fsutil.WriteAtomic(j.Control.File, []byte(j.Control.Content), 0o644); err != nil {
		return j, err
	}
	for deadline := time.Now().Add(controlWait); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		cur, err := Get(session, j.ID)
		if err != nil || !cur.Active() {
			return cur, err
		}
	}
	cur, err := Get(session, j.ID)
	if err == nil && cur.Active() {
		err = errors.New("the worker did not stop the turn when asked")
	}
	return cur, err
}

package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/fsutil"
	"github.com/sebastianrcnt/atto/shell"
)

// Supervise runs in the detached supervisor process: it executes the job,
// records the result and posts the exit event.
func Supervise(dir string) (err error) {
	j, err := load(dir)
	if err != nil {
		return err
	}
	if !j.Active() { // killed before it started
		return nil
	}
	// A detached supervisor has no caller watching its stderr. Report failures
	// independently of job.json so a failed state write is not silently lost.
	defer func() {
		if err != nil {
			err = errors.Join(err, reportSupervisionError(dir, j, err))
		}
	}()
	j.SupervisorPID = os.Getpid()
	if err := save(dir, j); err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(dir, "output.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	if err := fsutil.PrivateFile(logf); err != nil {
		return err
	}
	out := &cappedFile{f: logf}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	killed := false
	var killMu sync.Mutex
	defer signal.Stop(sigs)
	go func() {
		select {
		case <-sigs:
			killMu.Lock()
			killed = true
			killMu.Unlock()
			cancel()
		case <-ctx.Done():
		}
	}()

	var code int
	var detail string
	var notifyErr error
	if j.Monitor != nil {
		code, detail, err = superviseMonitor(ctx, dir, &j, out)
	} else if j.Notify != nil {
		nt := newNotifier(out, j, func(e events.Event) {
			if err := events.Push(j.Session, e); err != nil && notifyErr == nil {
				notifyErr = fmt.Errorf("posting job %d notification: %w", j.ID, err)
			}
		})
		code, detail, err = run(ctx, dir, &j, j.Command, nt, true)
		nt.Close()
	} else {
		code, detail, err = run(ctx, dir, &j, j.Command, out, true)
	}

	if err != nil {
		return errors.Join(err, notifyErr)
	}
	killMu.Lock()
	wasKilled := killed
	killMu.Unlock()
	return errors.Join(finish(dir, j, code, detail, wasKilled), notifyErr)
}

// finish records how a job ended and, unless it was killed (by atto job
// kill, which needs no telling), posts the exit event. code -2 means the
// command could not start.
func finish(dir string, j Job, code int, detail string, killed bool) error {
	now := time.Now()
	j.Ended = &now
	switch {
	case killed:
		j.Status = Killed
	case code == -2:
		j.Status = Failed
		j.Error = detail
	default:
		j.Status = Exited
		j.ExitCode = &code
	}
	if err := save(dir, j); err != nil {
		return err
	}
	if !killed && !j.Silent && !(j.Quiet && j.Status == Exited && code == 0) {
		return postEvent(j, detail)
	}
	return nil
}

// run executes command once, streaming output to out. It returns the exit
// code (-2 if it could not start, -1 if killed), a command error detail,
// and an infrastructure error. A state write failure kills and reaps the child.
func run(ctx context.Context, dir string, j *Job, command string, out io.Writer, record bool) (int, string, error) {
	cmd := shell.Command(ctx, command)
	if len(j.Args) > 0 {
		cmd = exec.CommandContext(ctx, j.Args[0], j.Args[1:]...)
	}
	cmd.Dir = j.Cwd
	cmd.Env = append(os.Environ(), "TERM=dumb", "PAGER=cat", "GIT_PAGER=cat", "NO_COLOR=1")
	cmd.Stdout, cmd.Stderr = out, out
	tree := shell.NewTree(cmd)
	defer tree.Close()
	if err := cmd.Start(); err != nil {
		var saveErr error
		if record {
			j.Status, j.Error = Failed, err.Error()
			if saveErr = save(dir, *j); saveErr != nil {
				saveErr = errors.Join(err, saveErr)
			}
		}
		return -2, err.Error(), saveErr
	}
	tree.Started()
	if record {
		j.Status, j.PID = Running, cmd.Process.Pid
		if err := save(dir, *j); err != nil {
			// Never leave a child running without a durable PID/status. Reap
			// it before returning, including descendants holding output pipes.
			tree.Kill()
			_ = cmd.Wait()
			return -1, "", err
		}
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	defer func() { close(done); <-stopped }()
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			tree.Kill()
		case <-done:
		}
	}()
	err := cmd.Wait()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, "", nil
	case errors.As(err, &ee):
		return ee.ExitCode(), "", nil
	}
	return -1, err.Error(), fmt.Errorf("running job %d: %w", j.ID, err)
}

//go:build !windows

package daemon

import (
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"
)

// tryLock takes an exclusive lock on f, held until f closes or the process
// ends; false if another process holds it.
func tryLock(f *os.File) bool {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) == nil
}

// daemonDir is the working directory of the daemon process.
func daemonDir() string { return "/" }

// workerStdin gives the worker a channel for the daemon to ask it to stop.
// On Unix that is a signal, so there is none.
func workerStdin(*exec.Cmd) (io.WriteCloser, error) { return nil, nil }

// stopWorker asks a worker to end gracefully.
func stopWorker(w *worker) { _ = w.cmd.Process.Signal(syscall.SIGTERM) }

// stopRequests delivers requests to end this worker process: the signals
// of a terminal's hangup, interrupt and termination.
func stopRequests() (<-chan struct{}, func()) {
	sigs := make(chan os.Signal, 1)
	out := make(chan struct{}, 1)
	done := make(chan struct{})
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		select {
		case <-sigs:
			out <- struct{}{}
		case <-done:
		}
	}()
	return out, func() { signal.Stop(sigs); close(done) }
}

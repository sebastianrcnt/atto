//go:build windows

package daemon

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"golang.org/x/sys/windows"
)

// tryLock takes an exclusive lock on f, held until f closes or the process
// ends (Windows releases a dead process's locks); false if another process
// holds it.
func tryLock(f *os.File) bool {
	return windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{}) == nil
}

// daemonDir is the working directory of the daemon process: not a project
// directory, which it would keep from being removed.
func daemonDir() string { return os.TempDir() }

// workerStdin gives the worker a channel for the daemon to ask it to stop:
// Windows has no SIGTERM to send, and a console control event only reaches
// processes on the sender's console. The worker reads a line from it.
func workerStdin(cmd *exec.Cmd) (io.WriteCloser, error) { return cmd.StdinPipe() }

// stopWorker asks a worker to end gracefully (closing its session as on
// exit); a worker that cannot be asked is killed.
func stopWorker(w *worker) {
	if w.stdin != nil {
		if _, err := io.WriteString(w.stdin, "stop\n"); err == nil {
			return
		}
	}
	_ = w.cmd.Process.Kill()
}

// stopRequests delivers requests to end this worker process: the daemon's
// line on standard input, and the console events Go reports as signals
// (Ctrl+C, Ctrl+Break, and a console closing, the user logging off or the
// system shutting down).
func stopRequests() (<-chan struct{}, func()) {
	sigs := make(chan os.Signal, 1)
	out := make(chan struct{}, 1)
	done := make(chan struct{})
	request := func() {
		select {
		case out <- struct{}{}:
		default:
		}
	}
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		select {
		case <-sigs:
			request()
		case <-done:
		}
	}()
	go func() {
		// Ending without a line (the daemon went away, or no pipe) is no
		// request: workers outlive their daemon.
		if bufio.NewScanner(os.Stdin).Scan() {
			request()
		}
	}()
	return out, func() { signal.Stop(sigs); close(done) }
}

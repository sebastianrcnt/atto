//go:build !windows

package tui

import (
	"os"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// Open a separate nonblocking description, never changing the shell's
// inherited stdout flags. Normal output waits for the terminal; signal
// cleanup bounds both an in-progress frame and terminal-mode restoration.
type processOutput struct {
	fd          atomic.Int64 // descriptor + 1; zero means unavailable
	interrupted atomic.Bool
}

func (o *processOutput) start(out *os.File) {
	if term.IsTerminal(int(out.Fd())) {
		fd, err := unix.Open("/dev/tty", unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err == nil {
			o.fd.Store(int64(fd) + 1)
		}
	}
}
func (o *processOutput) interrupt() {
	o.interrupted.Store(true)
	if fd := o.fd.Load(); fd > 0 {
		flushTerminalOutput(int(fd - 1))
	}
}
func (o *processOutput) close() {
	if fd := o.fd.Load(); fd > 0 {
		if o.interrupted.Load() {
			// Give a still-attached terminal a bounded chance to consume the
			// mode reset, then discard output rather than blocking in close.
			time.Sleep(100 * time.Millisecond)
			flushTerminalOutput(int(fd - 1))
		}
		_ = unix.Close(int(fd - 1))
		o.fd.Store(0)
	}
}
func (o *processOutput) write(out *os.File, s string) {
	fd := o.fd.Load()
	if fd == 0 {
		_, _ = out.WriteString(s)
		return
	}
	b := []byte(s)
	var deadline time.Time
	for len(b) > 0 {
		n, err := unix.Write(int(fd-1), b)
		if n > 0 {
			b = b[n:]
		}
		if err != nil && err != unix.EAGAIN && err != unix.EINTR {
			return
		}
		if len(b) == 0 {
			return
		}
		if o.interrupted.Load() {
			if deadline.IsZero() {
				deadline = time.Now().Add(100 * time.Millisecond)
			}
			if time.Now().After(deadline) {
				return
			}
		}
		_, err = unix.Poll([]unix.PollFd{{Fd: int32(fd - 1), Events: unix.POLLOUT}}, 50)
		if err != nil && err != unix.EINTR {
			return
		}
	}
}

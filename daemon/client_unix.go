//go:build !windows

package daemon

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// startWait is how long a client waits for a daemon it started.
const startWait = 5 * time.Second

// dial connects to the daemon; with start, it starts one if none runs.
func dial(start bool) (net.Conn, error) {
	sock := SocketPath()
	if err := privateDir(filepath.Dir(sock)); err != nil {
		return nil, err
	}
	c, err := trustedDial(sock)
	if errors.Is(err, errPeer) {
		return nil, err
	}
	if err == nil || !start {
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		return c, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if err := spawn(exe); err != nil {
		return nil, fmt.Errorf("%w: starting it: %v", ErrUnavailable, err)
	}
	deadline := time.Now().Add(startWait)
	for {
		c, err := trustedDial(sock)
		if errors.Is(err, errPeer) {
			return nil, err
		}
		if err == nil {
			return c, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w: it did not start (see %s): %v", ErrUnavailable, LogPath(), err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// spawn starts "atto _daemon" in its own session, with its errors going to
// the log. Two clients racing both spawn one; the second finds the lock
// taken and exits.
func spawn(exe string) error {
	if err := os.MkdirAll(filepath.Dir(LogPath()), 0o700); err != nil {
		return err
	}
	log, err := os.OpenFile(LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(exe, "_daemon")
	cmd.Dir = "/"
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// request sends h to the daemon and returns its first answer.
func request(h Hello, start bool) (net.Conn, byte, []byte, error) {
	c, err := dial(start)
	if err != nil {
		return nil, 0, nil, err
	}
	h.Proto = Proto
	if err := writeJSON(c, fHello, h); err != nil {
		c.Close()
		return nil, 0, nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	typ, b, err := readFrame(c)
	if err != nil {
		c.Close()
		return nil, 0, nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if typ == fError {
		c.Close()
		return nil, 0, nil, errors.New(string(b))
	}
	return c, typ, b, nil
}

// Stop ends the daemon; with workers running only when force.
func Stop(force bool) error {
	c, _, _, err := request(Hello{Op: "stop", Force: force}, false)
	if err != nil {
		return err
	}
	return c.Close()
}

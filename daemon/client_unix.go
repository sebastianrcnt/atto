//go:build !windows

package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"
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
	c, typ, b, err := requestVersion(h, start, Proto)
	// The pane operations have kept their frames and shapes since version
	// 1, so a daemon left running across an upgrade still lists, shows,
	// kills and stops its panes. Never downgrade execution (new, workers).
	if paneOp[h.Op] {
		for version := Proto - 1; errors.Is(err, ErrProtocol) && version >= 1; version-- {
			c, typ, b, err = requestVersion(h, start, version)
		}
	}
	return c, typ, b, err
}

var paneOp = map[string]bool{"attach": true, "list": true, "kill": true, "stop": true}

func requestVersion(h Hello, start bool, version int) (net.Conn, byte, []byte, error) {
	c, err := dial(start)
	if err != nil {
		return nil, 0, nil, err
	}
	h.Proto = version
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
		if strings.HasPrefix(string(b), "the running atto daemon speaks protocol ") {
			return nil, 0, nil, fmt.Errorf("%w: %s", ErrProtocol, b)
		}
		return nil, 0, nil, errors.New(string(b))
	}
	return c, typ, b, nil
}

// List returns the daemon's panes; none when no daemon runs.
func List() ([]Pane, error) {
	c, typ, b, err := request(Hello{Op: "list"}, false)
	if errors.Is(err, ErrUnavailable) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer c.Close()
	var panes []Pane
	if typ != fList || json.Unmarshal(b, &panes) != nil {
		return nil, errors.New("daemon: unexpected answer")
	}
	return panes, nil
}

// Stop ends the daemon; with panes running only when force.
func Stop(force bool) error {
	c, _, _, err := request(Hello{Op: "stop", Force: force}, false)
	if err != nil {
		return err
	}
	return c.Close()
}

// Kill ends a pane's atto, as closing its terminal would.
func Kill(target string) error {
	c, _, _, err := request(Hello{Op: "kill", Target: target}, false)
	if err != nil {
		return err
	}
	return c.Close()
}

// Run starts atto in a new pane (op "new", with h's args, cwd and env) or
// attaches to one (op "attach"), and shows it on this terminal until it
// exits or detaches. It returns the exit code to leave with, and a note
// to print after the terminal is restored. An error wrapping
// ErrUnavailable means no daemon could be reached: run atto directly.
func Run(h Hello) (code int, note string, err error) {
	in, out := os.Stdin, os.Stdout
	if w, r, err := term.GetSize(int(out.Fd())); err == nil {
		h.Size = Size{Cols: w, Rows: r}
	}
	c, typ, b, err := request(h, true)
	if err != nil {
		return 1, "", err
	}
	defer c.Close()
	var pane Pane
	if typ != fAttached || json.Unmarshal(b, &pane) != nil {
		return 1, "", errors.New("daemon: unexpected answer")
	}
	old, err := term.MakeRaw(int(in.Fd()))
	if err != nil {
		return 1, "", err
	}
	st := newStream()
	defer func() {
		_, _ = out.WriteString(st.reset())
		_ = term.Restore(int(in.Fd()), old)
	}()

	var wmu sync.Mutex
	send := func(typ byte, v []byte) {
		wmu.Lock()
		defer wmu.Unlock()
		_ = writeFrame(c, typ, v)
	}
	go func() {
		buf := make([]byte, 64<<10)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				send(fInput, buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGWINCH, syscall.SIGHUP, syscall.SIGTERM)
	defer signal.Stop(sigs)
	gone := make(chan struct{})
	defer close(gone)
	go func() {
		for {
			select {
			case <-gone:
				return
			case s := <-sigs:
				if s != syscall.SIGWINCH {
					c.Close() // the terminal is going: leave the pane running
					continue
				}
				if w, r, err := term.GetSize(int(out.Fd())); err == nil {
					b, _ := json.Marshal(Size{Cols: w, Rows: r})
					send(fResize, b)
				}
			}
		}
	}()

	for {
		typ, b, err := readFrame(c)
		if err != nil {
			return 0, fmt.Sprintf("atto pane %d is still running: atto attach %d", pane.ID, pane.ID), nil
		}
		switch typ {
		case fOutput:
			b, _ = st.feed(b)
			_, _ = out.Write(b)
		case fAttached: // moved to another pane
			_ = json.Unmarshal(b, &pane)
		case fExit:
			var x Exit
			_ = json.Unmarshal(b, &x)
			if x.Detached {
				return 0, fmt.Sprintf("detached from atto pane %d: atto attach %d to return", pane.ID, pane.ID), nil
			}
			return x.Code, "", nil
		case fError:
			return 1, "", errors.New(string(b))
		}
	}
}

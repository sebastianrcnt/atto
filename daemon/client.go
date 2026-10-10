package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/shell"
)

// startWait is how long a client waits for a daemon it started.
var startWait = 5 * time.Second

func init() {
	if runtime.GOOS == "windows" {
		startWait = 15 * time.Second // a new process starts slowly under antivirus scans
	}
}

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

// spawn starts "atto _daemon" detached from this terminal (a session of its
// own on Unix, a hidden console of its own on Windows), with its errors
// going to the log. Two clients racing both spawn one; the second finds the
// lock taken and exits.
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
	cmd.Dir = daemonDir()
	cmd.Stderr = log
	shell.Isolate(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// request sends h to the daemon and returns its first answer.
func request(h Hello, start bool) (net.Conn, byte, []byte, error) {
	c, typ, b, err := requestVersion(h, start, Proto)
	// Only stop and diagnostics may downgrade; execution never does.
	if h.Op == "stop" || h.Op == "status" {
		for version := Proto - 1; errors.Is(err, ErrProtocol) && version >= 2; version-- {
			old := h
			if h.Op == "status" {
				if version == 2 {
					old.Op = "list"
				} else {
					old.Op = "workers"
				}
			}
			c, typ, b, err = requestVersion(old, start, version)
		}
	}
	return c, typ, b, err
}

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

// Stop ends the daemon; force also ends its workers and their work.
func Stop(force bool) error {
	c, _, _, err := request(Hello{Op: "stop", Force: force}, false)
	if err != nil {
		return err
	}
	return c.Close()
}

// Kill explicitly closes a worker session, stopping its work.
func Kill(target string) error {
	c, _, _, err := request(Hello{Op: "kill", Target: target}, false)
	if err != nil {
		return err
	}
	return c.Close()
}

// Describe tells which daemon runs: ErrUnavailable when none does; a
// daemon from before the "info" request answers an error.
func Describe() (Info, error) {
	c, typ, b, err := request(Hello{Op: "info"}, false)
	if err != nil {
		return Info{}, err
	}
	defer c.Close()
	var out Info
	if typ != fWorker || json.Unmarshal(b, &out) != nil {
		return Info{}, errors.New("daemon: unexpected answer")
	}
	return out, nil
}

// Status tolerates old daemons for diagnostics only. Old pane listings
// are decoded locally, never made available as attachment targets.
func Status() ([]Worker, error) {
	c, typ, b, err := request(Hello{Op: "status"}, false)
	if errors.Is(err, ErrUnavailable) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer c.Close()
	var out []Worker
	if typ == fList {
		var old []struct {
			Session, Name, State, Cwd string
			Clients                   int
			Started                   time.Time
		}
		if err := json.Unmarshal(b, &old); err != nil {
			return nil, err
		}
		for _, p := range old {
			out = append(out, Worker{Session: p.Session, Name: p.Name, State: p.State, Cwd: p.Cwd, Clients: p.Clients, Started: p.Started, Version: "legacy"})
		}
	} else if typ != fWorker || json.Unmarshal(b, &out) != nil {
		return nil, errors.New("daemon: unexpected answer")
	}
	return out, nil
}

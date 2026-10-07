//go:build !windows

package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
)

// TestMain lets the test binary be the session worker the daemon runs.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "_session-server" {
		if err := RunWorker("test", os.Args[2:]); err != nil {
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}

func startDaemon(t *testing.T, idle time.Duration) chan error {
	t.Helper()
	t.Setenv(config.EnvDir, t.TempDir())
	idleExit = idle
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- Serve(exe) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if c, err := net.Dial("unix", SocketPath()); err == nil {
			c.Close()
			return done
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon did not listen")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDaemonStopAndErrors(t *testing.T) {
	done := startDaemon(t, 5*time.Second)

	// A second daemon for the same atto dir steps aside.
	exe, _ := os.Executable()
	if err := Serve(exe); err != nil {
		t.Fatalf("second daemon: %v", err)
	}
	if _, _, _, err := request(Hello{Op: "attach"}, false); err == nil || !strings.Contains(err.Error(), "unknown request") {
		t.Fatalf("a removed request: %v", err)
	}
	if ws, err := Workers(); err != nil || len(ws) != 0 {
		t.Fatalf("workers %v %v", ws, err)
	}
	if err := Stop(false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not exit")
	}
	if ws, err := Workers(); err != nil || ws != nil {
		t.Fatalf("workers with no daemon: %v %v", ws, err)
	}
}

// With no worker left the daemon exits by itself.
func TestDaemonIdleExit(t *testing.T) {
	done := startDaemon(t, 100*time.Millisecond)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("idle daemon did not exit")
	}
}

func TestProtocolMismatch(t *testing.T) {
	startDaemon(t, 5*time.Second)
	c, err := dial(false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = writeJSON(c, fHello, Hello{Proto: 1, Op: "workers"})
	typ, b, err := readFrame(c)
	if err != nil || typ != fError || !strings.Contains(string(b), "protocol") {
		t.Fatalf("%c %s %v", typ, b, err)
	}
	_ = Stop(true)
}

func TestSocketPathFallsBackWhenLong(t *testing.T) {
	t.Setenv(config.EnvDir, "/tmp/"+strings.Repeat("d", 120))
	p := SocketPath()
	if len(p) > maxSocketPath || !strings.HasPrefix(p, os.TempDir()) {
		t.Fatalf("socket path %q", p)
	}
}

func TestSocketDirectoryRejectsUnsafePaths(t *testing.T) {
	for _, mode := range []os.FileMode{0o755, 0o777} {
		dir := t.TempDir()
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		if err := privateDir(dir); err == nil {
			t.Fatalf("accepted mode %o", mode)
		}
	}
	root := t.TempDir()
	link := root + "/link"
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if err := privateDir(link); err == nil {
		t.Fatal("accepted a symlink")
	}
	dir := root + "/private"
	if err := privateDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := privateDir(dir); err != nil {
		t.Fatal(err)
	}
}

func TestDialRejectsUnsafeDirectoryBeforeHello(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	if err := os.MkdirAll(RunDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(RunDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if c, err := dial(false); err == nil {
		c.Close()
		t.Fatal("accepted unsafe socket directory")
	}
}

func TestPeerUID(t *testing.T) {
	if err := peerUID(uint32(os.Getuid())); err != nil {
		t.Fatal(err)
	}
	if err := peerUID(uint32(os.Getuid()) + 1); err == nil {
		t.Fatal("accepted another user's daemon")
	}
}

func TestClientRejectsLegacyDaemon(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	if err := privateDir(RunDir()); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer c.Close()
		typ, b, err := readFrame(c)
		var h Hello
		if err != nil || typ != fHello || json.Unmarshal(b, &h) != nil || h.Proto == 1 {
			done <- fmt.Errorf("new client sent legacy Hello: %c %s %v", typ, b, err)
			return
		}
		done <- writeFrame(c, fError, []byte("the running atto daemon speaks protocol 1, this atto 2: end its panes, then run atto daemon stop"))
	}()
	c, _, _, err := request(Hello{Op: "workers"}, false)
	if c != nil {
		c.Close()
		t.Fatal("connected to a legacy daemon")
	}
	// The mismatch response is actionable, not an unavailable daemon
	// error a client would silently fall back from.
	if err == nil || errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "protocol 1") {
		t.Fatalf("legacy daemon error: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

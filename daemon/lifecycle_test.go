package daemon

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/provider/providertest"
	"github.com/sebastianrcnt/atto/shell"
)

func waitFor(t *testing.T, what string, within time.Duration, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(within); !ok(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// The daemon lock is what tells a live daemon from a dead one.
func TestDaemonLockExcludesAnotherHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	open := func() *os.File {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		return f
	}
	a, b := open(), open()
	if !tryLock(a) {
		t.Fatal("a free lock was refused")
	}
	if tryLock(b) {
		t.Fatal("a held lock was granted twice")
	}
	a.Close()
	// Releasing a closed handle's lock is the system's to do, and may lag.
	waitFor(t, "the released lock", 5*time.Second, func() bool { return tryLock(b) })
}

// Clients start the daemon on demand, detached; it serves them, lists its
// workers, and ends on request together with them.
func TestDaemonStartsOnDemandAndStops(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	m := providertest.New(t, providertest.Reply{Text: "ok"})
	m.Install(t, config.Dir())
	t.Cleanup(func() { _ = Stop(true) })

	if ws, err := Workers(); err != nil || len(ws) != 0 {
		t.Fatalf("no daemon should mean no workers: %v %v", ws, err)
	}
	if _, err := os.Stat(SocketPath()); err == nil {
		t.Fatal("asking for workers must not start a daemon")
	}
	w, readOnly, err := StartWorker("", t.TempDir(), nil) // starts the daemon too
	if err != nil || readOnly != "" || w.Session == "" || w.PID <= 0 {
		t.Fatalf("start: %+v %q %v", w, readOnly, err)
	}
	if !shell.Alive(w.PID) {
		t.Fatal("the worker is not running")
	}
	status, err := Status()
	if err != nil || len(status) != 1 || status[0].Session != w.Session || status[0].PID != w.PID {
		t.Fatalf("status %+v %v", status, err)
	}

	if err := Stop(false); err == nil {
		t.Fatal("stop must refuse while a session runs")
	}
	if err := Stop(true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the worker to end", 10*time.Second, func() bool { return !shell.Alive(w.PID) })
	waitFor(t, "the daemon to end", 10*time.Second, func() bool {
		ws, err := Workers()
		return err == nil && len(ws) == 0
	})
	if _, err := os.Stat(w.Socket); !os.IsNotExist(err) {
		t.Fatalf("the worker's socket is left: %v", err)
	}
	if err := Stop(false); err == nil {
		t.Fatal("stopped a daemon that is gone")
	}
}

// stop -force asks every worker to close its session and end.
func TestStopForceEndsWorkers(t *testing.T) {
	done := startDaemon(t, 5*time.Second)
	m := providertest.New(t, providertest.Reply{Text: "ok"})
	m.Install(t, config.Dir())
	w, _, err := StartWorker("", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := Stop(true); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the daemon did not end")
	}
	waitFor(t, "the worker to end", 10*time.Second, func() bool { return !shell.Alive(w.PID) })
}

// A dead daemon's socket does not keep another from starting.
func TestServeReplacesDeadDaemonsSocket(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	if err := privateDir(RunDir()); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	if _, err := os.Lstat(SocketPath()); err != nil {
		t.Fatalf("no stale socket to replace: %v", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- Serve(exe) }()
	waitFor(t, "the new daemon", 10*time.Second, func() bool {
		c, err := trustedDial(SocketPath())
		if err == nil {
			c.Close()
		}
		return err == nil
	})
	if err := Stop(true); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

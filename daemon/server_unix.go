//go:build !windows

package daemon

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// idleExit is how long the daemon waits, with no worker left, for a
// client before it exits.
var idleExit = 2 * time.Second

// writeWait bounds writing an answer to a client.
const writeWait = 5 * time.Second

// Serve runs the daemon in this process until its last worker ends (or
// stop). exe is the atto binary the workers run. It returns at once, with
// no error, when another daemon already serves this atto dir.
func Serve(exe string) error {
	if err := privateDir(RunDir()); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(RunDir(), "daemon.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil // another daemon has it
	}
	sock := SocketPath()
	if err := privateDir(filepath.Dir(sock)); err != nil {
		return err
	}
	_ = os.Remove(sock) // a dead daemon's: we hold the lock
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	_ = os.Chmod(sock, 0o600)
	d := &daemon{exe: exe, ln: ln, workers: map[string]*worker{}, idleAfter: idleExit}
	d.idle = time.AfterFunc(d.idleAfter, func() { ln.Close() })
	defer os.Remove(sock)
	for {
		c, err := ln.Accept()
		if err != nil {
			return nil
		}
		go d.serve(c)
	}
}

type daemon struct {
	exe       string
	ln        net.Listener
	mu        sync.Mutex
	workers   map[string]*worker // by session
	wmu       sync.Mutex         // serializes worker starts
	idle      *time.Timer        // ends the daemon when it fires with no worker
	idleAfter time.Duration
}

// serve answers one client: its Hello, then one frame.
func (d *daemon) serve(conn net.Conn) {
	defer conn.Close()
	typ, b, err := readFrame(conn)
	var h Hello
	if err != nil || typ != fHello || json.Unmarshal(b, &h) != nil {
		return
	}
	answer := func(typ byte, v any) {
		_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
		_ = writeJSON(conn, typ, v)
	}
	fail := func(format string, args ...any) {
		_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
		_ = writeFrame(conn, fError, fmt.Appendf(nil, format, args...))
	}
	if h.Proto != Proto {
		fail("the running atto daemon speaks protocol %d, this atto %d: run atto daemon stop -force (it ends the sessions it runs)", Proto, h.Proto)
		return
	}
	switch h.Op {
	case "stop":
		if w := len(d.workerList()); w > 0 && !h.Force {
			fail("%d session(s) running; atto daemon stop -force ends them (and their sessions' work)", w)
			return
		}
		d.stopWorkers()
		answer(fExit, struct{}{})
		d.ln.Close()
	case "worker":
		answer(fWorker, d.startWorker(h))
	case "workers":
		answer(fWorker, d.workerList())
	default:
		fail("unknown request %q", h.Op)
	}
}

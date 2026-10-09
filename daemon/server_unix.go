//go:build !windows

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sebastianrcnt/atto/server"
	"golang.org/x/sys/unix"
)

var idleExit = 2 * time.Second

const writeWait = 5 * time.Second

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
	cleanWorkerSockets()
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
			d.stopWorkers()
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
	stopping  bool
	wmu       sync.Mutex  // serializes worker starts
	idle      *time.Timer // ends the daemon when it fires with no worker
	idleAfter time.Duration
}

// serve handles one bounded control request. Terminal rendering is never
// transported through the daemon.
func (d *daemon) serve(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(workerStartWait + writeWait))
	typ, b, err := readFrame(conn)
	var h Hello
	if err != nil || typ != fHello || json.Unmarshal(b, &h) != nil {
		return
	}
	fail := func(format string, args ...any) { _ = writeFrame(conn, fError, fmt.Appendf(nil, format, args...)) }
	if h.Proto != Proto {
		fail("the running atto daemon speaks protocol %d, this atto %d: run atto daemon stop -force, then try again", Proto, h.Proto)
		return
	}
	switch h.Op {
	case "stop":
		if n := len(d.workerList()); n > 0 && !h.Force {
			fail("%d session(s) running; atto daemon stop -force ends them and their work", n)
			return
		}
		d.stopWorkers()
		_ = writeJSON(conn, fExit, Exit{})
		d.ln.Close()
	case "worker":
		_ = writeJSON(conn, fWorker, d.startWorker(h))
	case "workers", "status":
		_ = writeJSON(conn, fWorker, d.workerList())
	case "kill":
		var found *Worker
		for _, w := range d.workerList() {
			if w.Session == h.Target {
				found = &w
				break
			}
			if strings.HasPrefix(w.Session, h.Target) {
				if found != nil {
					fail("ambiguous session %q", h.Target)
					return
				}
				found = &w
			}
		}
		if found == nil || h.Target == "" {
			fail("no worker for session %q (atto daemon status lists them)", h.Target)
			return
		}
		nc, err := DialWorker(*found)
		if err != nil {
			fail("%v", err)
			return
		}
		c := server.NewClient(nc)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = c.Call(ctx, "thread/close", map[string]any{"threadId": found.Session, "reason": "close"}, nil)
		cancel()
		c.Close()
		if err != nil {
			fail("%v", err)
			return
		}
		_ = writeJSON(conn, fExit, Exit{})
	default:
		fail("unknown request %q", h.Op)
	}
}

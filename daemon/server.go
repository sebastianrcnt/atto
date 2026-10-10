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
)

var idleExit = 2 * time.Second

const writeWait = 5 * time.Second

// Serve runs the daemon of this ATTO_DIR; exe is the atto it starts
// workers from and version its own build.
func Serve(exe, version string) error {
	if err := privateDir(RunDir()); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(RunDir(), "daemon.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if !tryLock(lock) {
		return nil // another daemon has it
	}
	sock := SocketPath()
	if err := privateDir(filepath.Dir(sock)); err != nil {
		return err
	}
	cleanWorkerSockets()
	removeSocket(sock) // a dead daemon's: we hold the lock
	ln, err := listenSocket(sock)
	if err != nil {
		return err
	}
	d := &daemon{exe: exe, version: version, started: time.Now(), ln: ln, workers: map[string]*worker{}, idleAfter: idleExit}
	d.mu.Lock()
	d.idle = time.AfterFunc(d.idleAfter, d.idleExit)
	d.mu.Unlock()
	d.adoptWorkers() // those an older daemon handed over
	handedOver := false
	defer func() {
		if !handedOver { // the path is the successor's then
			removeSocket(sock)
		}
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if d.handingOver() {
				// The workers go on; the daemon of the build on disk
				// takes them over (adoptWorkers).
				d.waitServing(3 * time.Second)
				removeSocket(sock)
				handedOver = true
				lock.Close()
				if err := spawn(d.exe); err != nil {
					logf("daemon %s: starting the daemon of %s: %v", d.version, d.exe, err)
				}
				return nil
			}
			d.stopWorkers()
			return nil
		}
		d.serving.Go(func() { d.serve(c) })
	}
}

type daemon struct {
	exe       string
	version   string // this daemon's build
	started   time.Time
	ln        net.Listener
	mu        sync.Mutex
	workers   map[string]*worker // by session
	stopping  bool
	handover  bool        // stopping to hand the workers over (handover.go)
	starting  int         // worker starts in progress
	wmu       sync.Mutex  // serializes worker starts
	idle      *time.Timer // ends the daemon when it fires with no worker
	idleAfter time.Duration
	serving   sync.WaitGroup // control requests being answered
}

// idleExit ends the daemon unless a worker runs or starts.
func (d *daemon) idleExit() {
	d.mu.Lock()
	busy := len(d.workers) > 0 || d.starting > 0
	d.mu.Unlock()
	if !busy {
		d.ln.Close()
	}
}

// idleCheck (d.mu held) arms the idle exit once nothing runs.
func (d *daemon) idleCheck() {
	if len(d.workers) == 0 && d.starting == 0 {
		d.idle.Reset(d.idleAfter)
	}
}

// waitServing waits up to max for the control requests being answered.
func (d *daemon) waitServing(max time.Duration) {
	done := make(chan struct{})
	go func() { d.serving.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(max):
	}
}

// Info describes the running daemon (the "info" request).
type Info struct {
	Version string    `json:"version"`
	PID     int       `json:"pid"`
	Exe     string    `json:"exe"`
	Started time.Time `json:"started"`
}

// serve handles one bounded control request. Terminal rendering is never
// transported through the daemon.
func (d *daemon) serve(conn net.Conn) {
	defer conn.Close()
	if authConn(conn) != nil {
		return
	}
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
		conn.Close()
		d.maybeHandOver()
	case "workers", "status":
		_ = writeJSON(conn, fWorker, d.workerList())
	case "info":
		_ = writeJSON(conn, fWorker, Info{Version: d.version, PID: os.Getpid(), Exe: d.exe, Started: d.started})
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

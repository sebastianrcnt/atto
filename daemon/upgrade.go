package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/shell"
)

// Updating atto replaces the binary on disk, not the processes running
// the old one. The daemon keeps both kinds current:
//
//   - A worker runs one build for its whole life. When a client asks for
//     the worker of a session whose build differs from the executable on
//     disk (the one the daemon starts workers from, asked with -version),
//     and the worker is idle, the daemon has it close (worker/retire, an
//     idle check and the close in one step on the worker's lane, so input
//     arriving meanwhile is refused, never lost) and starts the session
//     again from the binary on disk. A busy worker is left alone and
//     replaced at a later request once idle. Clients attached to it get
//     thread/closed with reason "upgrade" and reconnect; the worker
//     refuses while one is attached that does not declare it does
//     (capabilities.reattach).
//   - The daemon itself only supervises: workers are processes of their
//     own. A daemon whose build differs from the binary on disk hands
//     over after answering a worker request: it writes its registry to
//     handover.json, stops listening and starts the daemon of the binary
//     on disk, which adopts the workers. It does so only when every
//     worker says it outlives its daemon (Worker.Replaceable); workers of
//     builds before this one would die writing to the old daemon's pipe.

// versionCache remembers the build of the executable on disk by its size
// and modification time: asking it runs a process.
var versionCache struct {
	sync.Mutex
	exe     string
	size    int64
	mod     time.Time
	version string
}

// diskVersion is the build of the atto at exe ("" when it cannot be told).
func diskVersion(exe string) string {
	st, err := os.Stat(exe)
	if err != nil {
		return ""
	}
	c := &versionCache
	c.Lock()
	defer c.Unlock()
	if c.exe == exe && c.size == st.Size() && c.mod.Equal(st.ModTime()) {
		return c.version
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-version")
	cmd.Dir = daemonDir()
	shell.Isolate(cmd)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	// "atto v0.1.0 (slim) (edge)": the version is the second word.
	f := strings.Fields(string(out))
	if len(f) < 2 || f[0] != "atto" {
		return ""
	}
	c.exe, c.size, c.mod, c.version = exe, st.Size(), st.ModTime(), f[1]
	return c.version
}

// logf adds a line to the daemon log.
func logf(format string, args ...any) {
	if err := os.MkdirAll(filepath.Dir(LogPath()), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s atto daemon: %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

// callWorker makes one request of the worker w.
func callWorker(w Worker, timeout time.Duration, method string, params, result any) error {
	nc, err := DialWorker(w)
	if err != nil {
		return err
	}
	c := server.NewClient(nc)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := c.Call(ctx, "initialize", map[string]any{"protocolVersions": []int{server.ProtocolVersion}, "clientInfo": server.ClientInfo{Name: "atto-daemon"}}, nil); err != nil {
		return err
	}
	return c.Call(ctx, method, params, result)
}

// retireWait bounds how long a retired worker may take to exit.
var retireWait = 15 * time.Second

// current is the answer for a request that found worker w (wmu held): w,
// or, when w runs another build than the binary on disk and is idle, a
// new worker of the session started from that binary.
func (d *daemon) current(w *worker, h Hello) workerAnswer {
	want := diskVersion(d.exe)
	if want == "" {
		return workerAnswer{Worker: w.info}
	}
	var st Worker
	if err := callWorker(w.info, 2*time.Second, "worker/state", map[string]any{"threadId": w.info.Session}, &st); err != nil || st.Version == "" || st.Version == want {
		return workerAnswer{Worker: w.info}
	}
	keep := func(why string) workerAnswer {
		if w.kept != why {
			w.kept = why
			logf("kept the worker of session %s (pid %d, %s; on disk %s) for now: %s", w.info.Session, w.info.PID, st.Version, want, why)
		}
		return workerAnswer{Worker: w.info}
	}
	if !st.Replaceable {
		return keep("its build cannot be replaced while it runs (close the session to update it)")
	}
	var res struct {
		Retired bool   `json:"retired"`
		Reason  string `json:"reason"`
	}
	if err := callWorker(w.info, 30*time.Second, "worker/retire", map[string]any{"threadId": w.info.Session, "reason": "upgrade"}, &res); err != nil {
		return keep(err.Error())
	}
	if !res.Retired {
		return keep(res.Reason)
	}
	select {
	case <-w.done:
	case <-time.After(retireWait):
		killWorker(w)
		select {
		case <-w.done:
		case <-time.After(5 * time.Second):
			return workerAnswer{Error: "the session's old runtime did not exit (see " + LogPath() + ")"}
		}
	}
	h.Target = w.info.Session
	h.Cwd = w.info.Cwd
	if st.Cwd != "" {
		h.Cwd = st.Cwd
	}
	a := d.launch(h)
	if a.Error != "" {
		logf("retired the worker of session %s (pid %d, %s), but starting %s failed: %s", w.info.Session, w.info.PID, st.Version, want, a.Error)
		return a
	}
	logf("replaced the worker of session %s: pid %d (%s) by pid %d (%s)", w.info.Session, w.info.PID, st.Version, a.PID, want)
	return a
}

// noHandover lets in-process test daemons keep their workers.
var noHandover atomic.Bool

func handoverPath() string { return filepath.Join(RunDir(), "handover.json") }

func (d *daemon) handingOver() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.handover
}

// maybeHandOver hands the workers over to the daemon of the binary on
// disk when that is another build and every worker can outlive this
// daemon. Serve then starts it.
func (d *daemon) maybeHandOver() {
	if noHandover.Load() || d.version == "" {
		return
	}
	want := diskVersion(d.exe)
	if want == "" || want == d.version {
		return
	}
	d.wmu.Lock() // no start in progress
	defer d.wmu.Unlock()
	d.mu.Lock()
	stopping := d.stopping
	d.mu.Unlock()
	if stopping {
		return
	}
	workers := d.workerList()
	for _, w := range workers {
		if !w.Replaceable {
			return // kept until its workers are gone
		}
	}
	b, err := json.Marshal(workers)
	if err == nil {
		err = os.WriteFile(handoverPath(), b, 0o600)
	}
	if err != nil {
		logf("daemon %s: cannot hand over to %s: %v", d.version, want, err)
		return
	}
	d.mu.Lock()
	d.stopping, d.handover = true, true
	d.mu.Unlock()
	logf("daemon %s (pid %d) hands %d worker(s) over to the daemon of %s", d.version, os.Getpid(), len(workers), want)
	d.ln.Close()
}

// adoptWorkers takes over the workers an older daemon handed over, those
// still running.
func (d *daemon) adoptWorkers() {
	b, err := os.ReadFile(handoverPath())
	if err != nil {
		return
	}
	_ = os.Remove(handoverPath())
	var workers []Worker
	if json.Unmarshal(b, &workers) != nil {
		return
	}
	n := 0
	for _, info := range workers {
		nc, err := DialWorker(info)
		if err != nil {
			continue
		}
		c := server.NewClient(nc)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var st Worker
		err = c.Call(ctx, "initialize", map[string]any{"protocolVersions": []int{server.ProtocolVersion}, "clientInfo": server.ClientInfo{Name: "atto-daemon"}}, nil)
		if err == nil {
			err = c.Call(ctx, "worker/state", map[string]any{"threadId": info.Session}, &st)
		}
		cancel()
		if err != nil || st.Session != info.Session {
			c.Close()
			continue
		}
		w := &worker{info: Worker{Session: info.Session, Socket: info.Socket, PID: info.PID, Cwd: info.Cwd, Started: info.Started}, conn: c, done: make(chan struct{})}
		d.mu.Lock()
		if d.workers[w.info.Session] != nil {
			d.mu.Unlock()
			c.Close()
			continue
		}
		d.workers[w.info.Session] = w
		d.idle.Stop()
		d.mu.Unlock()
		n++
		go func() {
			// The worker's end closes the connection held to it.
			<-c.Done()
			d.forget(w)
		}()
	}
	if n > 0 {
		logf("daemon %s (pid %d) adopted %d worker(s)", d.version, os.Getpid(), n)
	}
}

// closeAdopted asks an adopted worker to end as on a daemon stop.
func closeAdopted(w *worker) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = w.conn.Call(ctx, "thread/close", map[string]any{"threadId": w.info.Session, "reason": "other"}, nil)
}

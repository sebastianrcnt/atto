package daemon

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/shell"
)

// The daemon runs one worker per session
// a terminal shows: "atto _session-server", the session's runtime
// (package server) behind a Unix socket of its own. Each TUI is a
// client of the worker, so closing the terminal, or the TUI crashing,
// ends a view and never the work; any number of terminals can show the
// same session, each with its own editor. The daemon finds or starts the
// worker of a session (one per session: it holds the session's writer
// lease) and forgets it when it exits. A worker exits when its session
// closes: explicitly, or once it has been idle with no client for the
// retention period (one minute).

// workerStartWait bounds how long the daemon waits for a worker to say it
// is ready.
const workerStartWait = 20 * time.Second

// worker is a running session worker, in the daemon.
type worker struct {
	info  Worker
	cmd   *exec.Cmd // nil for a worker adopted from an older daemon
	done  chan struct{}
	stdin io.WriteCloser // where the daemon asks a worker to stop, where signals cannot
	// conn, for an adopted worker, is the connection the daemon holds to
	// it: its end is the worker's.
	conn *server.Client
	kept string // why the worker of an older build was last kept (logged once)
}

// workerReq is the "worker" request's answer.
type workerAnswer struct {
	Worker
	Error    string `json:"error,omitempty"`
	ReadOnly string `json:"readOnly,omitempty"`
	// Retry: the daemon is handing over to a newer one; ask again.
	Retry bool `json:"retry,omitempty"`
}

// startWorker finds or starts the worker for h: Target names a session to
// resume ("" starts a new one in h.Cwd; h.Args may carry -model and
// -effort).
func (d *daemon) startWorker(h Hello) workerAnswer {
	d.wmu.Lock() // one start at a time: repeated starts converge
	defer d.wmu.Unlock()
	d.mu.Lock()
	stopping, handover := d.stopping, d.handover
	if !stopping {
		d.starting++
		d.idle.Stop()
	}
	d.mu.Unlock()
	if stopping {
		return workerAnswer{Error: "the daemon is stopping", Retry: handover}
	}
	defer func() {
		d.mu.Lock()
		d.starting--
		d.idleCheck()
		d.mu.Unlock()
	}()
	if h.Target != "" {
		d.mu.Lock()
		exact := d.workers[h.Target]
		var live []*worker
		for id, w := range d.workers {
			if strings.HasPrefix(id, h.Target) {
				live = append(live, w)
			}
		}
		d.mu.Unlock()
		if exact != nil {
			return d.current(exact, h)
		}
		path, err := session.Find(h.Target)
		if err != nil {
			if strings.Contains(err.Error(), "ambiguous") {
				return workerAnswer{Error: err.Error()}
			}
			if len(live) == 1 {
				return d.current(live[0], h)
			}
			if len(live) == 0 {
				return workerAnswer{Error: err.Error()}
			}
		}
		var saved session.Summary
		if err == nil {
			saved, err = session.Summarize(path)
			if err != nil {
				return workerAnswer{Error: err.Error()}
			}
		}
		ids := map[string]bool{}
		if saved.ID != "" {
			ids[saved.ID] = true
		}
		for _, w := range live {
			ids[w.info.Session] = true
		}
		if len(ids) > 1 {
			var candidates []string
			for id := range ids {
				candidates = append(candidates, id)
			}
			slices.Sort(candidates)
			return workerAnswer{Error: fmt.Sprintf("session %q is ambiguous: matches %s", h.Target, strings.Join(candidates, ", "))}
		}
		d.mu.Lock()
		found := d.workers[saved.ID]
		d.mu.Unlock()
		if found != nil {
			return d.current(found, h)
		}
		h.Target = saved.ID
		if saved.Cwd != "" {
			h.Cwd = saved.Cwd
		}
	}
	return d.launch(h)
}

// launch starts a worker for h (wmu held): Target is the session to
// resume, "" a new one.
func (d *daemon) launch(h Hello) workerAnswer {
	sock, err := workerSocket()
	if err != nil {
		return workerAnswer{Error: err.Error()}
	}
	args := []string{"_session-server", "-socket", sock}
	if h.Target != "" {
		args = append(args, "-session", h.Target)
	}
	// Test/deployment override: normal workers retain the one-minute default.
	if retention := os.Getenv("ATTO_WORKER_RETENTION"); retention != "" {
		args = append(args, "-retention", retention)
	}
	args = append(args, h.Args...)
	cmd := exec.Command(d.exe, args...)
	cmd.Dir = h.Cwd
	cmd.Env = slices.Clone(h.Env)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return workerAnswer{Error: err.Error()}
	}
	stdin, err := workerStdin(cmd)
	if err != nil {
		return workerAnswer{Error: err.Error()}
	}
	cmd.Stderr = nil
	if log, err := os.OpenFile(LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		cmd.Stderr = log
		defer log.Close()
	}
	shell.Isolate(cmd) // a session (Unix) or hidden console (Windows) of its own
	if err := cmd.Start(); err != nil {
		return workerAnswer{Error: err.Error()}
	}
	line := make(chan string, 1)
	go func() {
		s, _ := bufio.NewReader(out).ReadString('\n')
		line <- strings.TrimSpace(s)
		_, _ = io.Copy(io.Discard, out)
	}()
	var first string
	select {
	case first = <-line:
	case <-time.After(workerStartWait):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		removeSocket(sock)
		return workerAnswer{Error: "the session's runtime did not start (see " + LogPath() + ")"}
	}
	verb, rest, _ := strings.Cut(first, " ")
	switch verb {
	case "ready":
	case "readonly":
		_ = cmd.Wait()
		removeSocket(sock)
		return workerAnswer{ReadOnly: rest}
	default:
		_ = cmd.Wait()
		removeSocket(sock)
		if rest == "" {
			rest = "the session's runtime ended at once (see " + LogPath() + ")"
		}
		return workerAnswer{Error: rest}
	}
	w := &worker{cmd: cmd, stdin: stdin, done: make(chan struct{}), info: Worker{Session: rest, Socket: sock, PID: cmd.Process.Pid, Cwd: h.Cwd, Started: time.Now()}}
	d.mu.Lock()
	d.workers[w.info.Session] = w
	d.idle.Stop()
	d.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		removeSocket(sock)
		d.forget(w)
	}()
	return workerAnswer{Worker: w.info}
}

// forget drops worker w, which ended, from the registry.
func (d *daemon) forget(w *worker) {
	d.mu.Lock()
	if d.workers[w.info.Session] == w {
		delete(d.workers, w.info.Session)
	}
	d.idleCheck()
	d.mu.Unlock()
	close(w.done)
}

func (d *daemon) workerList() []Worker {
	d.mu.Lock()
	var out []Worker
	for _, w := range d.workers {
		out = append(out, w.info)
	}
	d.mu.Unlock()
	for i := range out {
		nc, err := DialWorker(out[i])
		if err != nil {
			continue
		}
		c := server.NewClient(nc)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		var state Worker
		err = c.Call(ctx, "worker/state", map[string]any{"threadId": out[i].Session}, &state)
		cancel()
		c.Close()
		if err == nil {
			out[i].ID, out[i].Version, out[i].Clients, out[i].Busy = state.ID, state.Version, state.Clients, state.Busy
			if state.Cwd != "" {
				out[i].Cwd = state.Cwd
			}
			out[i].Name, out[i].State = state.Name, state.State
			out[i].OpenPrompt, out[i].GoalWaiting = state.OpenPrompt, state.GoalWaiting
			out[i].Replaceable = state.Replaceable
		}
	}
	slices.SortFunc(out, func(a, b Worker) int { return a.Started.Compare(b.Started) })
	return out
}

// stopWorkers ends every worker (daemon stop -force): their sessions
// close as on exit.
func (d *daemon) stopWorkers() {
	d.wmu.Lock()
	defer d.wmu.Unlock()
	d.mu.Lock()
	d.stopping = true
	var workers []*worker
	for _, w := range d.workers {
		workers = append(workers, w)
	}
	d.mu.Unlock()
	for _, w := range workers {
		if w.cmd == nil {
			go closeAdopted(w)
		} else {
			stopWorker(w)
		}
	}
	deadline := time.After(5 * time.Second)
	for _, w := range workers {
		select {
		case <-w.done:
		case <-deadline:
			for _, w := range workers {
				killWorker(w)
			}
			return
		}
	}
}

// killWorker ends worker w at once.
func killWorker(w *worker) {
	if w.cmd != nil {
		_ = w.cmd.Process.Kill()
		return
	}
	if p, err := os.FindProcess(w.info.PID); err == nil {
		_ = p.Kill()
	}
}

// workerSocket is a new socket path beside the daemon's, or in the
// private temp directory when that is too long for a socket.
func workerSocket() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	name := "w-" + hex.EncodeToString(b) + ".sock"
	dir, prefix := workerSocketLocation()
	p := filepath.Join(dir, prefix+name)
	if len(p) > maxSocketPath {
		h := sha256.Sum256([]byte(config.Dir()))
		p = filepath.Join(tempSocketBase(), privateTempName(), fmt.Sprintf("%x-%s", h[:4], name))
	}
	if err := privateDir(filepath.Dir(p)); err != nil {
		return "", err
	}
	return p, nil
}

// workerSocketLocation separates each ATTO_DIR's sockets even when a deep
// directory makes them share the private temporary socket directory.
func workerSocketLocation() (string, string) {
	dir := filepath.Dir(SocketPath())
	if dir == RunDir() {
		return dir, ""
	}
	h := sha256.Sum256([]byte(config.Dir()))
	return dir, fmt.Sprintf("%x-", h[:4])
}

// workerSocketLocations are all the places workerSocket puts sockets of this
// ATTO_DIR: beside the daemon's, and in the private temp directory, which a
// worker's longer path may need although the daemon's own fits.
func workerSocketLocations() [][2]string {
	dir, prefix := workerSocketLocation()
	h := sha256.Sum256([]byte(config.Dir()))
	temp := [2]string{filepath.Join(tempSocketBase(), privateTempName()), fmt.Sprintf("%x-", h[:4])}
	if temp[0] == dir {
		return [][2]string{{dir, prefix}}
	}
	return [][2]string{{dir, prefix}, temp}
}

func cleanWorkerSockets() {
	for _, loc := range workerSocketLocations() {
		dir, prefix := loc[0], loc[1]
		paths, _ := filepath.Glob(filepath.Join(dir, prefix+"w-*.sock"))
		for _, path := range paths {
			st, err := os.Lstat(path)
			if err != nil || st.Mode()&os.ModeSocket == 0 {
				continue
			}
			c, err := trustedDial(path)
			if err == nil {
				c.Close()
				continue
			}
			if staleSocket(err) {
				removeSocket(path)
			}
		}
		cleanOrphanTokens(dir, prefix)
	}
}

// StartWorker asks the daemon (started if need be) for the worker of
// session id ("" starts a new session in cwd; args may carry -model and
// -effort). A session another process writes comes back as readOnly (no
// worker).
func StartWorker(id, cwd string, args []string) (w Worker, readOnly string, err error) {
	// A daemon handing over to a newer one answers Retry, or goes while
	// answering: the next request reaches (or starts) its successor.
	for attempt := 0; ; attempt++ {
		var retry bool
		w, readOnly, retry, err = startWorker(id, cwd, args)
		if !retry || attempt >= 50 {
			return w, readOnly, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func startWorker(id, cwd string, args []string) (Worker, string, bool, error) {
	c, typ, b, err := request(Hello{Op: "worker", Target: id, Cwd: cwd, Env: os.Environ(), Args: args}, true)
	if err != nil {
		// Only a connection lost before any answer can be the daemon
		// handing over (asking again finds the same worker anyway).
		return Worker{}, "", errors.Is(err, ErrUnavailable) && strings.Contains(err.Error(), "EOF"), err
	}
	defer c.Close()
	var a workerAnswer
	if typ != fWorker || json.Unmarshal(b, &a) != nil {
		return Worker{}, "", false, errors.New("daemon: unexpected answer")
	}
	if a.Error != "" {
		return Worker{}, "", a.Retry, errors.New(a.Error)
	}
	return a.Worker, a.ReadOnly, false, nil
}

// Workers lists the daemon's session workers; none when no daemon runs.
func Workers() ([]Worker, error) {
	c, typ, b, err := request(Hello{Op: "workers"}, false)
	if errors.Is(err, ErrUnavailable) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer c.Close()
	var out []Worker
	if typ != fWorker || json.Unmarshal(b, &out) != nil {
		return nil, errors.New("daemon: unexpected answer")
	}
	return out, nil
}

// DialWorker connects to a worker's socket, checking it runs as this user.
func DialWorker(w Worker) (net.Conn, error) { return trustedDial(w.Socket) }

// --- the worker process ---

// RunWorker is "atto _session-server": the runtime of one session behind
// a Unix socket. It says "ready <session>" on stdout once it holds the
// session ("readonly <why>" or "error <why>" and exits otherwise), serves
// clients until the session closes, and exits then.
func RunWorker(version string, args []string) error {
	fs := flag.NewFlagSet("_session-server", flag.ContinueOnError)
	sock := fs.String("socket", "", "the socket to listen on")
	id := fs.String("session", "", "the session to resume (none: a new one)")
	model := fs.String("model", "", "the model of a new session")
	effort := fs.String("effort", "", "the effort of a new session")
	deferStart := fs.Bool("defer-start", false, "wait for the terminal project trust decision")
	retention := fs.Duration("retention", server.DefaultSessionRetention, "unattended idle grace period")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *sock == "" {
		return errors.New("-socket is required")
	}
	if *retention < 0 {
		return errors.New("retention must not be negative")
	}
	if err := privateDir(filepath.Dir(*sock)); err != nil {
		return err
	}
	if busy, err := socketBusy(*sock); err != nil {
		return err
	} else if busy {
		return errors.New("the worker socket is already in use")
	}
	defer removeSocket(*sock)
	fail := func(verb string, err error) error {
		fmt.Printf("%s %s\n", verb, strings.ReplaceAll(err.Error(), "\n", " "))
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fail("error", err)
	}
	if err := config.Ensure(); err != nil {
		return fail("error", err)
	}
	srv := server.New(version, cwd)
	srv.LockKind = session.KindTUI
	srv.AgentTurns = true // the sessions of agents run their turns here
	srv.Retire, srv.Retention = true, *retention
	closed := make(chan struct{})
	var once sync.Once
	srv.OnThreadClosed = func(string) { once.Do(func() { close(closed) }) }
	ctx := context.Background()

	var info server.ThreadInfo
	method, params := "thread/start", map[string]any{"cwd": cwd, "model": *model, "effort": *effort, "deferStart": *deferStart}
	if *id != "" {
		method, params = "thread/resume", map[string]any{"threadId": *id, "cwd": cwd, "deferStart": *deferStart}
	}
	if err := callServer(ctx, srv, method, params, &info); err != nil {
		srv.Close()
		return fail("error", err)
	}
	if info.ReadOnly != "" || info.Offline {
		srv.Close()
		return fail("readonly", errors.New(info.ReadOnly))
	}
	removeSocket(*sock)
	ln, err := listenSocket(*sock)
	if err != nil {
		srv.Close()
		return fail("error", err)
	}
	fmt.Printf("ready %s\n", info.ID)
	// Nothing more goes to the daemon's pipe: a worker outlives the
	// daemon that started it when that hands over to a newer one, and a
	// write to a pipe nobody reads would end it.
	detachStdout()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				if authConn(c) != nil {
					c.Close()
					return
				}
				_ = srv.ServeConn(ctx, c)
			}()
		}
	}()
	stop, stopped := stopRequests()
	defer stopped()
	select {
	case <-closed:
		// The session closed while answering thread/close: let that
		// answer reach the client before the connections go.
		time.Sleep(250 * time.Millisecond)
	case <-stop:
	}
	ln.Close()
	srv.CloseWith("other")
	return nil
}

// callServer makes one request of srv from inside the worker (no client).
func callServer(ctx context.Context, srv *server.Server, method string, params, result any) error {
	c := server.Connect(ctx, srv)
	defer c.Close()
	if err := c.Call(ctx, "initialize", map[string]any{"protocolVersions": []int{3}}, nil); err != nil {
		return err
	}
	return c.Call(ctx, method, params, result)
}

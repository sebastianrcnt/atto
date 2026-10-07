//go:build !windows

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
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/session"
)

// Session workers. Besides panes, the daemon runs one worker per session
// a terminal shows: "atto _session-server", the session's runtime
// (package server) behind a Unix socket of its own. A pane's TUI is a
// client of the worker, so closing the terminal, or the TUI crashing,
// ends a view and never the work; any number of terminals can show the
// same session, each with its own editor. The daemon finds or starts the
// worker of a session (one per session: it holds the session's writer
// lease) and forgets it when it exits. A worker exits when its session
// closes: explicitly, or once it has been idle with no client for the
// retention period (settings.json sessionRetention).

// workerStartWait bounds how long the daemon waits for a worker to say it
// is ready.
const workerStartWait = 20 * time.Second

// defaultRetention is how long an idle session no terminal shows stays.
const defaultRetention = time.Minute

// worker is a running session worker, in the daemon.
type worker struct {
	info Worker
	cmd  *exec.Cmd
}

// workerReq is the "worker" request's answer.
type workerAnswer struct {
	Worker
	Error    string `json:"error,omitempty"`
	ReadOnly string `json:"readOnly,omitempty"`
}

// startWorker finds or starts the worker for h: Target names a session to
// resume ("" starts a new one in h.Cwd; h.Args may carry -model and
// -effort).
func (d *daemon) startWorker(h Hello) workerAnswer {
	d.wmu.Lock() // one start at a time: repeated starts converge
	defer d.wmu.Unlock()
	if h.Target != "" {
		d.mu.Lock()
		w := d.workers[h.Target]
		d.mu.Unlock()
		if w != nil {
			return workerAnswer{Worker: w.info}
		}
	}
	sock, err := workerSocket()
	if err != nil {
		return workerAnswer{Error: err.Error()}
	}
	args := []string{"_session-server", "-socket", sock}
	if h.Target != "" {
		args = append(args, "-session", h.Target)
	}
	args = append(args, h.Args...)
	cmd := exec.Command(d.exe, args...)
	cmd.Dir = h.Cwd
	cmd.Env = slices.DeleteFunc(slices.Clone(h.Env), func(e string) bool {
		return strings.HasPrefix(e, EnvPane+"=") || strings.HasPrefix(e, EnvPaneToken+"=")
	})
	out, err := cmd.StdoutPipe()
	if err != nil {
		return workerAnswer{Error: err.Error()}
	}
	cmd.Stderr = nil
	if log, err := os.OpenFile(LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		cmd.Stderr = log
		defer log.Close()
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
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
		_ = os.Remove(sock)
		return workerAnswer{Error: "the session's runtime did not start (see " + LogPath() + ")"}
	}
	verb, rest, _ := strings.Cut(first, " ")
	switch verb {
	case "ready":
	case "readonly":
		_ = cmd.Wait()
		_ = os.Remove(sock)
		return workerAnswer{ReadOnly: rest}
	default:
		_ = cmd.Wait()
		_ = os.Remove(sock)
		if rest == "" {
			rest = "the session's runtime ended at once (see " + LogPath() + ")"
		}
		return workerAnswer{Error: rest}
	}
	w := &worker{cmd: cmd, info: Worker{Session: rest, Socket: sock, PID: cmd.Process.Pid, Cwd: h.Cwd, Started: time.Now()}}
	d.mu.Lock()
	d.workers[w.info.Session] = w
	d.idle.Stop()
	d.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		_ = os.Remove(sock)
		d.mu.Lock()
		if d.workers[w.info.Session] == w {
			delete(d.workers, w.info.Session)
		}
		if len(d.panes) == 0 && len(d.workers) == 0 {
			d.idle.Reset(d.idleAfter)
		}
		d.mu.Unlock()
	}()
	return workerAnswer{Worker: w.info}
}

func (d *daemon) workerList() []Worker {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []Worker
	for _, w := range d.workers {
		out = append(out, w.info)
	}
	slices.SortFunc(out, func(a, b Worker) int { return a.Started.Compare(b.Started) })
	return out
}

// stopWorkers ends every worker (daemon stop -force): their sessions
// close as on exit.
func (d *daemon) stopWorkers() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, w := range d.workers {
		_ = w.cmd.Process.Signal(syscall.SIGTERM)
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
	p := filepath.Join(filepath.Dir(SocketPath()), name)
	if len(p) > maxSocketPath {
		h := sha256.Sum256([]byte(config.Dir()))
		p = filepath.Join(os.TempDir(), fmt.Sprintf("atto-%d", os.Getuid()), fmt.Sprintf("%x-%s", h[:4], name))
	}
	if err := privateDir(filepath.Dir(p)); err != nil {
		return "", err
	}
	return p, nil
}

// StartWorker asks the daemon (started if need be) for the worker of
// session id ("" starts a new session in cwd; args may carry -model and
// -effort). A session another process writes comes back as readOnly (no
// worker).
func StartWorker(id, cwd string, args []string) (w Worker, readOnly string, err error) {
	c, typ, b, err := request(Hello{Op: "worker", Target: id, Cwd: cwd, Env: os.Environ(), Args: args}, true)
	if err != nil {
		return Worker{}, "", err
	}
	defer c.Close()
	var a workerAnswer
	if typ != fWorker || json.Unmarshal(b, &a) != nil {
		return Worker{}, "", errors.New("daemon: unexpected answer")
	}
	if a.Error != "" {
		return Worker{}, "", errors.New(a.Error)
	}
	return a.Worker, a.ReadOnly, nil
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *sock == "" {
		return errors.New("-socket is required")
	}
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
	srv.Retire, srv.KeepForWork, srv.Retention = true, true, retention()
	closed := make(chan struct{})
	var once sync.Once
	srv.OnThreadClosed = func(string) { once.Do(func() { close(closed) }) }
	ctx := context.Background()

	var info server.ThreadInfo
	method, params := "thread/start", map[string]any{"cwd": cwd, "model": *model, "effort": *effort}
	if *id != "" {
		method, params = "thread/resume", map[string]any{"threadId": *id, "cwd": cwd}
	}
	if err := callServer(ctx, srv, method, params, &info); err != nil {
		srv.Close()
		return fail("error", err)
	}
	if info.ReadOnly != "" || info.Offline {
		srv.Close()
		return fail("readonly", errors.New(info.ReadOnly))
	}
	_ = os.Remove(*sock)
	ln, err := net.Listen("unix", *sock)
	if err != nil {
		srv.Close()
		return fail("error", err)
	}
	_ = os.Chmod(*sock, 0o600)
	defer os.Remove(*sock)
	fmt.Printf("ready %s\n", info.ID)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if checkPeer(c.(*net.UnixConn)) != nil {
				c.Close()
				continue
			}
			go func() { _ = srv.ServeConn(ctx, c) }()
		}
	}()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	select {
	case <-closed:
	case <-sigs:
	}
	ln.Close()
	srv.CloseWith("other")
	return nil
}

// callServer makes one request of srv from inside the worker (no client).
func callServer(ctx context.Context, srv *server.Server, method string, params, result any) error {
	c := server.Connect(ctx, srv)
	defer c.Close()
	return c.Call(ctx, method, params, result)
}

// retention is settings.json's sessionRetention, or the default.
func retention() time.Duration {
	s, _ := config.LoadSettings()
	if s.SessionRetention == "" {
		return defaultRetention
	}
	if s.SessionRetention == "0" {
		return 0
	}
	if d, err := time.ParseDuration(s.SessionRetention); err == nil && d >= 0 {
		return d
	}
	return defaultRetention
}

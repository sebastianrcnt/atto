package daemon

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/provider/providertest"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/session"
)

// workerClient connects to w and attaches to its session.
func workerClient(t *testing.T, w Worker) (*server.Client, server.ThreadInfo) {
	t.Helper()
	nc, err := DialWorker(w)
	if err != nil {
		t.Fatal(err)
	}
	c := server.NewClient(nc)
	ctx := context.Background()
	if err := c.Call(ctx, "initialize", map[string]any{"protocolVersions": []int{server.ProtocolVersion}}, nil); err != nil {
		t.Fatal(err)
	}
	var info server.ThreadInfo
	if err := c.Call(ctx, "thread/attach", map[string]any{"threadId": w.Session}, &info); err != nil {
		t.Fatal(err)
	}
	return c, info
}

// A session runs in a worker of the daemon: terminals come and go as
// clients, a turn goes on while none is attached, and the worker ends
// with its session.
func TestSessionWorker(t *testing.T) {
	startDaemon(t, 5*time.Second)
	defer Stop(true)
	gate := make(chan struct{})
	m := providertest.New(t, providertest.Reply{Text: "the answer", Gate: gate})
	m.Install(t, config.Dir())
	cwd := t.TempDir()

	w, readOnly, err := StartWorker("", cwd, nil)
	if err != nil || readOnly != "" || w.Session == "" {
		t.Fatalf("start: %+v %q %v", w, readOnly, err)
	}
	// The directory may be shared with the user's own workers.
	others, _ := filepath.Glob(filepath.Join(filepath.Dir(w.Socket), "w-*.sock"))
	again, _, err := StartWorker(w.Session, cwd, nil)
	if err != nil || again.Socket != w.Socket || again.PID != w.PID {
		t.Fatalf("a second start must find the same worker: %+v %v", again, err)
	}

	// A view starts a turn and goes away while the model answers.
	c, _ := workerClient(t, w)
	if err := c.Call(context.Background(), "input/submit", map[string]any{"threadId": w.Session, "input": "hello"}, nil); err != nil {
		t.Fatal(err)
	}
	m.Started(10 * time.Second)
	c.Close()
	close(gate)
	time.Sleep(300 * time.Millisecond)

	// Another view finds the turn done, in the same worker.
	c2, info := workerClient(t, w)
	defer c2.Close()
	deadline := time.Now().Add(10 * time.Second)
	for info.Busy && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		_ = c2.Call(context.Background(), "thread/read", map[string]any{"threadId": w.Session}, &info)
	}
	b, _ := json.Marshal(info.Items)
	if info.Busy || !strings.Contains(string(b), "the answer") {
		t.Fatalf("the turn did not go on without a view: busy %v\n%s", info.Busy, b)
	}
	if ws, _ := Workers(); len(ws) != 1 || ws[0].Session != w.Session {
		t.Fatalf("workers %+v", ws)
	}

	// Closing the session ends the worker.
	if err := c2.Call(context.Background(), "thread/close", map[string]any{"threadId": w.Session}, nil); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if ws, _ := Workers(); len(ws) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the worker outlived its session")
		}
	}
	if _, err := os.Stat(w.Socket); !os.IsNotExist(err) {
		t.Fatalf("socket left: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(w.Socket), "w-*.sock"))
	for _, m := range matches {
		if !slices.Contains(others, m) || m == w.Socket {
			t.Fatalf("sockets left: %v", matches)
		}
	}
}

func TestWorkerRetentionDoesNotStopDetachedTurn(t *testing.T) {
	startDaemon(t, 5*time.Second)
	defer Stop(true)
	gate := make(chan struct{})
	m := providertest.New(t, providertest.Reply{Text: "retained answer", Gate: gate})
	m.Install(t, config.Dir())
	w, _, err := StartWorker("", t.TempDir(), []string{"-retention", "100ms"})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := workerClient(t, w)
	if err := c.Call(context.Background(), "turn/start", map[string]any{"threadId": w.Session, "input": "wait for me"}, nil); err != nil {
		t.Fatal(err)
	}
	if m.Started(5*time.Second) == 0 {
		t.Fatal("no model request")
	}
	c.Close()
	time.Sleep(250 * time.Millisecond)
	if workers, err := Workers(); err != nil || len(workers) != 1 || !workers[0].Busy || workers[0].Clients != 0 || workers[0].Version != "test" || workers[0].ID == "" {
		t.Fatalf("busy worker retired or state wrong: %+v %v", workers, err)
	}
	close(gate)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		workers, err := Workers()
		if err != nil {
			t.Fatal(err)
		}
		if len(workers) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("idle unattended worker did not retire")
		}
	}
	if _, err := os.Stat(w.Socket); !os.IsNotExist(err) {
		t.Fatalf("retired worker left socket: %v", err)
	}
}

func TestWorkerFailedStartRemovesSocket(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	sock, err := workerSocket()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sock, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunWorker("test", []string{"-socket", sock, "-session", "does-not-exist"}); err == nil {
		t.Fatal("missing session succeeded")
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("failed worker left stale socket: %v", err)
	}
}

func TestWorkerConcurrentResumeHasOneWriter(t *testing.T) {
	startDaemon(t, 5*time.Second)
	defer Stop(true)
	m := providertest.New(t, providertest.Reply{Text: "ok"})
	m.Install(t, config.Dir())
	cwd := t.TempDir()
	w, _, err := StartWorker("", cwd, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan Worker, 8)
	errs := make(chan error, 8)
	for range 8 {
		go func() { again, _, err := StartWorker(w.Session[:4], cwd, nil); got <- again; errs <- err }()
	}
	for range 8 {
		again := <-got
		err := <-errs
		if err != nil || again.PID != w.PID || again.Socket != w.Socket {
			t.Fatalf("second writer: %+v %v", again, err)
		}
	}
	st, err := os.Stat(w.Socket)
	if err != nil || runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("private socket: %v %v", st, err)
	}
}

func TestCleanWorkerSocketsKeepsLiveAndNonSocketPaths(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	dead, err := workerSocket()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", dead)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	live, err := workerSocket()
	if err != nil {
		t.Fatal(err)
	}
	liveLn, err := net.Listen("unix", live)
	if err != nil {
		t.Fatal(err)
	}
	defer liveLn.Close()
	file, err := workerSocket()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	cleanWorkerSockets()
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatalf("stale socket retained: %v", err)
	}
	for _, path := range []string{live, file} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("live or nonsocket path removed: %s: %v", path, err)
		}
	}
}

func TestWorkerDoesNotUnlinkLiveSocket(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	sock, err := workerSocket()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if err := RunWorker("test", []string{"-socket", sock}); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("live socket accepted: %v", err)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("live socket unlinked: %v", err)
	}
}

func TestWorkerResumeNamePrefixAndAmbiguity(t *testing.T) {
	startDaemon(t, 5*time.Second)
	defer Stop(true)
	m := providertest.New(t)
	m.Install(t, config.Dir())
	cwd := t.TempDir()
	w, _, err := StartWorker("", cwd, nil)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := workerClient(t, w)
	defer c.Close()
	if err := c.Call(context.Background(), "thread/setName", map[string]any{"threadId": w.Session, "name": "Fix parser"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"FIX PARSER", w.Session[:4]} {
		got, _, err := StartWorker(target, cwd, nil)
		if err != nil || got.PID != w.PID {
			t.Fatalf("resume %q: %+v %v", target, got, err)
		}
	}
	saved := session.New(cwd)
	original := saved.ID
	saved.ID = w.Session[:2] + "saved"
	saved.Path = strings.Replace(saved.Path, original+".jsonl", saved.ID+".jsonl", 1)
	saved.Append(session.Entry{Type: session.TypeName, Name: "other saved session"})
	saved.Close()
	if _, _, err := StartWorker(w.Session[:2], cwd, nil); err == nil || !strings.Contains(err.Error(), w.Session) || !strings.Contains(err.Error(), saved.ID) {
		t.Fatalf("ambiguous live/saved prefix: %v", err)
	}
	if got, _, err := StartWorker(w.Session, cwd, nil); err != nil || got.PID != w.PID {
		t.Fatalf("exact live ID: %+v %v", got, err)
	}
}

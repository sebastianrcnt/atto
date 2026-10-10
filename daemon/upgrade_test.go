package daemon

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/provider/providertest"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/ui"
)

// setBuild makes the test binary on disk, and the workers started from
// now on, the given build.
func setBuild(t *testing.T, version string) {
	t.Helper()
	t.Setenv("ATTO_TEST_VERSION", version)
	versionCache.Lock()
	versionCache.exe = ""
	versionCache.Unlock()
}

// attach connects to w as a client and attaches; reattach is whether it
// says it follows an upgrade close.
func attach(t *testing.T, w Worker, reattach bool) (*server.Client, server.ThreadInfo) {
	t.Helper()
	nc, err := DialWorker(w)
	if err != nil {
		t.Fatal(err)
	}
	c := server.NewClient(nc)
	t.Cleanup(func() { c.Close() })
	ctx := context.Background()
	caps := server.Capabilities{Interactive: true, Reattach: reattach, UI: &ui.Capabilities{Version: 1, Surface: "terminal", Elements: ui.Catalog()}}
	if err := c.Call(ctx, "initialize", map[string]any{"protocolVersions": []int{server.ProtocolVersion}, "capabilities": caps}, nil); err != nil {
		t.Fatal(err)
	}
	var info server.ThreadInfo
	if err := c.Call(ctx, "thread/attach", map[string]any{"threadId": w.Session}, &info); err != nil {
		t.Fatal(err)
	}
	return c, info
}

func workerVersion(t *testing.T, w Worker) string {
	t.Helper()
	var st Worker
	if err := callWorker(w, 5*time.Second, "worker/state", map[string]any{"threadId": w.Session}, &st); err != nil {
		t.Fatal(err)
	}
	return st.Version
}

// closedReason waits for c's thread/closed and returns its reason.
func closedReason(t *testing.T, c *server.Client) string {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case n, ok := <-c.Events():
			if !ok {
				t.Fatal("connection ended without thread/closed")
			}
			if n.Method == "thread/closed" {
				var p struct {
					Reason string `json:"reason"`
				}
				_ = json.Unmarshal(n.Params, &p)
				return p.Reason
			}
		case <-timeout:
			t.Fatal("no thread/closed")
		}
	}
}

func daemonLog(t *testing.T) string {
	t.Helper()
	b, _ := os.ReadFile(LogPath())
	return string(b)
}

// An idle worker of another build than the binary on disk is replaced
// when a client asks for it: the attached client is told (reason
// "upgrade"), the session comes back in a worker of the current build,
// with its transcript and the status instances of the UI.
func TestIdleOldWorkerReplacedOnAttach(t *testing.T) {
	startDaemon(t, 5*time.Second)
	defer Stop(true)
	m := providertest.New(t, providertest.Reply{Text: "answer of the old build"})
	m.Install(t, config.Dir())
	cwd := t.TempDir()
	setBuild(t, "v0.0.1")
	old, _, err := StartWorker("", cwd, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v := workerVersion(t, old); v != "v0.0.1" {
		t.Fatalf("old worker says %q", v)
	}
	if again, _, err := StartWorker(old.Session, cwd, nil); err != nil || again.PID != old.PID {
		t.Fatalf("a worker of the build on disk was replaced: %+v %v", again, err)
	}
	tui, _ := attach(t, old, true)
	if err := tui.Call(context.Background(), "turn/start", map[string]any{"threadId": old.Session, "input": "hello old build"}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the turn", 10*time.Second, func() bool {
		ws, _ := Workers()
		return len(ws) == 1 && !ws[0].Busy && strings.Contains(sessionText(t, old.Session), "answer of the old build")
	})

	setBuild(t, "v0.0.2")
	got, readOnly, err := StartWorker(old.Session, cwd, nil)
	if err != nil || readOnly != "" {
		t.Fatalf("resume: %v %q", err, readOnly)
	}
	if got.PID == old.PID || got.Session != old.Session {
		t.Fatalf("idle old worker kept: old %+v new %+v", old, got)
	}
	if reason := closedReason(t, tui); reason != "upgrade" {
		t.Fatalf("attached client told %q", reason)
	}
	if v := workerVersion(t, got); v != "v0.0.2" {
		t.Fatalf("new worker says %q", v)
	}
	_, info := attach(t, got, true)
	b, _ := json.Marshal(info.Items)
	if !strings.Contains(string(b), "answer of the old build") || info.RuntimeVersion != "v0.0.2" {
		t.Fatalf("snapshot after the swap: %s %q", b, info.RuntimeVersion)
	}
	status := 0
	for _, i := range info.UI.Instances {
		if i.Site == ui.Status {
			status++
		}
	}
	if status == 0 {
		t.Fatalf("no status instances in the new worker's snapshot: %+v", info.UI.Instances)
	}
	if !strings.Contains(daemonLog(t), "replaced the worker of session "+old.Session) {
		t.Fatalf("no log line:\n%s", daemonLog(t))
	}
	if ws, _ := Workers(); len(ws) != 1 || ws[0].PID != got.PID {
		t.Fatalf("workers %+v", ws)
	}
}

func sessionText(t *testing.T, id string) string {
	t.Helper()
	var info server.ThreadInfo
	ws, _ := Workers()
	for _, w := range ws {
		if w.Session == id {
			_ = callWorker(w, 5*time.Second, "thread/read", map[string]any{"threadId": id}, &info)
		}
	}
	b, _ := json.Marshal(info.Items)
	return string(b)
}

// A busy worker of an older build is never interrupted: it goes on, and
// is replaced at a later request, once idle.
func TestBusyOldWorkerReplacedOnceIdle(t *testing.T) {
	startDaemon(t, 5*time.Second)
	defer Stop(true)
	gate := make(chan struct{})
	m := providertest.New(t, providertest.Reply{Text: "finished by the old build", Gate: gate})
	m.Install(t, config.Dir())
	cwd := t.TempDir()
	setBuild(t, "v0.0.1")
	old, _, err := StartWorker("", cwd, nil)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := attach(t, old, true)
	if err := c.Call(context.Background(), "turn/start", map[string]any{"threadId": old.Session, "input": "long work"}, nil); err != nil {
		t.Fatal(err)
	}
	m.Started(10 * time.Second)
	setBuild(t, "v0.0.2")
	if got, _, err := StartWorker(old.Session, cwd, nil); err != nil || got.PID != old.PID {
		t.Fatalf("busy worker replaced: %+v %v", got, err)
	}
	if ws, _ := Workers(); len(ws) != 1 || !ws[0].Busy || ws[0].PID != old.PID {
		t.Fatalf("the turn did not go on: %+v", ws)
	}
	if !strings.Contains(daemonLog(t), "kept the worker of session "+old.Session) {
		t.Fatalf("no log line for the kept worker:\n%s", daemonLog(t))
	}
	close(gate)
	waitFor(t, "the turn's end", 10*time.Second, func() bool { ws, _ := Workers(); return len(ws) == 1 && !ws[0].Busy })
	got, _, err := StartWorker(old.Session, cwd, nil)
	if err != nil || got.PID == old.PID {
		t.Fatalf("idle worker not replaced: %+v %v", got, err)
	}
	if v := workerVersion(t, got); v != "v0.0.2" {
		t.Fatalf("new worker says %q", v)
	}
	if !strings.Contains(sessionText(t, old.Session), "finished by the old build") {
		t.Fatal("the old build's turn is missing from the session")
	}
	if len(m.Requests()) != 1 {
		t.Fatalf("turn rerun: %v", m.Requests())
	}
}

// A client that does not say it follows an upgrade close (an older TUI,
// atto -p) keeps the worker until it leaves.
func TestOldWorkerKeptWhileOldClientAttached(t *testing.T) {
	startDaemon(t, 5*time.Second)
	defer Stop(true)
	m := providertest.New(t, providertest.Reply{Text: "saved"})
	m.Install(t, config.Dir())
	cwd := t.TempDir()
	setBuild(t, "v0.0.1")
	old, _, err := StartWorker("", cwd, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A new session lives only in its worker until its first entry.
	setBuild(t, "v0.0.2")
	if got, _, err := StartWorker(old.Session, cwd, nil); err != nil || got.PID != old.PID {
		t.Fatalf("unsaved session replaced: %+v %v", got, err)
	}
	if !strings.Contains(daemonLog(t), ": unsaved") {
		t.Fatalf("log:\n%s", daemonLog(t))
	}
	c, _ := attach(t, old, false)
	if err := c.Call(context.Background(), "turn/start", map[string]any{"threadId": old.Session, "input": "save me"}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the turn", 10*time.Second, func() bool { return strings.Contains(sessionText(t, old.Session), "saved") })
	waitFor(t, "idle", 10*time.Second, func() bool { ws, _ := Workers(); return len(ws) == 1 && !ws[0].Busy })
	if got, _, err := StartWorker(old.Session, cwd, nil); err != nil || got.PID != old.PID {
		t.Fatalf("replaced under a client that cannot follow: %+v %v", got, err)
	}
	if !strings.Contains(daemonLog(t), ": client") {
		t.Fatalf("log:\n%s", daemonLog(t))
	}
	c.Close()
	waitFor(t, "the client to leave", 5*time.Second, func() bool { ws, _ := Workers(); return len(ws) == 1 && ws[0].Clients == 0 })
	if got, _, err := StartWorker(old.Session, cwd, nil); err != nil || got.PID == old.PID {
		t.Fatalf("not replaced once the client left: %+v %v", got, err)
	}
}

// A daemon of another build than the binary on disk hands its workers
// over to the daemon of that binary, which adopts them; they go on.
func TestDaemonHandsOverToCurrentBuild(t *testing.T) {
	setBuild(t, "v0.0.1")
	done := startDaemon(t, 5*time.Second)
	noHandover.Store(false)
	t.Cleanup(func() { _ = Stop(true) })
	m := providertest.New(t, providertest.Reply{Text: "after the handover"})
	m.Install(t, config.Dir())
	cwd := t.TempDir()
	setBuild(t, "v0.0.2")
	w, _, err := StartWorker("", cwd, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the old daemon did not hand over")
	}
	var info Info
	waitFor(t, "the new daemon", 20*time.Second, func() bool {
		info, err = Describe()
		return err == nil
	})
	if info.Version != "v0.0.2" || info.PID == os.Getpid() {
		t.Fatalf("daemon after the handover: %+v", info)
	}
	ws, err := Workers()
	if err != nil || len(ws) != 1 || ws[0].PID != w.PID || ws[0].Session != w.Session {
		t.Fatalf("worker not adopted: %+v %v", ws, err)
	}
	if again, _, err := StartWorker(w.Session, cwd, nil); err != nil || again.PID != w.PID {
		t.Fatalf("adopted worker not found: %+v %v", again, err)
	}
	c, _ := attach(t, ws[0], true)
	if err := c.Call(context.Background(), "turn/start", map[string]any{"threadId": w.Session, "input": "still there?"}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a turn in the adopted worker", 10*time.Second, func() bool {
		return strings.Contains(sessionText(t, w.Session), "after the handover")
	})
	log := daemonLog(t)
	if !strings.Contains(log, "hands 1 worker(s) over") || !strings.Contains(log, "adopted 1 worker(s)") {
		t.Fatalf("log:\n%s", log)
	}
	c.Close()
	if err := Stop(true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the adopted worker to stop", 10*time.Second, func() bool {
		_, err := os.Stat(w.Socket)
		return os.IsNotExist(err)
	})
}

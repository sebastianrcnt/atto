//go:build !windows

package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/provider/providertest"
	"github.com/sebastianrcnt/atto/server"
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

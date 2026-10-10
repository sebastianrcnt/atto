package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/provider/providertest"
	"github.com/sebastianrcnt/atto/server"
)

// A facade (atto serve, app-server) marks the snapshot of an older busy
// worker, and follows its replacement: its clients get events/reset,
// not thread/closed, and read the session again from the new worker,
// which the facade attaches to as it did to the old one.
func TestGatewayFollowsWorkerUpgrade(t *testing.T) {
	startDaemon(t, 5*time.Second)
	defer Stop(true)
	gate := make(chan struct{})
	m := providertest.New(t, providertest.Reply{Text: "seed answer"}, providertest.Reply{Text: "long answer", Gate: gate}, providertest.Reply{Text: "after answer"})
	m.Install(t, config.Dir())
	cwd := t.TempDir()
	setBuild(t, "v0.0.1")
	gw := server.New("v0.0.2", cwd)
	gw.Workers = Routes()
	defer gw.Close()
	ctx := context.Background()
	fc := server.Connect(ctx, gw)
	defer fc.Close()
	if err := fc.Call(ctx, "initialize", map[string]any{"protocolVersions": []int{3}}, nil); err != nil {
		t.Fatal(err)
	}
	var th server.ThreadInfo
	if err := fc.Call(ctx, "thread/start", map[string]any{"cwd": cwd}, &th); err != nil {
		t.Fatal(err)
	}
	if th.RuntimeVersion != "v0.0.1" || !th.RuntimeOutdated {
		t.Fatalf("snapshot of an older worker: %q %v", th.RuntimeVersion, th.RuntimeOutdated)
	}
	if err := fc.Call(ctx, "turn/start", map[string]any{"threadId": th.ID, "input": "seed"}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the seed turn", 10*time.Second, func() bool { return strings.Contains(sessionText(t, th.ID), "seed answer") })
	waitFor(t, "idle", 10*time.Second, func() bool { ws, _ := Workers(); return len(ws) == 1 && !ws[0].Busy })
	if err := fc.Call(ctx, "turn/start", map[string]any{"threadId": th.ID, "input": "long"}, nil); err != nil {
		t.Fatal(err)
	}
	m.Started(10 * time.Second)
	old, _ := Workers()
	setBuild(t, "v0.0.2")
	if w, _, err := StartWorker(th.ID, cwd, nil); err != nil || w.PID != old[0].PID {
		t.Fatalf("busy worker replaced: %+v %v", w, err)
	}
	var info server.ThreadInfo
	if err := fc.Call(ctx, "thread/resume", map[string]any{"threadId": th.ID}, &info); err != nil || !info.RuntimeOutdated || info.RuntimeVersion != "v0.0.1" {
		t.Fatalf("busy old worker's snapshot: %q %v %v", info.RuntimeVersion, info.RuntimeOutdated, err)
	}
	close(gate)
	waitFor(t, "idle", 10*time.Second, func() bool { ws, _ := Workers(); return len(ws) == 1 && !ws[0].Busy })
	for drained := false; !drained; { // what the turn sent
		select {
		case <-fc.Events():
		case <-time.After(200 * time.Millisecond):
			drained = true
		}
	}

	// Another client (a TUI opening the session) triggers the swap.
	w, _, err := StartWorker(th.ID, cwd, nil)
	if err != nil || w.PID == old[0].PID {
		t.Fatalf("idle worker not replaced: %+v %v", w, err)
	}
	deadline := time.After(10 * time.Second)
wait:
	for {
		select {
		case n := <-fc.Events():
			if n.Method == "thread/closed" {
				t.Fatalf("facade relayed the upgrade close: %s", n.Params)
			}
			if n.Method == "events/reset" {
				break wait
			}
		case <-deadline:
			t.Fatal("no events/reset")
		}
	}
	info = server.ThreadInfo{}
	if err := fc.Call(ctx, "thread/read", map[string]any{"threadId": th.ID}, &info); err != nil {
		t.Fatal(err)
	}
	if info.RuntimeVersion != "v0.0.2" || info.RuntimeOutdated {
		t.Fatalf("snapshot after the swap: %q %v", info.RuntimeVersion, info.RuntimeOutdated)
	}
	b, _ := json.Marshal(info.Items)
	if !strings.Contains(string(b), "long answer") {
		t.Fatalf("transcript lost: %s", b)
	}
	if ws, _ := Workers(); len(ws) != 1 || ws[0].PID != w.PID || ws[0].Clients != 1 {
		t.Fatalf("the facade did not attach to the new worker: %+v", ws)
	}
	if err := fc.Call(ctx, "turn/start", map[string]any{"threadId": th.ID, "input": "after"}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a turn in the new worker", 10*time.Second, func() bool { return strings.Contains(sessionText(t, th.ID), "after answer") })
}

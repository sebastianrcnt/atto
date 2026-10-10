package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/provider/providertest"
)

func retire(t *testing.T, c *Client, id string) retireResult {
	t.Helper()
	var r retireResult
	if err := c.Call(context.Background(), "worker/retire", map[string]any{"threadId": id, "reason": "upgrade"}, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func connectWith(t *testing.T, s *Server, caps Capabilities) *Client {
	t.Helper()
	c := Connect(context.Background(), s)
	t.Cleanup(func() { c.Close() })
	if err := c.Call(context.Background(), "initialize", map[string]any{"protocolVersions": []int{3}, "capabilities": caps}, nil); err != nil {
		t.Fatal(err)
	}
	return c
}

// worker/retire closes an idle session for a worker of another build,
// ending nothing, and refuses while anything runs or waits, or while a
// client that cannot follow is attached. Input after it is refused.
func TestWorkerRetireForUpgrade(t *testing.T) {
	gate := make(chan struct{})
	s, m := testServer(t, providertest.Reply{Text: "first", Gate: gate}, providertest.Reply{Text: "second"})
	ctx := context.Background()
	c := connectWith(t, s, Capabilities{Reattach: true})
	var th ThreadInfo
	if err := c.Call(ctx, "thread/start", map[string]any{}, &th); err != nil {
		t.Fatal(err)
	}
	if th.RuntimeVersion != "test" {
		t.Fatalf("snapshot runtimeVersion %q", th.RuntimeVersion)
	}
	if r := retire(t, c, th.ID); r.Retired || r.Reason != "unsaved" {
		t.Fatalf("unsaved session: %+v", r)
	}
	if err := c.Call(ctx, "turn/start", map[string]any{"threadId": th.ID, "input": "hi"}, nil); err != nil {
		t.Fatal(err)
	}
	m.Started(5 * time.Second)
	if r := retire(t, c, th.ID); r.Retired || r.Reason != "busy" {
		t.Fatalf("busy: %+v", r)
	}
	close(gate)
	waitIdle := func() {
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			var info ThreadInfo
			_ = c.Call(ctx, "thread/read", map[string]any{"threadId": th.ID}, &info)
			if !info.Busy {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("turn did not end")
			}
		}
	}
	waitIdle()

	// A goal in progress keeps the worker.
	tt, _ := s.thread(th.ID)
	_ = tt.call(func() error { tt.goal.Goal = &goal.Goal{Objective: "x", Status: goal.Active}; return nil })
	if r := retire(t, c, th.ID); r.Retired || r.Reason != "goal" {
		t.Fatalf("goal: %+v", r)
	}
	_ = tt.call(func() error { tt.goal.Goal = nil; return nil })

	// An attached client that does not follow an upgrade keeps it.
	old := connectWith(t, s, Capabilities{})
	if err := old.Call(ctx, "thread/attach", map[string]any{"threadId": th.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if r := retire(t, c, th.ID); r.Retired || r.Reason != "client" {
		t.Fatalf("old client attached: %+v", r)
	}
	if err := old.Call(ctx, "thread/detach", map[string]any{"threadId": th.ID}, nil); err != nil {
		t.Fatal(err)
	}

	follower := connectWith(t, s, Capabilities{Reattach: true})
	if err := follower.Call(ctx, "thread/attach", map[string]any{"threadId": th.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if r := retire(t, c, th.ID); !r.Retired {
		t.Fatalf("idle: %+v", r)
	}
	for deadline := time.After(5 * time.Second); ; {
		select {
		case n := <-follower.Events():
			if n.Method != "thread/closed" {
				continue
			}
			var p map[string]any
			_ = json.Unmarshal(n.Params, &p)
			if p["reason"] != "upgrade" {
				t.Fatalf("closed %v", p)
			}
		case <-deadline:
			t.Fatal("no thread/closed")
		}
		break
	}
	err := c.Call(ctx, "input/submit", map[string]any{"threadId": th.ID, "input": "too late"}, nil)
	if err == nil || !strings.Contains(err.Error(), "thread") {
		t.Fatalf("input after the retirement: %v", err)
	}
	if len(m.Requests()) != 1 {
		t.Fatalf("requests %v", m.Requests())
	}
}

// Input racing a retirement is either taken (a turn runs, or ran) or
// refused, never accepted and dropped.
func TestWorkerRetireRacingInput(t *testing.T) {
	const rounds = 20
	var script []providertest.Reply
	for range 2 * rounds {
		script = append(script, providertest.Reply{Text: "ok"})
	}
	s, m := testServer(t, script...)
	ctx := context.Background()
	c := connectWith(t, s, Capabilities{Reattach: true})
	accepted := 0
	for range rounds {
		var th ThreadInfo
		if err := c.Call(ctx, "thread/start", map[string]any{}, &th); err != nil {
			t.Fatal(err)
		}
		if err := c.Call(ctx, "turn/start", map[string]any{"threadId": th.ID, "input": "seed"}, nil); err != nil {
			t.Fatal(err)
		}
		accepted++
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
			var info ThreadInfo
			_ = c.Call(ctx, "thread/read", map[string]any{"threadId": th.ID}, &info)
			if !info.Busy && len(m.Requests()) == accepted {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("seed turn did not end")
			}
		}
		in := Connect(ctx, s)
		if err := in.Call(ctx, "initialize", map[string]any{"protocolVersions": []int{3}}, nil); err != nil {
			t.Fatal(err)
		}
		errc := make(chan error, 1)
		go func() {
			errc <- in.Call(ctx, "input/submit", map[string]any{"threadId": th.ID, "input": "racing"}, nil)
		}()
		r := retire(t, c, th.ID)
		err := <-errc
		in.Close()
		if err == nil {
			accepted++
			for deadline := time.Now().Add(5 * time.Second); len(m.Requests()) < accepted; time.Sleep(5 * time.Millisecond) {
				if time.Now().After(deadline) {
					t.Fatalf("accepted input never ran (retired %+v)", r)
				}
			}
		}
		if !r.Retired {
			// The input won: the session goes on in this runtime.
			_ = c.Call(ctx, "thread/close", map[string]any{"threadId": th.ID}, nil)
		}
	}
}

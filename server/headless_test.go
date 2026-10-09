package server

import (
	"context"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/provider/providertest"
)

func TestHeadlessWorkerDropsDisplayAndRebuildsOnAttach(t *testing.T) {
	s, _ := testServer(t, providertest.Reply{Command: "echo headless-output", Description: "Headless command"}, providertest.Reply{Text: "headless answer"})
	ctx := context.Background()
	c := Connect(ctx, s)
	defer c.Close()
	if err := c.Call(ctx, "initialize", map[string]any{"protocolVersions": []int{3}}, nil); err != nil {
		t.Fatal(err)
	}
	var info ThreadInfo
	if err := c.Call(ctx, "thread/start", nil, &info); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(ctx, "thread/detach", map[string]any{"threadId": info.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(ctx, "input/submit", map[string]any{"threadId": info.ID, "input": "headless question"}, nil); err != nil {
		t.Fatal(err)
	}
	done := false
	deadline := time.After(10 * time.Second)
	for !done {
		select {
		case n := <-c.Events():
			done = n.Method == "turn/completed"
		case <-deadline:
			t.Fatal("headless turn did not complete")
		}
	}
	th, _ := s.thread(info.ID)
	if err := th.call(func() error {
		if len(th.attached) != 0 || len(th.items) != 0 || len(th.tr.Items()) != 0 || len(th.blocks) != 0 {
			t.Errorf("headless display retained: clients=%d items=%d builder=%d blocks=%d", len(th.attached), len(th.items), len(th.tr.Items()), len(th.blocks))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var read ThreadInfo
	if err := c.Call(ctx, "thread/read", map[string]any{"threadId": info.ID}, &read); err != nil {
		t.Fatal(err)
	}
	if len(read.Items) == 0 {
		t.Fatal("headless read did not reconstruct disk items")
	}
	_ = th.call(func() error {
		if len(th.items) != 0 {
			t.Error("read retained headless display")
		}
		return nil
	})
	if err := c.Call(ctx, "thread/attach", map[string]any{"threadId": info.ID}, &info); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range info.Items {
		if it.Text == "headless answer" {
			found = true
		}
	}
	if !found {
		t.Fatal("headless saved answer not reconstructed")
	}
	if err := c.Call(ctx, "thread/detach", map[string]any{"threadId": info.ID}, nil); err != nil {
		t.Fatal(err)
	}
	_ = th.call(func() error {
		if len(th.items) != 0 || len(th.tr.Items()) != 0 {
			t.Error("last detach kept a tail")
		}
		return nil
	})
}

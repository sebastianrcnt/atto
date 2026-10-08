package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/provider/providertest"
	"github.com/sebastianrcnt/atto/session"
)

func TestScopedGatewayProtocol(t *testing.T) {
	gate := make(chan struct{})
	h := newHarness(t, providertest.Reply{Text: "first", Gate: gate}, providertest.Reply{Text: "steered"})
	var selected atomic.Value
	selected.Store(h.id)
	local := make(chan string, 1)
	web := httptest.NewServer(h.s.ScopedHandler("token", Scope{
		Thread: func() string { return selected.Load().(string) },
		Local: func(text string) bool {
			if text != "/clear" {
				return false
			}
			local <- text
			return true
		},
	}))
	defer web.Close()
	c := rpcClient{t, web.URL, "token"}
	init := c.must("initialize", map[string]any{"protocolVersions": []int{1, 2}})
	if init["live"] != true || init["threadId"] != h.id || init["clientId"] == nil || init["protocolVersion"] != float64(2) {
		t.Fatalf("initialize: %v", init)
	}
	for _, method := range []string{"thread/read", "thread/resume", "thread/attach"} {
		info := c.must(method, nil)
		if info["threadId"] != h.id || info["live"] != true || info["eventId"] == nil {
			t.Fatalf("%s: %v", method, info)
		}
	}
	listed := c.must("thread/list", nil)["threads"].([]any)
	if len(listed) != 1 || listed[0].(map[string]any)["threadId"] != h.id {
		t.Fatalf("list: %v", listed)
	}
	if _, err := c.call("thread/start", nil); err == nil || err.Data.Reason != ReasonUnsupported {
		t.Fatalf("thread/start: %v", err)
	}
	for _, method := range []string{"thread/read", "turn/start", "thread/close", "job/list"} {
		if _, err := c.call(method, map[string]any{"threadId": "another", "input": "no"}); err == nil {
			t.Fatalf("%s admitted another session", method)
		}
	}
	if out := c.must("turn/start", map[string]any{"input": "start"}); out["status"] != StatusStarted {
		t.Fatalf("start: %v", out)
	}
	if h.m.Started(5*time.Second) == 0 {
		t.Fatal("model did not start")
	}
	if out := c.must("turn/start", map[string]any{"input": "also"}); out["status"] != StatusSteered {
		t.Fatalf("steer via start: %v", out)
	}
	c.must("turn/unsteer", map[string]any{"input": "also"})
	if out := c.must("turn/steer", map[string]any{"input": "kept"}); out["status"] != StatusSteered {
		t.Fatalf("steer: %v", out)
	}
	if out := c.must("turn/start", map[string]any{"input": "/clear"}); out["status"] != StatusDone {
		t.Fatalf("local: %v", out)
	}
	select {
	case <-local:
	default:
		t.Fatal("local command was not forwarded")
	}
	close(gate)
	h.completed()
	read := c.must("thread/read", nil)
	var texts []string
	for _, raw := range read["items"].([]any) {
		if text, ok := raw.(map[string]any)["text"].(string); ok {
			texts = append(texts, text)
		}
	}
	all := strings.Join(texts, "\n")
	for _, text := range []string{"start", "kept", "first", "steered"} {
		if !strings.Contains(all, text) {
			t.Fatalf("missing %q: %s", text, all)
		}
	}
	path, err := session.Find(h.id)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := session.ReadActive(path)
	if err != nil || len(entries.Entries) == 0 {
		t.Fatalf("session: %v, %v", entries, err)
	}
	rollback := c.must("thread/rollback", nil)
	if rollback["input"] == nil || rollback["live"] != true {
		t.Fatalf("rollback: %v", rollback)
	}
	var other ThreadInfo
	if err := h.c.Call(context.Background(), "thread/start", nil, &other); err != nil {
		t.Fatal(err)
	}
	selected.Store(other.ID)
	h.s.Switched(other.ID, h.id)
	if out := c.must("thread/read", nil); out["threadId"] != other.ID || out["live"] != true {
		t.Fatalf("switch: %v", out)
	}
	if _, err := c.call("thread/read", map[string]any{"threadId": h.id}); err == nil {
		t.Fatal("old session remains accessible")
	}
}

func TestScopedGatewayFiltersLiveAndReplayedEvents(t *testing.T) {
	h := newHarness(t)
	var selected atomic.Value
	selected.Store(h.id)
	web := httptest.NewServer(h.s.ScopedHandler("token", Scope{Thread: func() string { return selected.Load().(string) }}))
	defer web.Close()
	from := h.s.eventSeq()
	publish := func(id, text string) {
		h.s.publish("item/completed", map[string]any{"threadId": id, "item": Item{ID: text, Text: text}})
	}
	publish("private", "secret replay")
	publish(h.id, "public replay")
	evs, stop := readSSE(t, web.URL+"/events?token=token", map[string]string{"Last-Event-ID": jsonNumber(from)})
	defer stop()
	assert := func(want string) {
		t.Helper()
		ev := nextSSE(t, evs)
		p := ev.data["params"].(map[string]any)
		if p["threadId"] != selected.Load().(string) || p["item"].(map[string]any)["text"] != want {
			t.Fatalf("event: %v; want %q", ev, want)
		}
	}
	assert("public replay")
	publish("private", "secret live")
	publish(h.id, "public live")
	assert("public live")
	old := h.id
	selected.Store("new")
	publish(old, "old session secret")
	publish("new", "new public")
	assert("new public")
	// Unauthenticated requests never reach either RPC or the event stream.
	for _, path := range []string{"/rpc", "/events"} {
		method := "GET"
		if path == "/rpc" {
			method = "POST"
		}
		req, _ := http.NewRequest(method, web.URL+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
	}
}

func jsonNumber(v int64) string { b, _ := json.Marshal(v); return string(b) }

func TestScopedGatewayStopDoesNotStopTurn(t *testing.T) {
	gate := make(chan struct{})
	h := newHarness(t, providertest.Reply{Text: "after gateway stopped", Gate: gate})
	web := httptest.NewServer(h.s.ScopedHandler("token", Scope{Thread: func() string { return h.id }}))
	c := rpcClient{t, web.URL, "token"}
	c.must("turn/start", map[string]any{"input": "go"})
	if h.m.Started(5*time.Second) == 0 {
		t.Fatal("model did not start")
	}
	web.Close()
	close(gate)
	h.completed()
	out := h.call("thread/read", nil)
	if out["turn"] != nil {
		t.Fatalf("turn still running: %v", out)
	}
	if len(h.m.Requests()) != 1 {
		t.Fatal("closing gateway restarted execution")
	}
}

func TestScopedGatewayDisconnectReleasesOnlyItsGate(t *testing.T) {
	h := newHarness(t)
	a := httptest.NewServer(h.s.ScopedHandler("token", Scope{Thread: func() string { return h.id }}))
	defer a.Close()
	b := httptest.NewServer(h.s.ScopedHandler("token", Scope{Thread: func() string { return h.id }}))
	defer b.Close()
	_, stopA := readSSE(t, a.URL+"/events?token=token", nil)
	defer stopA()
	_, stopB := readSSE(t, b.URL+"/events?token=token", nil)
	defer stopB()
	ca := rpcClient{t, a.URL, "token"}
	cb := rpcClient{t, b.URL, "token"}
	ca.must("client/gate", map[string]any{"open": true})
	cb.must("client/gate", map[string]any{"open": true})
	th, err := h.s.thread(h.id)
	if err != nil {
		t.Fatal(err)
	}
	gates := func() int {
		n := 0
		if err := th.call(func() error { n = len(th.gates); return nil }); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := gates(); n != 2 {
		t.Fatalf("gates before disconnect: %d", n)
	}
	stopA()
	deadline := time.Now().Add(5 * time.Second)
	for gates() != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("gates after disconnect: %d", gates())
		}
		time.Sleep(time.Millisecond)
	}
	cb.must("client/gate", map[string]any{"open": false})
	if n := gates(); n != 0 {
		t.Fatalf("other client's gate was not preserved: %d", n)
	}
}

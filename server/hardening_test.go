package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A connection that falls behind the hub loses events, never stalls the
// server, and is told so (events/reset) so it reads its threads again.
func TestSlowConnectionGetsReset(t *testing.T) {
	s := New("test", t.TempDir())
	defer s.Close()
	a, b := net.Pipe()
	defer b.Close()
	go s.ServeConn(context.Background(), a)
	time.Sleep(20 * time.Millisecond) // subscribed
	done := make(chan struct{})
	go func() {
		for i := range subscriberBuffer + 50 {
			s.publish("test/n", map[string]any{"i": i})
		}
		close(done)
	}()
	select {
	case <-done: // the server never waited for the reader
	case <-time.After(5 * time.Second):
		t.Fatal("publishing waited for a slow client")
	}
	sc := bufio.NewScanner(b)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	deadline := time.Now().Add(10 * time.Second)
	_ = b.SetReadDeadline(deadline)
	for sc.Scan() {
		var m struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(sc.Bytes(), &m)
		if m.Method == "events/reset" {
			return
		}
	}
	t.Fatal("no events/reset after falling behind")
}

// A scoped gateway serves its session only: another thread ID, or a new
// thread, is refused.
func TestScopedGatewayStaysInItsSession(t *testing.T) {
	s, _ := testServer(t)
	info, err := s.startThread("", threadParams{})
	if err != nil {
		t.Fatal(err)
	}
	id := info.(ThreadInfo).ID
	h := httptest.NewServer(s.ScopedHandler("tok-1234567890123456", Scope{Thread: func() string { return id }}))
	defer h.Close()
	call := func(method string, params map[string]any) (map[string]any, string) {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		req, _ := http.NewRequest("POST", h.URL+"/rpc", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer tok-1234567890123456")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var r struct {
			Result map[string]any `json:"result"`
			Error  *rpcError      `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&r)
		if r.Error != nil {
			return nil, r.Error.Message
		}
		return r.Result, ""
	}
	if r, _ := call("initialize", nil); r["live"] != true || r["threadId"] != id {
		t.Fatalf("initialize %v", r)
	}
	if _, msg := call("thread/read", map[string]any{"threadId": "someone-else"}); !strings.Contains(msg, "no longer the live session") {
		t.Fatalf("another thread: %q", msg)
	}
	if _, msg := call("thread/start", nil); !strings.Contains(msg, "live session") {
		t.Fatalf("thread/start: %q", msg)
	}
	if r, msg := call("thread/read", nil); msg != "" || r["threadId"] != id {
		t.Fatalf("its own thread: %v %q", r, msg)
	}
}

// ctx.hasUI is true while an interactive client is attached.
func TestHasUIFollowsInteractiveClients(t *testing.T) {
	s := New("test", t.TempDir())
	defer s.Close()
	h := threadHost{t: &thread{s: s}}
	if h.HasUI() {
		t.Fatal("no client: no UI")
	}
	c := Connect(context.Background(), s)
	if err := c.Call(context.Background(), "initialize", map[string]any{"capabilities": map[string]bool{"interactive": true}}, nil); err != nil {
		t.Fatal(err)
	}
	if !h.HasUI() {
		t.Fatal("an interactive client is attached")
	}
	c.Close()
	deadline := time.Now().Add(5 * time.Second)
	for h.HasUI() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if h.HasUI() {
		t.Fatal("the client left")
	}
}

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A subscriber that falls behind loses its stream rather than events.
func TestBrokerKicksSlowSubscriber(t *testing.T) {
	b := newBroker()
	ch, kick := b.subscribe()
	for i := 0; i < cap(ch)+1; i++ {
		b.publish(i)
	}
	select {
	case <-kick:
	default:
		t.Fatal("a subscriber that fell behind was not told")
	}
	if b.clients() != 0 {
		t.Fatal("kicked subscriber still subscribed")
	}
	if got := b.last(); got != int64(cap(ch)+1) {
		t.Fatalf("last event %d", got)
	}
}

// slowModel streams n words, one every delay.
func slowModel(t *testing.T, n int, delay time.Duration) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := range n {
			fmt.Fprintf(w, `data: {"choices":[{"delta":{"content":"w%d "}}]}`+"\n\n", i)
			w.(http.Flusher).Flush()
			time.Sleep(delay)
		}
		fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// A thread read while its answer streams has the answer so far, and the
// events after its eventId bring exactly the rest: nothing missing, nothing
// twice.
func TestReadWhileStreaming(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ATTO_DIR", dir)
	url := slowModel(t, 60, 10*time.Millisecond)
	os.WriteFile(filepath.Join(dir, "models.json"), []byte(`{"providers":{"fake":{"baseUrl":"`+url+`","models":[{"id":"m","contextWindow":100000}]}}}`), 0o644)
	s := New("test", t.TempDir())
	t.Cleanup(s.Close)
	ctx := context.Background()
	c := Connect(ctx, s)
	t.Cleanup(func() { c.Close() })
	call := func(method string, params any) map[string]any {
		t.Helper()
		var out map[string]any
		if err := c.Call(ctx, method, params, &out); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		return out
	}
	call("initialize", map[string]any{"protocolVersions": []int{ProtocolVersion}})
	started := call("thread/start", map[string]any{})
	id := started["threadId"].(string)
	call("turn/start", map[string]any{"threadId": id, "input": "hi"})

	var info map[string]any
	var msgItem map[string]any
	deadline := time.Now().Add(10 * time.Second)
	for msgItem == nil {
		if time.Now().After(deadline) {
			t.Fatal("the answer never showed in thread/read")
		}
		time.Sleep(50 * time.Millisecond)
		info = call("thread/read", map[string]any{"threadId": id})
		items, _ := info["items"].([]any)
		for _, x := range items {
			if it := x.(map[string]any); it["type"] == ItemAgent {
				msgItem = it
			}
		}
	}
	if msgItem["status"] != "inProgress" {
		t.Fatalf("read mid-stream: answer is %v", msgItem["status"])
	}
	text := msgItem["text"].(string)
	from := int64(info["eventId"].(float64))
	timeout := time.After(10 * time.Second)
	for {
		var n Notification
		select {
		case n = <-c.Events():
		case <-timeout:
			t.Fatal("the answer never completed")
		}
		if n.EventID <= from {
			continue // as of the read
		}
		var p map[string]any
		_ = json.Unmarshal(n.Params, &p)
		switch n.Method {
		case "item/delta":
			if p["itemId"] == msgItem["id"] {
				text += p["delta"].(string)
			}
		case "item/completed":
			it := p["item"].(map[string]any)
			if it["id"] == msgItem["id"] {
				if it["text"] != strings.TrimSpace(text) && it["text"] != text {
					t.Fatalf("snapshot + deltas = %q\ncompleted      = %q", text, it["text"])
				}
				return
			}
		}
	}
}

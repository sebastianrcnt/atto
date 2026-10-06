package server

import (
	"bufio"
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

// sseReader reads an event stream: each event's id ("" for none) and data.
type sseEv struct {
	id   string
	data map[string]any
}

func readSSE(t *testing.T, url string, header map[string]string) (<-chan sseEv, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	out := make(chan sseEv, 4096)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		var id string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "id: "):
				id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "data: "):
				var m map[string]any
				json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &m)
				out <- sseEv{id, m}
				id = ""
			}
		}
	}()
	return out, cancel
}

func nextSSE(t *testing.T, ch <-chan sseEv) sseEv {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("stream ended")
		}
		return ev
	case <-time.After(10 * time.Second):
		t.Fatal("no event")
	}
	return sseEv{}
}

func publishN(s *Server, n int) {
	for i := range n {
		s.Notify("test/n", map[string]any{"i": i})
	}
}

// EventSource reconnects by itself to the same URL, query included, with
// Last-Event-ID set to the last event it got: that one wins, or every
// delta since the query's ID would be applied twice.
func TestEventsHeaderWinsOverQuery(t *testing.T) {
	s := NewLive("test", &fakeLive{id: "s1"})
	h := httptest.NewServer(s.HTTPHandler("tok-1234567890123456"))
	defer h.Close()
	publishN(s, 10)
	evs, stop := readSSE(t, h.URL+"/events?token=tok-1234567890123456&lastEventId=2", map[string]string{"Last-Event-ID": "7"})
	defer stop()
	if ev := nextSSE(t, evs); ev.id != "8" {
		t.Fatalf("resumed at %q, want 8", ev.id)
	}
	// Without the header the query counts.
	evs2, stop2 := readSSE(t, h.URL+"/events?token=tok-1234567890123456&lastEventId=2", nil)
	defer stop2()
	if ev := nextSSE(t, evs2); ev.id != "3" {
		t.Fatalf("followed from %q, want 3", ev.id)
	}
}

// A client resuming from events the server no longer knows (it restarted,
// or the client was away too long) is told to read its thread again.
func TestEventsReset(t *testing.T) {
	s := NewLive("test", &fakeLive{id: "s1"})
	h := httptest.NewServer(s.HTTPHandler("tok-1234567890123456"))
	defer h.Close()
	s.events.keep = 5
	publishN(s, 20)
	for _, from := range []string{"50", "3"} { // ahead of the server; dropped from the ring
		evs, stop := readSSE(t, h.URL+"/events?token=tok-1234567890123456", map[string]string{"Last-Event-ID": from})
		ev := nextSSE(t, evs)
		if ev.id != "" || ev.data["method"] != "events/reset" {
			t.Fatalf("from %s: got %v (id %q), want events/reset", from, ev.data, ev.id)
		}
		if p := ev.data["params"].(map[string]any); p["eventId"] != float64(s.eventSeq()) {
			t.Fatalf("reset eventId = %v", p["eventId"])
		}
		publishN(s, 1) // and the stream goes on
		if ev := nextSSE(t, evs); ev.data["method"] != "test/n" {
			t.Fatalf("after reset: %v", ev.data)
		}
		stop()
	}
	// From an event the ring still has (21 and 22 were published since): a
	// plain resume.
	evs, stop := readSSE(t, h.URL+"/events?token=tok-1234567890123456", map[string]string{"Last-Event-ID": "21"})
	defer stop()
	if ev := nextSSE(t, evs); ev.id != "22" {
		t.Fatalf("resume: got id %q %v", ev.id, ev.data)
	}
}

// A subscriber that falls behind loses its stream rather than events: it
// reconnects and resumes from the ring.
func TestBrokerKicksSlowSubscriber(t *testing.T) {
	b := newBroker(10000)
	_, ch, kick, _ := b.subscribe(0)
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
	backlog, _, _, gap := b.subscribe(int64(len(ch)))
	if gap || len(backlog) != 1 || backlog[0].id != int64(cap(ch)+1) {
		t.Fatalf("resume after kick: gap %v, %d events", gap, len(backlog))
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
	h := httptest.NewServer(s.HTTPHandler("tok-1234567890123456"))
	defer h.Close()
	call := func(method string, params any) map[string]any {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		req, _ := http.NewRequest("POST", h.URL+"/rpc", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer tok-1234567890123456")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m msg
		json.NewDecoder(resp.Body).Decode(&m)
		if m.Error != nil {
			t.Fatalf("%s: %s", method, m.Error.Message)
		}
		return m.Result
	}
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
	evs, stop := readSSE(t, fmt.Sprintf("%s/events?token=tok-1234567890123456&lastEventId=%d", h.URL, from), nil)
	defer stop()
	for {
		ev := nextSSE(t, evs)
		p, _ := ev.data["params"].(map[string]any)
		switch ev.data["method"] {
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

// The replay ring is bounded by bytes too: big events push old ones out,
// and a client behind them gets a gap (and reads the thread again).
func TestBrokerKeepsBytesBounded(t *testing.T) {
	b := newBroker(10000)
	big := strings.Repeat("x", keepBytes/4)
	for range 20 {
		b.publish(map[string]any{"text": big})
	}
	if b.bytes > keepBytes || len(b.ring) == 0 || len(b.ring) > 5 {
		t.Fatalf("ring holds %d events, %d bytes", len(b.ring), b.bytes)
	}
	if _, _, _, gap := b.subscribe(1); !gap {
		t.Fatal("a client behind the ring must get a gap")
	}
}

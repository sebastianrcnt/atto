package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/sebastianrcnt/atto/session"
)

// flakyServer answers the first request with a 400 and the rest with "ok",
// and records each request's messages.
func flakyServer(t *testing.T) (*httptest.Server, func() [][]map[string]any) {
	var mu sync.Mutex
	var seen [][]map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		seen = append(seen, body.Messages)
		first := len(seen) == 1
		mu.Unlock()
		if first {
			http.Error(w, `{"error":{"message":"bad request"}}`, http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, func() [][]map[string]any { mu.Lock(); defer mu.Unlock(); return seen }
}

func userMessages(msgs []map[string]any) (out []string) {
	for _, m := range msgs {
		if m["role"] == "user" {
			out = append(out, m["content"].(string))
		}
	}
	return out
}

// Sending the same message again after a turn that failed before any reply
// does not add it to the conversation (or the session) twice, whatever notes
// the first attempt had added to it.
func TestResendAfterFailedTurnAddsNoCopy(t *testing.T) {
	srv, seen := flakyServer(t)
	a := newTestAgent(srv.URL)
	var rec []session.Entry
	a.Record = func(e session.Entry) { rec = append(rec, e) }
	run := func(input, note string) error {
		a.SetInputNote(note)
		return a.Run(context.Background(), input, func(any) {})
	}
	if err := run("hello", "[note]"); err == nil {
		t.Fatal("the first request should fail")
	}
	if err := run("hello", ""); err != nil {
		t.Fatal(err)
	}
	reqs := seen()
	if len(reqs) != 2 {
		t.Fatalf("%d requests", len(reqs))
	}
	if got := userMessages(reqs[1]); len(got) != 1 || got[0] != "hello\n\n[note]" {
		t.Fatalf("second request's user messages: %q", got)
	}
	users := 0
	for _, e := range rec {
		if e.Message != nil && e.Message.Role == "user" {
			users++
		}
	}
	if users != 1 || len(a.messages) != 2 {
		t.Fatalf("session has %d user entries, conversation %d messages", users, len(a.messages))
	}
	// Answered: the same words again are a new message.
	if err := run("hello", ""); err != nil {
		t.Fatal(err)
	}
	if len(a.messages) != 4 {
		t.Fatalf("conversation has %d messages, want 4", len(a.messages))
	}
}

// A different message after a failed turn is added, and the request carries
// both as one user message.
func TestOtherInputAfterFailedTurnIsAdded(t *testing.T) {
	srv, seen := flakyServer(t)
	a := newTestAgent(srv.URL)
	if err := a.Run(context.Background(), "first", func(any) {}); err == nil {
		t.Fatal("the first request should fail")
	}
	if err := a.Run(context.Background(), "second", func(any) {}); err != nil {
		t.Fatal(err)
	}
	if got := userMessages(seen()[1]); len(got) != 1 || got[0] != "first\n\nsecond" {
		t.Fatalf("second request's user messages: %q", got)
	}
	if len(a.messages) != 3 || a.messages[0].Content != "first" || a.messages[1].Content != "second" {
		t.Fatalf("history changed: %+v", a.messages)
	}
}

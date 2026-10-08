package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

// scriptedServer answers the n-th request with replies[n] (the last one
// repeats): an HTTP status and body, or 200 and an SSE stream.
func scriptedServer(t *testing.T, replies ...func(w http.ResponseWriter)) (*httptest.Server, func() int) {
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		i := min(n, len(replies)-1)
		n++
		mu.Unlock()
		replies[i](w)
	}))
	t.Cleanup(srv.Close)
	return srv, func() int { mu.Lock(); defer mu.Unlock(); return n }
}

func status(code int, msg string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, msg), code)
	}
}

func sse(chunks ...string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		for _, c := range chunks {
			fmt.Fprint(w, "data: "+c+"\n\n")
		}
	}
}

// cutOff is a reply the provider drops mid-stream: text, no finish_reason.
var cutOff = sse(`{"choices":[{"delta":{"content":"partial rep"}}]}`)

var okReply = sse(`{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`, "[DONE]")

func noWait(t *testing.T) {
	old := retryWait
	retryWait = func(error, int, time.Duration) (time.Duration, error) { return time.Millisecond, nil }
	t.Cleanup(func() { retryWait = old })
}

// A reply cut off mid-stream, or a 5xx, is sent again within the turn, and
// what streamed before the cut is not kept.
func TestTurnRetriesPassingFailures(t *testing.T) {
	noWait(t)
	srv, count := scriptedServer(t, cutOff, status(503, "overloaded"), okReply)
	a := newTestAgent(srv.URL)
	var retries []StreamRetry
	var rec []session.Entry
	a.Record = func(e session.Entry) { rec = append(rec, e) }
	err := a.Run(context.Background(), "go", func(ev any) {
		if r, ok := ev.(StreamRetry); ok {
			retries = append(retries, r)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if count() != 3 || len(retries) != 2 || retries[0].Attempt != 1 || retries[1].Attempt != 2 {
		t.Fatalf("%d requests, retries %+v", count(), retries)
	}
	for _, e := range rec {
		if e.Message != nil && e.Message.Role == "assistant" && e.Message.Content != "done" {
			t.Fatalf("kept a cut-off reply: %q", e.Message.Content)
		}
	}
}

// A failure a retry cannot fix fails the turn at once.
func TestTurnDoesNotRetryPermanentFailures(t *testing.T) {
	noWait(t)
	for _, reply := range []func(http.ResponseWriter){
		status(401, "invalid api key"),
		status(400, "invalid request: tools must be an array"),
		status(402, "insufficient_quota"),
	} {
		srv, count := scriptedServer(t, reply, okReply)
		a := newTestAgent(srv.URL)
		if err := a.Run(context.Background(), "go", func(any) {}); err == nil || count() != 1 {
			t.Fatalf("err %v after %d requests", err, count())
		}
	}
}

// Retries stop after streamRetries, and the turn fails with the last error.
func TestTurnRetriesAreBounded(t *testing.T) {
	noWait(t)
	srv, count := scriptedServer(t, status(500, "boom"))
	a := newTestAgent(srv.URL)
	err := a.Run(context.Background(), "go", func(any) {})
	if err == nil || !strings.Contains(err.Error(), "boom") || count() != streamRetries+1 {
		t.Fatalf("err %v after %d requests", err, count())
	}
}

// A request that no longer fits is compacted once and sent again.
func TestTurnCompactsOnContextOverflow(t *testing.T) {
	noWait(t)
	notes := sse(`{"choices":[{"delta":{"content":"notes"},"finish_reason":"stop"}]}`, "[DONE]")
	srv, count := scriptedServer(t, status(400, "This model's maximum context length is 1000 tokens"), notes, okReply)
	a := newTestAgent(srv.URL)
	a.messages = append(a.messages, provider.Message{Role: "user", Content: "earlier"},
		provider.Message{Role: "assistant", Content: "earlier answer"})
	compacted := false
	err := a.Run(context.Background(), "go", func(ev any) {
		if _, ok := ev.(CompactEnd); ok {
			compacted = true
		}
	})
	if err != nil || !compacted || count() != 3 {
		t.Fatalf("err %v, compacted %v, %d requests", err, compacted, count())
	}
}

func TestTurnDoesNotRetryLongProviderDelay(t *testing.T) {
	srv, count := scriptedServer(t, func(w http.ResponseWriter) {
		w.Header().Set("Retry-After", "3600")
		status(429, "rate limited")(w)
	}, okReply)
	a := newTestAgent(srv.URL)
	retries := 0
	err := a.Run(context.Background(), "go", func(ev any) {
		if _, ok := ev.(StreamRetry); ok {
			retries++
		}
	})
	if err == nil || !strings.Contains(err.Error(), "3600s retry delay") || count() != 1 || retries != 0 {
		t.Fatalf("err %v, %d requests, %d retries", err, count(), retries)
	}
}

func TestCompactionDoesNotConsumeStreamRetry(t *testing.T) {
	noWait(t)
	notes := sse(`{"choices":[{"delta":{"content":"notes"},"finish_reason":"stop"}]}`, "[DONE]")
	replies := []func(http.ResponseWriter){status(400, "This model's maximum context length is 1000 tokens"), notes}
	for range streamRetries {
		replies = append(replies, status(503, "overloaded"))
	}
	replies = append(replies, okReply)
	srv, count := scriptedServer(t, replies...)
	a := newTestAgent(srv.URL)
	a.messages = append(a.messages, provider.Message{Role: "user", Content: "earlier"},
		provider.Message{Role: "assistant", Content: "earlier answer"})
	var retries []StreamRetry
	err := a.Run(context.Background(), "go", func(ev any) {
		if r, ok := ev.(StreamRetry); ok {
			retries = append(retries, r)
		}
	})
	if err != nil || count() != streamRetries+3 || len(retries) != streamRetries+1 {
		t.Fatalf("err %v, %d requests, retries %+v", err, count(), retries)
	}
	if retries[0].Attempt != 1 || retries[0].Of != 1 {
		t.Fatalf("compaction retry: %+v", retries[0])
	}
	for i, r := range retries[1:] {
		if r.Attempt != i+1 || r.Of != streamRetries {
			t.Fatalf("ordinary retry %d: %+v", i, r)
		}
	}
}

func lengthReply(tokens int) []string {
	return []string{fmt.Sprintf(`{"choices":[{"delta":{"content":"truncated reply"},"finish_reason":"length"}],"usage":{"prompt_tokens":100,"completion_tokens":%d}}`, tokens)}
}

func TestTurnCompactsOnEarlyLength(t *testing.T) {
	for _, tools := range []bool{false, true} {
		t.Run(fmt.Sprint("tools=", tools), func(t *testing.T) {
			reply := lengthReply(10)
			if tools {
				reply = append(toolCallFinish("echo should-not-run", "length"), reply...)
			}
			srv, seen := fakeServer(t, reply, text("notes"), text("done"))
			a := newTestAgent(srv.URL)
			a.model.Model.MaxTokens = 1000
			var rec []session.Entry
			a.Record = func(e session.Entry) { rec = append(rec, e) }
			starts, retries := 0, 0
			err := a.Run(context.Background(), "go", func(ev any) {
				switch ev.(type) {
				case ToolStart:
					starts++
				case StreamRetry:
					retries++
				}
			})
			if err != nil || len(seen()) != 3 || starts != 0 || retries != 1 {
				t.Fatalf("err %v, requests %d, starts %d, retries %d", err, len(seen()), starts, retries)
			}
			for _, req := range seen()[1:] {
				for _, m := range req {
					if m["role"] == "assistant" || m["role"] == "tool" {
						t.Fatalf("replayed truncated reply: %v", m)
					}
				}
			}
			for _, e := range rec {
				if e.Message != nil && e.Message.Role == "assistant" && e.Message.Content != "done" {
					t.Fatalf("saved truncated reply: %+v", e.Message)
				}
			}
		})
	}
}

func TestTurnDoesNotCompactNormalLength(t *testing.T) {
	for _, tokens := range []int{0, 900, 1000} {
		t.Run(fmt.Sprint(tokens), func(t *testing.T) {
			srv, seen := fakeServer(t, lengthReply(tokens))
			a := newTestAgent(srv.URL)
			a.model.Model.MaxTokens = 1000
			err := a.Run(context.Background(), "go", func(any) {})
			if err != nil || len(seen()) != 1 || a.messages[len(a.messages)-1].Content != "truncated reply" {
				t.Fatalf("err %v, requests %d, messages %v", err, len(seen()), a.messages)
			}
		})
	}
}

func TestTurnContextRecoveryIsBounded(t *testing.T) {
	noWait(t)
	length := sse(lengthReply(10)...)
	overflow := status(400, "This model's maximum context length is 1000 tokens")
	notes := sse(text("notes")...)
	for _, c := range []struct {
		name          string
		first, second func(http.ResponseWriter)
		fails         bool
	}{
		{"length twice", length, length, false},
		{"overflow twice", overflow, overflow, true},
		{"length then overflow", length, overflow, true},
		{"overflow then length", overflow, length, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv, count := scriptedServer(t, c.first, notes, c.second)
			a := newTestAgent(srv.URL)
			a.model.Model.MaxTokens = 1000
			retries, compacts := 0, 0
			err := a.Run(context.Background(), "go", func(ev any) {
				switch ev.(type) {
				case StreamRetry:
					retries++
				case CompactEnd:
					compacts++
				}
			})
			if (err != nil) != c.fails || count() != 3 || retries != 1 || compacts != 1 {
				t.Fatalf("err %v, requests %d, retries %d, compacts %d", err, count(), retries, compacts)
			}
		})
	}
}

func TestTurnContextRecoveryBudgetSurvivesToolStep(t *testing.T) {
	noWait(t)
	overflow := status(400, "This model's maximum context length is 1000 tokens")
	srv, count := scriptedServer(t, overflow, sse(text("notes")...), sse(toolCall("echo ok")...), overflow)
	a := newTestAgent(srv.URL)
	compacts := 0
	err := a.Run(context.Background(), "go", func(ev any) {
		if _, ok := ev.(CompactEnd); ok {
			compacts++
		}
	})
	if err == nil || count() != 4 || compacts != 1 {
		t.Fatalf("err %v, requests %d, compacts %d", err, count(), compacts)
	}
}

func TestTurnContextRecoveryBudgetResets(t *testing.T) {
	srv, seen := fakeServer(t, lengthReply(10), text("notes"), text("done"),
		lengthReply(10), text("more notes"), text("done again"))
	a := newTestAgent(srv.URL)
	a.model.Model.MaxTokens = 1000
	compacts := 0
	for range 2 {
		if err := a.Run(context.Background(), "go", func(ev any) {
			if _, ok := ev.(CompactEnd); ok {
				compacts++
			}
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen()) != 6 || compacts != 2 {
		t.Fatalf("requests %d, compacts %d", len(seen()), compacts)
	}
}

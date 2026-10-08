package ai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func shortStreamIdleTimeout(t *testing.T, timeout time.Duration) {
	t.Helper()
	old := streamIdleTimeout
	streamIdleTimeout = timeout
	t.Cleanup(func() { streamIdleTimeout = old })
}

func TestSSEIdleStreamTimesOut(t *testing.T) {
	shortStreamIdleTimeout(t, 50*time.Millisecond)
	for _, api := range []string{ApiOpenAICompletions, ApiOpenAIResponses, ApiOpenAICodexResponses} {
		t.Run(api, func(t *testing.T) {
			ended := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(ended)
				io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer srv.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			model := &Model{ID: "m", Api: api, Provider: "p", BaseURL: srv.URL}
			began := time.Now()
			_, msg := collect(StreamSimple(model, ctxWithUser("hi"), &SimpleStreamOptions{Context: ctx, APIKey: fakeJWT("acct")}))
			if msg.StopReason != StopError || !errors.Is(msg.ErrorCause, ErrStreamStalled) ||
				!strings.Contains(msg.ErrorMessage, "no response bytes received for 50ms") || ctx.Err() != nil {
				t.Fatalf("stalled stream: %+v, context %v", msg, ctx.Err())
			}
			if elapsed := time.Since(began); elapsed > time.Second {
				t.Fatalf("stream stayed blocked for %s", elapsed)
			}
			select {
			case <-ended:
			case <-time.After(time.Second):
				t.Fatal("stalled request did not close the server connection")
			}
		})
	}
}

func TestSSEPartialBytesKeepStreamAlive(t *testing.T) {
	shortStreamIdleTimeout(t, 100*time.Millisecond)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		// No complete event arrives for longer than the timeout; raw
		// bytes, including comments and partial lines, must reset it.
		for _, b := range []byte(": heartbeat\n\ndata: done\n\n") {
			if _, err := w.Write([]byte{b}); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-time.After(10 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
	}))
	defer srv.Close()
	resp, err := postJSON(t.Context(), nil, srv.URL, []byte("{}"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var data string
	if err := readSSE(resp.Body, func(ev sseEvent) (bool, error) { data = ev.Data; return false, nil }); err != nil || data != "done" {
		t.Fatalf("live byte stream failed: %q %v", data, err)
	}
}

func TestSSEProcessingPauseDoesNotStall(t *testing.T) {
	shortStreamIdleTimeout(t, 20*time.Millisecond)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		fmt.Fprint(w, "data: one\n\ndata: two\n\n")
	}))
	defer srv.Close()
	resp, err := postJSON(t.Context(), nil, srv.URL, []byte("{}"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got []string
	err = readSSE(resp.Body, func(ev sseEvent) (bool, error) {
		got = append(got, ev.Data)
		time.Sleep(50 * time.Millisecond)
		return true, nil
	})
	if err != nil || strings.Join(got, ",") != "one,two" {
		t.Fatalf("consumer pause triggered a stall: %q %v", got, err)
	}
}

func TestSSEContextCancellationIsNotStall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	resp, err := postJSON(ctx, nil, srv.URL, []byte("{}"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	cancel()
	if _, err := io.ReadAll(resp.Body); !errors.Is(err, context.Canceled) || errors.Is(err, ErrStreamStalled) {
		t.Fatalf("canceled stream: %v", err)
	}
}

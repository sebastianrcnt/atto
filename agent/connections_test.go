package agent

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/ai"
	"github.com/sebastianrcnt/atto/provider"
)

// A stream may stop cleanly at the HTTP layer but before finish_reason.
// Its connection is still pooled: the retry must retire it explicitly.
func TestIncompleteStreamRetryDialsFresh(t *testing.T) {
	noWait(t)
	resetRequestLog(t)
	srv, count := scriptedServer(t, okReply, cutOff, okReply)
	a := newTestAgent(srv.URL)
	for _, input := range []string{"warm the pool", "go"} {
		if err := a.Run(context.Background(), input, func(any) {}); err != nil {
			t.Fatal(err)
		}
	}
	log := readRequestLog(t)
	if count() != 3 || len(log) != 2 || log[0].Conn == nil || !log[0].Conn.Reused ||
		log[1].Event != requestRecovered || log[1].Conn == nil || log[1].Conn.Reused {
		t.Fatalf("retry did not replace its pooled connection: %+v", log)
	}
}

func TestClosedStreamRetryRetiresIdleConnections(t *testing.T) {
	noWait(t)
	resetRequestLog(t)
	defer ai.ResetConnections()
	probeServer, _ := scriptedServer(t, okReply)
	probe, req := newTestAgent(probeServer.URL).request()
	probeConn := func() ai.ConnInfo {
		t.Helper()
		ctx, trace := ai.WithConnTrace(context.Background())
		if _, err := probe.Stream(ctx, req, provider.Handler{}); err != nil {
			t.Fatal(err)
		}
		info, ok := trace.Last()
		if !ok {
			t.Fatal("no probe connection trace")
		}
		return info
	}
	probeConn() // another idle connection in the shared model pool
	if info := probeConn(); !info.Reused {
		t.Fatal("probe did not establish an idle pooled connection")
	}
	cut := func(w http.ResponseWriter) {
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		chunk := "data: " + `{"choices":[{"delta":{"content":"partial"}}]}` + "\n\n"
		fmt.Fprintf(rw, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n", len(chunk), chunk)
		rw.Flush() // no terminating chunk: the next read gets unexpected EOF
	}
	srv, count := scriptedServer(t, okReply, cut, okReply)
	a := newTestAgent(srv.URL)
	for _, input := range []string{"warm the pool", "go"} {
		if err := a.Run(context.Background(), input, func(any) {}); err != nil {
			t.Fatal(err)
		}
	}
	log := readRequestLog(t)
	if count() != 3 || len(log) != 2 || !strings.Contains(log[0].Error, "unexpected EOF") ||
		log[0].Conn == nil || !log[0].Conn.Reused || log[1].Conn == nil || log[1].Conn.Reused {
		t.Fatalf("mid-stream disconnect retry: %+v", log)
	}
	// Closing the failed socket alone would not retire this other idle
	// connection. This checks that the retry called ResetConnections.
	if info := probeConn(); info.Reused {
		t.Fatal("connection-error retry left the old idle pool alive")
	}
}

func TestProviderRetryKeepsHealthyConnections(t *testing.T) {
	noWait(t)
	resetRequestLog(t)
	srv, _ := scriptedServer(t, okReply, status(http.StatusServiceUnavailable, "overloaded"), okReply)
	a := newTestAgent(srv.URL)
	for _, input := range []string{"warm the pool", "go"} {
		if err := a.Run(context.Background(), input, func(any) {}); err != nil {
			t.Fatal(err)
		}
	}
	log := readRequestLog(t)
	if len(log) != 2 || log[0].Conn == nil || !log[0].Conn.Reused || log[1].Conn == nil || !log[1].Conn.Reused {
		t.Fatalf("HTTP retry discarded a healthy connection: %+v", log)
	}
}

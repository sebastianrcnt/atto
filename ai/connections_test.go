package ai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestModelTransportSettings(t *testing.T) {
	tr := newModelTransport()
	defer tr.CloseIdleConnections()
	def := http.DefaultTransport.(*http.Transport)
	if tr == def || tr.IdleConnTimeout != 30*time.Second || tr.HTTP2 == nil ||
		tr.HTTP2.SendPingTimeout != 15*time.Second || tr.HTTP2.PingTimeout != 5*time.Second {
		t.Fatalf("model transport: %+v", tr)
	}
	if tr.Proxy == nil || tr.DialContext == nil || tr.ForceAttemptHTTP2 != def.ForceAttemptHTTP2 ||
		tr.TLSHandshakeTimeout != def.TLSHandshakeTimeout || tr.ExpectContinueTimeout != def.ExpectContinueTimeout ||
		tr.MaxIdleConns != def.MaxIdleConns || tr.DisableKeepAlives != def.DisableKeepAlives {
		t.Fatal("model transport lost the default proxy, dialing or pool settings")
	}
	if modelClient.Transport != modelTransport {
		t.Fatal("model client does not use the shared transport")
	}
}

func TestIsConnectionError(t *testing.T) {
	for _, err := range []error{
		io.ErrUnexpectedEOF,
		syscall.ECONNRESET,
		syscall.EPIPE,
		&net.OpError{Op: "read", Net: "tcp", Err: errors.New("lost connection")},
		context.DeadlineExceeded,
		ErrStreamStalled,
		errors.New("stream error: stream ID 1; INTERNAL_ERROR; received from peer"),
		errors.New("http2: server sent GOAWAY and closed the connection"),
		errors.New("http2: client connection lost"),
		errors.New("connection error: PROTOCOL_ERROR"),
		errors.New("Stream ended without finish_reason"),
		errors.New("OpenAI Responses stream ended without a stop reason"),
	} {
		if !IsConnectionError(err) || !IsConnectionError(fmt.Errorf("request: %w", err)) {
			t.Errorf("not a connection error: %v", err)
		}
	}
	for _, err := range []error{nil, context.Canceled, io.EOF, errors.New("overloaded"),
		&ProviderError{Status: 503, Body: "http2: upstream connection failed"},
		errors.New("http2: unsupported request parameter")} {
		if IsConnectionError(err) {
			t.Errorf("not a broken connection: %v", err)
		}
	}
}

func TestResetConnectionsRetiresIdlePool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		fmt.Fprint(w, "done")
	}))
	defer srv.Close()
	defer ResetConnections()
	request := func() ConnInfo {
		t.Helper()
		ctx, trace := WithConnTrace(t.Context())
		resp, err := postJSON(ctx, nil, srv.URL, []byte("{}"), "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadAll(resp.Body); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		info, ok := trace.Last()
		if !ok {
			t.Fatal("no connection trace")
		}
		return info
	}
	if first := request(); first.Reused {
		t.Fatal("new server reused a connection")
	}
	if pooled := request(); !pooled.Reused {
		t.Fatal("successful requests did not share their pool")
	}
	ResetConnections()
	if next := request(); next.Reused {
		t.Fatal("reset reused an idle connection")
	}
}

func TestResetConnectionsLeavesActiveStreamAlone(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-release:
			fmt.Fprint(w, "done")
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer ResetConnections()
	resp, err := postJSON(t.Context(), nil, srv.URL, []byte("{}"), "")
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var resets sync.WaitGroup
	for range 10 {
		resets.Go(ResetConnections)
	}
	resets.Wait()
	close(release)
	if body, err := io.ReadAll(resp.Body); err != nil || string(body) != "done" {
		t.Fatalf("reset interrupted an active stream: %q %v", body, err)
	}
}

func TestExplicitClientKeepsItsOwnPool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		fmt.Fprint(w, "done")
	}))
	defer srv.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	for i := range 2 {
		ctx, trace := WithConnTrace(t.Context())
		resp, err := postJSON(ctx, client, srv.URL, []byte("{}"), "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadAll(resp.Body); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if info, ok := trace.Last(); !ok || info.Reused != (i == 1) {
			t.Fatalf("custom client attempt %d: %+v %v", i, info, ok)
		}
		ResetConnections() // the explicit client's transport is not ours
	}
}

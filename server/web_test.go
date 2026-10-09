package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/provider/providertest"
	"github.com/sebastianrcnt/atto/ui"
)

func TestWebAuthAndAssets(t *testing.T) {
	s, _ := testServer(t)
	h := httptest.NewServer(s.WebHandler("secret", nil))
	defer h.Close()
	for _, path := range []string{"/", "/app.js", "/app.css"} {
		r, e := http.Get(h.URL + path)
		if e != nil {
			t.Fatal(e)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != 200 || len(b) == 0 {
			t.Fatal(path, r.Status)
		}
		for _, header := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy", "X-Frame-Options"} {
			if r.Header.Get(header) == "" {
				t.Fatal(header)
			}
		}
		csp := r.Header.Get("Content-Security-Policy")
		if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") || strings.Contains(string(b), "secret") {
			t.Fatal("unsafe assets")
		}
	}
	for _, tc := range []struct {
		path, token, origin string
		want                int
	}{{"/ws", "", "", 401}, {"/ws", "wrong", "", 401}, {"/ws", "secret", "https://evil.example", 403}, {"/ws?token=secret", "secret", "", 400}, {"/ws", "secret", "null", 403}, {"/ws", "secret", h.URL, 101}} {
		headers := http.Header{"Sec-WebSocket-Protocol": {"atto.rpc.v3, atto.auth." + tc.token}}
		if tc.origin != "" {
			headers.Set("Origin", tc.origin)
		}
		c := dialWS(t, h.URL+tc.path, headers, tc.want)
		if c != nil {
			result := c.rpc(t, 1, "initialize", map[string]any{"protocolVersions": []int{3}, "capabilities": map[string]any{"interactive": true, "ui": ui.Capabilities{Version: 1, Surface: "web", Width: 40, Elements: ui.Catalog()}}})
			if result["clientId"] == nil {
				t.Fatal(result)
			}
		}
	}
	for _, path := range []string{"/api", "/dist/app.js", "/../go.mod", "/service-worker.js"} {
		r, e := http.Get(h.URL + path)
		if e != nil {
			t.Fatal(e)
		}
		r.Body.Close()
		if r.StatusCode != 404 {
			t.Fatal(path, r.Status)
		}
	}
}
func TestWebListenerBootstrapAndDetach(t *testing.T) {
	gate := make(chan struct{})
	s, m := testServer(t, providertest.Reply{Text: "still running", Gate: gate})
	ctx := t.Context()
	l, e := s.ListenWeb(ctx, "ws://0.0.0.0:0", nil)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	u, e := url.Parse(l.URL)
	if e != nil {
		t.Fatal(e)
	}
	token := strings.TrimPrefix(u.Fragment, "token=")
	if token == "" || !l.Public || u.RawQuery != "" {
		t.Fatal("bad bootstrap link")
	}
	u.Fragment = ""
	u.Host = strings.ReplaceAll(u.Host, "[::]", "127.0.0.1")
	u.Host = strings.ReplaceAll(u.Host, "0.0.0.0", "127.0.0.1")
	u.Path = "/ws"
	headers := http.Header{"Origin": {u.Scheme + "://" + u.Host}, "Sec-WebSocket-Protocol": {"atto.rpc.v3, atto.auth." + token}}
	c := dialWS(t, u.String(), headers, 101)
	c.rpc(t, 1, "initialize", map[string]any{"protocolVersions": []int{3}, "capabilities": map[string]any{"interactive": true}})
	info := c.rpc(t, 2, "thread/start", nil)
	id := info["threadId"].(string)
	c.rpc(t, 3, "input/submit", map[string]any{"threadId": id, "input": "hello", "intent": "auto"})
	m.Started(5 * time.Second)
	l.Close()
	if e = l.Wait(); e != nil {
		t.Fatal(e)
	}
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, e = io.Copy(io.Discard, c.r); e != nil {
		if n, ok := e.(interface{ Timeout() bool }); ok && n.Timeout() {
			t.Fatal("web client not detached")
		}
	}
	// The listener has no ownership of work. A second transport attaches to the
	// same runtime, observes the active turn, and sees completion after release.
	client := Connect(context.Background(), s)
	defer client.Close()
	var snap ThreadInfo
	if e = client.Call(context.Background(), "thread/attach", map[string]any{"threadId": id}, &snap); e != nil {
		t.Fatal(e)
	}
	if !snap.Busy {
		t.Fatal("listener stopped work")
	}
	close(gate)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if e = client.Call(context.Background(), "thread/read", map[string]any{"threadId": id}, &snap); e != nil {
			t.Fatal(e)
		}
		if !snap.Busy {
			for _, i := range snap.Items {
				if i.Type == ItemAgent && i.Text == "still running" {
					return
				}
			}
			b, _ := json.Marshal(snap)
			t.Fatal(string(b))
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("turn never completed")
}
func TestWebListenAddressValidation(t *testing.T) {
	s, _ := testServer(t)
	for _, addr := range []string{"stdio://", "unix:///tmp/x", "ws://user@localhost:1", "http://localhost:0/path", "http://localhost:0?token=x"} {
		if l, e := s.ListenWeb(context.Background(), addr, nil); e == nil {
			l.Close()
			t.Fatal(addr)
		}
	}
}

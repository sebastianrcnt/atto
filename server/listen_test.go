package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

type listenerBanner struct{ lines chan string }

func (b listenerBanner) Write(p []byte) (int, error) { b.lines <- string(p); return len(p), nil }

func TestWebSocketListen(t *testing.T) {
	for _, public := range []bool{false, true} {
		t.Run(map[bool]string{false: "loopback", true: "public"}[public], func(t *testing.T) {
			s, _ := testServer(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			lines := make(chan string, 4)
			addr := "ws://127.0.0.1:0"
			if public {
				addr = "ws://0.0.0.0:0"
			}
			go func() { done <- s.ServeListen(ctx, addr, nil, listenerBanner{lines}) }()
			var address string
			select {
			case line := <-lines:
				address = strings.TrimSpace(strings.TrimPrefix(line, "atto app-server listening on "))
			case <-time.After(5 * time.Second):
				t.Fatal("no banner")
			}
			headers := http.Header{}
			if public {
				var line string
				select {
				case line = <-lines:
				case <-time.After(5 * time.Second):
					t.Fatal("no token")
				}
				if !strings.Contains(line, TLSWarning) {
					t.Fatal("missing TLS warning")
				}
				token := strings.Fields(line)[2]
				headers.Set("Authorization", "Bearer "+token)
				address = strings.Replace(address, "0.0.0.0", "127.0.0.1", 1)
				dialWS(t, address, nil, 401)
			}
			c := dialWS(t, address, headers, 101)
			c.rpc(t, 1, "initialize", map[string]any{"protocolVersions": []int{3}})
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("listener did not stop")
			}
			if _, err := c.r.ReadByte(); err == nil {
				t.Fatal("socket left open")
			}
		})
	}
}

func TestAllowOriginFlag(t *testing.T) {
	var f originFlags
	for _, a := range []string{"*", "null", "file:///tmp/a", "http://localhost/path", "http://a?x=y", "https://u@a"} {
		if err := f.Set(a); err == nil {
			t.Fatalf("accepted %s", a)
		}
	}
	if err := f.Set("http://localhost:8000"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("https://example.test"); err != nil || len(f) != 2 {
		t.Fatal(f, err)
	}
}

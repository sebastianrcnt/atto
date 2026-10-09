//go:build !windows

package server

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUnixListenLifecycle(t *testing.T) {
	s, _ := testServer(t)
	path := filepath.Join(t.TempDir(), "rpc.sock")
	// Keep below macOS's sockaddr_un length limit.
	if len(path) > 100 {
		path = filepath.Join(os.TempDir(), "atto-rpc-"+s.instance+".sock")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	ready := make(chan string, 1)
	go func() { done <- s.ServeListen(ctx, "unix://"+path, nil, listenerBanner{ready}) }()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("no listener")
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("socket: %v %v", st, err)
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(conn)
	defer c.Close()
	var init map[string]any
	if err := c.Call(ctx, "initialize", map[string]any{"protocolVersions": []int{3}}, &init); err != nil || init["clientId"] == nil {
		t.Fatal(init, err)
	}
	// A second bind must fail, never unlink or replace the first socket.
	if err := s.ServeListen(ctx, "unix://"+path, nil, listenerBanner{ready}); err == nil {
		t.Fatal("replaced existing listener")
	}
	other, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	other.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("listener did not stop")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("socket left behind: %v", err)
	}
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("connection left behind")
	}
}

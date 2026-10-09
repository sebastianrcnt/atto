package app

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/server"
)

func TestRemoteOffClosesWebSocketNotRuntime(t *testing.T) {
	a := remoteApp(t, newRemoteModel(t))
	a.ui.Do(func() { a.cmdRemote("on") })
	link := a.remoteClient(t)
	link.must("turn/start", map[string]any{"input": "block"})
	within(t, a, "runtime started", func() bool { return a.busy })
	conn, err := net.Dial("tcp", strings.TrimPrefix(link.base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", link.base+"/ws?token="+link.token, nil)
	req.Header = http.Header{"Upgrade": {"websocket"}, "Connection": {"Upgrade"}, "Sec-Websocket-Version": {"13"}, "Sec-Websocket-Key": {"dGhlIHNhbXBsZSBub25jZQ=="}}
	if err := req.Write(conn); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 101 {
		t.Fatal(resp.Status)
	}
	within(t, a, "WS client counted", func() bool { return a.remote.clients == 1 })
	a.ui.Do(a.stopRemote)
	// A close frame or a notification may still come first; the
	// connection must then end before the deadline.
	if _, err := io.Copy(io.Discard, reader); err != nil {
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			t.Fatal("/remote off left WebSocket open")
		}
	}
	var snapshot server.ThreadInfo
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var id string
	a.ui.Do(func() { id = a.threadID })
	if err := a.conn.c.Call(ctx, "thread/read", map[string]any{"threadId": id}, &snapshot); err != nil || !snapshot.Busy {
		t.Fatalf("gateway close stopped runtime: %+v %v", snapshot, err)
	}
	if err := a.conn.c.Call(ctx, "turn/interrupt", map[string]any{"threadId": id, "mode": "cancel"}, nil); err != nil {
		t.Fatal(err)
	}
}

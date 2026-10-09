package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/provider/providertest"
)

// wsTestClient is a small independent RFC 6455 client, including masking.
type wsTestClient struct {
	net.Conn
	r *bufio.Reader
}

func dialWS(t *testing.T, address string, headers http.Header, want int) *wsTestClient {
	t.Helper()
	u, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	req, _ := http.NewRequest("GET", address, nil)
	req.Header = headers.Clone()
	if req.Header == nil {
		req.Header = http.Header{}
	}
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "keep-alive, Upgrade")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	if err := req.Write(c); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(c)
	resp, err := http.ReadResponse(r, req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != want {
		t.Fatalf("handshake: %s, want %d", resp.Status, want)
	}
	if want != 101 {
		resp.Body.Close()
		return nil
	}
	digest := sha1.Sum([]byte(req.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	if resp.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(digest[:]) {
		t.Fatal("incorrect accept")
	}
	return &wsTestClient{c, r}
}

func maskedFrame(fin bool, op byte, p []byte) []byte {
	h := []byte{op, 0x80}
	if fin {
		h[0] |= 0x80
	}
	switch {
	case len(p) < 126:
		h[1] |= byte(len(p))
	case len(p) <= 65535:
		h[1] |= 126
		h = binary.BigEndian.AppendUint16(h, uint16(len(p)))
	default:
		h[1] |= 127
		h = binary.BigEndian.AppendUint64(h, uint64(len(p)))
	}
	mask := []byte{1, 2, 3, 4}
	h = append(h, mask...)
	for i, b := range p {
		h = append(h, b^mask[i%4])
	}
	return h
}
func (c *wsTestClient) send(t *testing.T, fin bool, op byte, p []byte) {
	t.Helper()
	if _, err := c.Write(maskedFrame(fin, op, p)); err != nil {
		t.Fatal(err)
	}
}
func (c *wsTestClient) receive(t *testing.T) (byte, []byte) {
	t.Helper()
	var h [2]byte
	if _, err := io.ReadFull(c.r, h[:]); err != nil {
		t.Fatal(err)
	}
	if h[0]&0x80 == 0 || h[1]&0x80 != 0 {
		t.Fatal("server must send unmasked complete frames")
	}
	n := uint64(h[1])
	var ext [8]byte
	if n == 126 {
		if _, err := io.ReadFull(c.r, ext[:2]); err != nil {
			t.Fatal(err)
		}
		n = uint64(binary.BigEndian.Uint16(ext[:2]))
	} else if n == 127 {
		if _, err := io.ReadFull(c.r, ext[:]); err != nil {
			t.Fatal(err)
		}
		n = binary.BigEndian.Uint64(ext[:])
	}
	if n > maxWSMessage {
		t.Fatal("oversized server frame")
	}
	p := make([]byte, int(n))
	if _, err := io.ReadFull(c.r, p); err != nil {
		t.Fatal(err)
	}
	return h[0] & 15, p
}
func (c *wsTestClient) rpc(t *testing.T, id int, method string, params any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	c.send(t, true, 1, b)
	for {
		op, p := c.receive(t)
		if op != 1 {
			t.Fatalf("unexpected opcode %d", op)
		}
		var v map[string]any
		if err := json.Unmarshal(p, &v); err != nil {
			t.Fatal(err)
		}
		if v["id"] == float64(id) {
			if v["error"] != nil {
				t.Fatalf("RPC %s: %s", method, p)
			}
			return v["result"].(map[string]any)
		}
	}
}

func TestWebSocketHandshakeAndSecurity(t *testing.T) {
	s := New("test", t.TempDir())
	t.Cleanup(s.Close)
	h := httptest.NewServer(s.WebSocketHandler("secret", []string{"https://example.test"}))
	t.Cleanup(h.Close)
	for _, tc := range []struct {
		name, token, origin string
		bearer              bool
		status              int
	}{
		{"no token", "", "", false, 401}, {"wrong token", "wrong", "", true, 401},
		{"query", "secret", "", false, 101}, {"header", "secret", "", true, 101},
		{"same host", "secret", h.URL, true, 101}, {"loopback", "secret", "http://localhost:4321", true, 101},
		{"ipv6 loopback", "secret", "http://[::1]:123", true, 101},
		{"evil", "secret", "https://evil.test", true, 403}, {"null", "secret", "null", true, 403},
		{"allowed", "secret", "https://example.test", true, 101},
		{"not origin", "secret", "http://localhost/path", true, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := http.Header{}
			address := h.URL + "/ws"
			if tc.bearer {
				headers.Set("Authorization", "Bearer "+tc.token)
			} else {
				address += "?token=" + tc.token
			}
			if tc.origin != "" {
				headers.Set("Origin", tc.origin)
			}
			dialWS(t, address, headers, tc.status)
		})
	}
	for _, tc := range []struct {
		key, value string
		status     int
	}{
		{"Upgrade", "wrong", 400}, {"Connection", "keep-alive", 400}, {"Sec-WebSocket-Version", "12", 426}, {"Sec-WebSocket-Key", "not base64", 400}, {"Sec-WebSocket-Key", "eA==", 400},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			req, _ := http.NewRequest("GET", h.URL+"/ws?token=secret", nil)
			req.Header = http.Header{"Upgrade": {"websocket"}, "Connection": {"Upgrade"}, "Sec-Websocket-Version": {"13"}, "Sec-Websocket-Key": {"dGhlIHNhbXBsZSBub25jZQ=="}}
			req.Header.Set(tc.key, tc.value)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Fatal(resp.Status)
			}
		})
	}
}

func TestWebSocketFrames(t *testing.T) {
	s := New("test", t.TempDir())
	t.Cleanup(s.Close)
	h := httptest.NewServer(s.WebSocketHandler("", nil))
	t.Cleanup(h.Close)
	c := dialWS(t, h.URL, nil, 101)
	c.send(t, false, 1, []byte("{\n\"id\":1,"))
	c.send(t, true, 9, []byte("ping"))
	op, p := c.receive(t)
	if op != 10 || string(p) != "ping" {
		t.Fatalf("pong %d %s", op, p)
	}
	c.send(t, true, 10, []byte("ignored"))
	c.send(t, true, 0, []byte("\"method\":\"initialize\",\"params\":{\"protocolVersions\":[3]}}"))
	op, p = c.receive(t)
	var reply struct {
		Result struct {
			ClientID string `json:"clientId"`
			Version  int    `json:"protocolVersion"`
		} `json:"result"`
	}
	if op != 1 || json.Unmarshal(p, &reply) != nil || reply.Result.ClientID == "" || reply.Result.Version != 3 {
		t.Fatalf("initialize: %d %s", op, p)
	}
	c.send(t, true, 1, []byte(`{"method":"initialized"}`))
	// Both extended-length encodings work and a frame is one JSON message.
	for _, size := range []int{200, 70000} {
		payload := []byte(fmt.Sprintf(`{"id":2,"method":"ping","params":{"padding":"%s"}}`, strings.Repeat("x", size)))
		c.send(t, true, 1, payload)
		op, p = c.receive(t)
		if op != 1 || !bytes.Contains(p, []byte(`"id":2`)) {
			t.Fatalf("large frame: %d %s", op, p)
		}
	}
	c.send(t, true, 1, []byte("{\"id\":3,\"method\":\"bad\nstring\"}"))
	_, p = c.receive(t)
	if !bytes.Contains(p, []byte(`"reason":"parseError"`)) {
		t.Fatalf("invalid JSON: %s", p)
	}
	c.send(t, true, 8, []byte{3, 232, 'b', 'y', 'e'})
	op, p = c.receive(t)
	if op != 8 || !bytes.Equal(p, []byte{3, 232, 'b', 'y', 'e'}) {
		t.Fatalf("close %d %v", op, p)
	}
	if _, err := c.r.ReadByte(); err == nil {
		t.Fatal("socket remained open")
	}
}

func TestWebSocketMalformedFrames(t *testing.T) {
	s := New("test", t.TempDir())
	t.Cleanup(s.Close)
	h := httptest.NewServer(s.WebSocketHandler("", nil))
	t.Cleanup(h.Close)
	for _, tc := range []struct {
		name  string
		frame []byte
		code  uint16
	}{
		{"unmasked", []byte{0x81, 0}, 1002}, {"RSV", []byte{0xc1, 0x80}, 1002},
		{"unknown opcode", []byte{0x83, 0x80}, 1002}, {"binary", []byte{0x82, 0x80}, 1003},
		{"orphan continuation", maskedFrame(true, 0, nil), 1002},
		{"interleaved text", append(maskedFrame(false, 1, []byte("a")), maskedFrame(true, 1, nil)...), 1002},
		{"fragmented control", maskedFrame(false, 9, nil), 1002}, {"long control", maskedFrame(true, 9, make([]byte, 126)), 1002},
		{"noncanonical16", []byte{0x81, 0xfe, 0, 1}, 1002}, {"noncanonical64", []byte{0x81, 0xff, 0, 0, 0, 0, 0, 0, 0, 1}, 1002},
		{"negative64", []byte{0x81, 0xff, 0x80, 0, 0, 0, 0, 0, 0, 0}, 1002},
		{"oversize", append([]byte{0x81, 0xff}, binary.BigEndian.AppendUint64(nil, maxWSMessage+1)...), 1009},
		{"invalid utf8", maskedFrame(true, 1, []byte{0xff}), 1007},
		{"short close", maskedFrame(true, 8, []byte{1}), 1002}, {"bad close code", maskedFrame(true, 8, []byte{3, 237}), 1002},
		{"bad close utf8", maskedFrame(true, 8, []byte{3, 232, 0xff}), 1007},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := dialWS(t, h.URL, nil, 101)
			if _, err := c.Write(tc.frame); err != nil {
				t.Fatal(err)
			}
			op, p := c.receive(t)
			if op != 8 || len(p) < 2 || binary.BigEndian.Uint16(p[:2]) != tc.code {
				t.Fatalf("close %d %v, want %d", op, p, tc.code)
			}
		})
	}
}

func TestWebSocketFragmentLimitAndUTF8(t *testing.T) {
	for _, tc := range []struct {
		name  string
		parts [][]byte
		limit int
		code  uint16
	}{
		{"aggregate", [][]byte{[]byte("12345"), []byte("67890")}, 9, 1009},
		{"split utf8", [][]byte{{'"', 0xe2}, {0x82, 0xac, '"'}}, 10, 0},
		{"exact limit", [][]byte{[]byte(`"12`), []byte(`34"`)}, 6, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			_ = a.SetDeadline(time.Now().Add(5 * time.Second))
			c := &wsConn{Conn: a, r: bufio.NewReader(a), limit: tc.limit}
			done := make(chan error, 1)
			go func() { _, err := c.message(); done <- err }()
			client := &wsTestClient{b, bufio.NewReader(b)}
			client.send(t, false, 1, tc.parts[0])
			client.send(t, true, 0, tc.parts[1])
			if tc.code != 0 {
				op, p := client.receive(t)
				if op != 8 || binary.BigEndian.Uint16(p) != tc.code {
					t.Fatal(op, p)
				}
			}
			if err := <-done; (err != nil) != (tc.code != 0) {
				t.Fatal(err)
			}
		})
	}
}

func TestWebSocketWorkerDetachAndSnapshot(t *testing.T) {
	gate := make(chan struct{})
	defer func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	}()
	worker, m := testServer(t, providertest.Reply{Text: "WebSocket answer", Gate: gate})
	gateway := workerFacade(t, worker)
	h := httptest.NewServer(gateway.WebSocketHandler("token", nil))
	t.Cleanup(h.Close)
	c := dialWS(t, h.URL+"/ws?token=token", nil, 101)
	init := c.rpc(t, 1, "initialize", map[string]any{"protocolVersions": []int{3}, "capabilities": map[string]any{"interactive": true}})
	c.send(t, true, 1, []byte(`{"method":"initialized"}`))
	thread := c.rpc(t, 2, "thread/start", nil)
	id := thread["threadId"].(string)
	c.rpc(t, 3, "turn/start", map[string]any{"threadId": id, "input": "web socket"})
	if m.Started(5*time.Second) == 0 {
		t.Fatal("no model call")
	}
	c.Close() // transport detach must not cancel work
	viewer := dialWS(t, h.URL+"/ws?token=token", nil, 101)
	if other := viewer.rpc(t, 1, "initialize", map[string]any{"protocolVersions": []int{3}}); other["clientId"] == init["clientId"] {
		t.Fatal("shared identity")
	}
	snapshot := viewer.rpc(t, 2, "thread/resume", map[string]any{"threadId": id})
	if snapshot["busy"] != true {
		t.Fatalf("detached turn stopped: %v", snapshot)
	}
	close(gate)
	var last int64
	for {
		op, p := viewer.receive(t)
		if op != 1 {
			t.Fatal(op)
		}
		var n Notification
		if err := json.Unmarshal(p, &n); err != nil {
			t.Fatal(err)
		}
		if n.EventID <= last {
			t.Fatalf("cursor not increasing: %d <= %d", n.EventID, last)
		}
		last = n.EventID
		if n.Method == "turn/completed" {
			break
		}
	}
	snapshot = viewer.rpc(t, 3, "thread/read", map[string]any{"threadId": id})
	b, _ := json.Marshal(snapshot)
	if !bytes.Contains(b, []byte("WebSocket answer")) {
		t.Fatalf("snapshot: %s", b)
	}
	if len(m.Requests()) != 1 {
		t.Fatal("duplicate execution")
	}
}

func TestWebSocketServerClose(t *testing.T) {
	s := New("test", t.TempDir())
	h := httptest.NewServer(s.WebSocketHandler("", nil))
	defer h.Close()
	c := dialWS(t, h.URL, nil, 101)
	c.rpc(t, 1, "initialize", map[string]any{"protocolVersions": []int{3}})
	s.Close()
	if _, err := c.r.ReadByte(); err == nil {
		t.Fatal("server close left upgraded connection open")
	}
}

func TestServeListenInvalid(t *testing.T) {
	s := New("test", t.TempDir())
	defer s.Close()
	for _, addr := range []string{"tcp://127.0.0.1:0", "ws://", "ws://user@localhost:1", "ws://localhost:1/rpc", "unix://relative", "unix:///tmp/a?token=x"} {
		if err := s.ServeListen(context.Background(), addr, nil, io.Discard); err == nil {
			t.Fatalf("accepted %s", addr)
		}
	}
}

func TestWebSocketPromptFirstAnswerWins(t *testing.T) {
	worker, _ := testServer(t)
	gateway := workerFacade(t, worker)
	h := httptest.NewServer(gateway.WebSocketHandler("token", nil))
	defer h.Close()
	owner := dialWS(t, h.URL+"/ws?token=token", nil, 101)
	other := dialWS(t, h.URL+"/ws?token=token", nil, 101)
	owner.rpc(t, 1, "initialize", map[string]any{"protocolVersions": []int{3}, "capabilities": map[string]bool{"interactive": true}})
	other.rpc(t, 1, "initialize", map[string]any{"protocolVersions": []int{3}, "capabilities": map[string]bool{"interactive": true}})
	id := owner.rpc(t, 2, "thread/start", nil)["threadId"].(string)
	other.rpc(t, 2, "thread/attach", map[string]any{"threadId": id})
	prompt := owner.rpc(t, 3, "prompt/clientOpen", map[string]any{"threadId": id, "prompt": Prompt{Kind: PromptSelect, RequestID: "ws-picker", Title: "Pick", Options: []PromptOption{{Label: "one"}, {Label: "two"}}}})
	other.rpc(t, 3, "prompt/answer", map[string]any{"threadId": id, "id": prompt["id"], "index": 1})
	raw, _ := json.Marshal(map[string]any{"id": 4, "method": "prompt/answer", "params": map[string]any{"threadId": id, "id": prompt["id"], "index": 0}})
	owner.send(t, true, 1, raw)
	for {
		_, p := owner.receive(t)
		var v struct {
			ID    int       `json:"id"`
			Error *RPCError `json:"error"`
		}
		if err := json.Unmarshal(p, &v); err != nil {
			t.Fatal(err)
		}
		if v.ID == 4 {
			if v.Error == nil || v.Error.Data.Reason != ReasonStalePrompt {
				t.Fatalf("late answer: %s", p)
			}
			break
		}
	}
	if snapshot := other.rpc(t, 4, "thread/read", map[string]any{"threadId": id}); snapshot["prompt"] != nil {
		t.Fatal("answered prompt remains open")
	}
}

func TestWebSocketTruncatedFrame(t *testing.T) {
	for _, raw := range [][]byte{{0x81}, {0x81, 0xfe, 0}, {0x81, 0x81, 1, 2}, {0x81, 0x82, 1, 2, 3, 4, 'a'}} {
		a, b := net.Pipe()
		done := make(chan error, 1)
		c := &wsConn{Conn: a, r: bufio.NewReader(a), limit: maxWSMessage}
		go func() { _, err := c.message(); done <- err }()
		if _, err := b.Write(raw); err != nil {
			t.Fatal(err)
		}
		b.Close()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("accepted incomplete frame")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("truncated frame blocked")
		}
		a.Close()
	}
}

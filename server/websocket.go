package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const maxWSMessage = 64 << 20

// WebSocketHandler serves JSON-RPC messages as text frames. Like ServeConn,
// each socket has its own identity and closing it only detaches the client.
// Empty token is for loopback-only listeners, never public listeners.
func (s *Server) WebSocketHandler(token string, allowOrigins []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" && !bearerOK(r, token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !wsOriginOK(r, allowOrigins) {
			http.Error(w, "origin forbidden", http.StatusForbidden)
			return
		}
		ws, err := upgradeWS(w, r)
		if err != nil {
			return
		}
		defer ws.Close()
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		go func() {
			select {
			case <-s.stop:
				cancel()
			case <-ctx.Done():
			}
		}()
		_ = s.ServeConn(ctx, ws)
	})
}

func bearerOK(r *http.Request, token string) bool {
	got := r.URL.Query().Get("token")
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		got = strings.TrimPrefix(h, "Bearer ")
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

func wsOriginOK(r *http.Request, allowed []string) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	} // non-browser clients
	if slices.Contains(allowed, origin) {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	return strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
}

func headerToken(h, want string) bool {
	for part := range strings.SplitSeq(h, ",") {
		if strings.EqualFold(strings.TrimSpace(part), want) {
			return true
		}
	}
	return false
}

func upgradeWS(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	reject := func(code int, msg string) (*wsConn, error) { http.Error(w, msg, code); return nil, errors.New(msg) }
	if r.Method != "GET" || r.ProtoMajor != 1 || r.ProtoMinor < 1 || !headerToken(strings.Join(r.Header.Values("Connection"), ","), "upgrade") || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return reject(http.StatusBadRequest, "expected WebSocket upgrade")
	}
	if r.Header.Get("Sec-WebSocket-Version") != "13" {
		w.Header().Set("Sec-WebSocket-Version", "13")
		return reject(http.StatusUpgradeRequired, "WebSocket version must be 13")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(raw) != 16 {
		return reject(http.StatusBadRequest, "invalid WebSocket key")
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return reject(http.StatusInternalServerError, "upgrade unsupported")
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, err
	}
	digest := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	_, err = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(digest[:]) + "\r\n\r\n")
	if err == nil {
		err = rw.Flush()
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &wsConn{Conn: conn, r: rw.Reader, limit: maxWSMessage}, nil
}

// wsConn adapts complete WebSocket messages to ServeConn's JSON lines.
// It never exposes frame boundaries or control messages to the dispatcher.
type wsConn struct {
	net.Conn
	r       *bufio.Reader
	pending []byte
	limit   int
	mu      sync.Mutex // writes of notifications, replies and control frames
}

func (c *wsConn) frame(op byte, p []byte) error { return c.frameFragment(op, p, true) }

func (c *wsConn) frameFragment(op byte, p []byte, fin bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	defer c.SetWriteDeadline(time.Time{})
	var h [10]byte
	h[0] = op
	if fin {
		h[0] |= 0x80
	}
	n := 2
	switch {
	case len(p) < 126:
		h[1] = byte(len(p))
	case len(p) <= 65535:
		h[1] = 126
		binary.BigEndian.PutUint16(h[2:4], uint16(len(p)))
		n = 4
	default:
		h[1] = 127
		binary.BigEndian.PutUint64(h[2:], uint64(len(p)))
		n = 10
	}
	if _, err := io.Copy(c.Conn, io.MultiReader(bytes.NewReader(h[:n]), bytes.NewReader(p))); err != nil {
		c.Conn.Close() // a partial frame cannot be retried on this connection
		return err
	}
	return nil
}

func (c *wsConn) Write(p []byte) (int, error) {
	if err := c.frame(1, bytes.TrimSuffix(p, []byte{'\n'})); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *wsConn) fail(code uint16) error {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], code)
	_ = c.frame(8, b[:])
	return errors.New("invalid WebSocket message")
}

func validClose(code uint16) bool {
	return code >= 3000 && code <= 4999 || code >= 1000 && code <= 1014 && code != 1004 && code != 1005 && code != 1006
}

func (c *wsConn) message() ([]byte, error) {
	var message []byte
	fragmented := false
	for {
		var h [2]byte
		if _, err := io.ReadFull(c.r, h[:]); err != nil {
			return nil, err
		}
		fin, op := h[0]&0x80 != 0, h[0]&15
		if h[0]&0x70 != 0 || h[1]&0x80 == 0 {
			return nil, c.fail(1002)
		}
		n := uint64(h[1] & 127)
		var ext [8]byte
		switch n {
		case 126:
			if _, err := io.ReadFull(c.r, ext[:2]); err != nil {
				return nil, err
			}
			n = uint64(binary.BigEndian.Uint16(ext[:2]))
			if n < 126 {
				return nil, c.fail(1002)
			}
		case 127:
			if _, err := io.ReadFull(c.r, ext[:]); err != nil {
				return nil, err
			}
			n = binary.BigEndian.Uint64(ext[:])
			if n < 65536 || n>>63 != 0 {
				return nil, c.fail(1002)
			}
		}
		control := op >= 8
		if control && (!fin || n > 125) {
			return nil, c.fail(1002)
		}
		switch op {
		case 0:
			if !fragmented {
				return nil, c.fail(1002)
			}
		case 1:
			if fragmented {
				return nil, c.fail(1002)
			}
		case 2:
			return nil, c.fail(1003)
		case 8, 9, 10:
		default:
			return nil, c.fail(1002)
		}
		if !control && n > uint64(c.limit-len(message)) {
			return nil, c.fail(1009)
		}
		var mask [4]byte
		if _, err := io.ReadFull(c.r, mask[:]); err != nil {
			return nil, err
		}
		p := make([]byte, int(n))
		if _, err := io.ReadFull(c.r, p); err != nil {
			return nil, err
		}
		for i := range p {
			p[i] ^= mask[i%4]
		}
		switch op {
		case 8:
			if len(p) == 1 || len(p) >= 2 && !validClose(binary.BigEndian.Uint16(p[:2])) {
				return nil, c.fail(1002)
			}
			if len(p) >= 2 && !utf8.Valid(p[2:]) {
				return nil, c.fail(1007)
			}
			_ = c.frame(8, p)
			return nil, io.EOF
		case 9:
			if err := c.frame(10, p); err != nil {
				return nil, err
			}
			continue
		case 10:
			continue
		}
		message = append(message, p...)
		if fin {
			if !utf8.Valid(message) {
				return nil, c.fail(1007)
			}
			// A frame is one RPC, even when a sender pretty-prints its JSON.
			var compact bytes.Buffer
			if json.Compact(&compact, message) != nil {
				message = []byte("{")
			} else {
				message = compact.Bytes()
			}
			return append(message, '\n'), nil
		}
		fragmented = true
	}
}

func (c *wsConn) Read(p []byte) (int, error) {
	if len(c.pending) == 0 {
		b, err := c.message()
		if err != nil {
			return 0, err
		}
		c.pending = b
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

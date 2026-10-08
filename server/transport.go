package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/server/web"
)

// TokenPath stores the HTTP server's bearer token.
func TokenPath() string { return filepath.Join(config.Dir(), "server-token") }

// NewToken returns a random token of n bytes, hex encoded.
func NewToken(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// LoadOrCreateToken returns the persistent server token.
func LoadOrCreateToken() (string, error) {
	if b, err := os.ReadFile(TokenPath()); err == nil && len(strings.TrimSpace(string(b))) >= 16 {
		return strings.TrimSpace(string(b)), nil
	}
	tok, err := NewToken(24)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(config.Dir(), 0o755); err != nil {
		return "", err
	}
	return tok, os.WriteFile(TokenPath(), []byte(tok+"\n"), 0o600)
}

// webCSP is the web client's Content-Security-Policy: its own script
// and styles, images it attaches (blob: and data: URLs), no frames.
const webCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' blob: data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// HTTPHandler serves the protocol over HTTP:
//
//	POST /rpc      one JSON-RPC request in the body, response in the reply
//	GET  /events   notifications as Server-Sent Events (resumable)
//	GET  /         the web client (server/web), and its assets
//
// Every request except the web client's files needs the token, as
// "Authorization: Bearer" or ?token= (EventSource cannot set headers).
func (s *Server) HTTPHandler(token string) http.Handler {
	b := s.events
	// Legacy HTTP clients share an anonymous transport identity; they need
	// not echo a new field to keep working. JSON-lines connections have
	// independent identities.
	c := &clientConn{id: fmt.Sprintf("h%d", clientSeq.Add(1))}
	clients := func() {
		if s.OnClients != nil {
			s.OnClients(b.clients()) // outside the broker's lock: the hook may wait for a UI
		}
	}
	authed := func(r *http.Request) bool {
		got := r.URL.Query().Get("token")
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			got = strings.TrimPrefix(h, "Bearer ")
		}
		return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
	}
	mux := http.NewServeMux()
	files := http.FileServerFS(web.FS())
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		// Revalidate: the files change with the binary, and the asset URLs
		// carry their hash anyway.
		w.Header().Set("Cache-Control", "no-cache")
		// The client loads nothing from elsewhere, and no page may frame
		// it or learn its URL (the token arrives in its fragment).
		w.Header().Set("Content-Security-Policy", webCSP)
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		files.ServeHTTP(w, r)
	})
	mux.HandleFunc("POST /rpc", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		resp := s.Handle(context.WithValue(r.Context(), clientKey{}, c), body)
		w.Header().Set("Content-Type", "application/json")
		if resp == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		// The query says where a client starts following; the header,
		// which EventSource sends when it reconnects by itself (to the
		// same URL), the last event it got since then, so it wins.
		last, _ := strconv.ParseInt(r.URL.Query().Get("lastEventId"), 10, 64)
		if h := r.Header.Get("Last-Event-ID"); h != "" {
			last, _ = strconv.ParseInt(h, 10, 64)
		}
		backlog, ch, kick, gap := b.subscribe(last)
		s.httpStreams.Add(1)
		defer func() {
			if s.httpStreams.Add(-1) == 0 {
				s.clientGone(c.id)
			}
		}()
		clients()
		defer clients()
		defer b.unsubscribe(ch)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		write := func(ev sseEvent) {
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", ev.id, ev.data)
		}
		if gap {
			// What happened since is not known: the client reads its
			// thread again (events/reset, see protocol.go). No id, so
			// it does not move the client's Last-Event-ID.
			fmt.Fprintf(w, "data: %s\n\n", s.resetNotification())
		}
		for _, ev := range backlog {
			write(ev)
		}
		fl.Flush()
		heartbeat := time.NewTicker(15 * time.Second)
		defer heartbeat.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-kick: // fell behind: reconnect and resume
				return
			case ev := <-ch:
				write(ev)
				fl.Flush()
			case <-heartbeat.C:
				fmt.Fprint(w, ": ping\n\n")
				fl.Flush()
			}
		}
	})
	return mux
}

// IsLoopback reports whether addr only listens on the local machine.
func IsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

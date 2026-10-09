package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"

	"github.com/sebastianrcnt/atto/server/web"
)

// WebHandler exposes public, non-secret static assets and the authenticated
// protocol at /ws. No cookies, HTTP RPC, query tokens or anonymous identity.
func (s *Server) WebHandler(token string, origins []string) http.Handler {
	ws := s.WebSocketHandler(token, origins)
	static := web.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws" {
			// The web endpoint deliberately forbids query credentials. Raw legacy WS
			// listeners retain their header/query token rules for existing clients.
			if r.URL.RawQuery != "" {
				http.Error(w, "query parameters forbidden", 400)
				return
			}
			ws.ServeHTTP(w, r)
			return
		}
		static.ServeHTTP(w, r)
	})
}

// WebListener owns only its transport. Close cancels attached clients, never
// closes the Server or any worker/session. Call Wait to collect serve errors.
type WebListener struct {
	URL    string
	Public bool
	cancel context.CancelFunc
	done   chan struct{}
	err    error
	once   sync.Once
}

func (l *WebListener) Close()      { l.once.Do(l.cancel) }
func (l *WebListener) Wait() error { <-l.done; return l.err }

func (s *Server) ListenWeb(ctx context.Context, address string, origins []string) (*WebListener, error) {
	u, err := url.Parse(address)
	if err != nil {
		return nil, err
	}
	if (u.Scheme != "ws" && u.Scheme != "http") || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("web listen must be ws://HOST:PORT or http://HOST:PORT")
	}
	ln, err := net.Listen("tcp", u.Host)
	if err != nil {
		return nil, err
	}
	public := !IsLoopback(ln.Addr().String())
	token := ""
	if public {
		token, err = LoadOrCreateToken()
		if err != nil {
			ln.Close()
			return nil, err
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	l := &WebListener{URL: "http://" + ln.Addr().String() + "/", Public: public, cancel: cancel, done: make(chan struct{})}
	if token != "" {
		l.URL += "#token=" + url.QueryEscape(token)
	}
	go func() {
		defer close(l.done)
		defer ln.Close()
		l.err = serveHTTP(ctx, ln, s.WebHandler(token, origins))
		cancel()
	}()
	return l, nil
}

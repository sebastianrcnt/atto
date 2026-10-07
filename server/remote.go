package server

import (
	"net"
	"net/http"
	"strconv"
	"time"
)

// A session's own gateway (remote/start): the /remote of a session that
// runs in a daemon worker is served by the worker, scoped to that session,
// so the browser reaches the runtime itself. It stops with remote/stop or
// with the session.

type gateway struct {
	srv   *http.Server
	addr  string
	token string
	links []string
}

// startGateway serves thread t's session on host:port with a new token.
func (s *Server) startGateway(t *thread, host string, port int) (*gateway, error) {
	token, err := NewToken(16)
	if err != nil {
		return nil, err
	}
	if host == "" {
		host = "0.0.0.0"
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	id := t.id
	g := &gateway{token: token, addr: ln.Addr().String()}
	g.srv = &http.Server{Handler: s.ScopedHandler(token, Scope{Thread: func() string { return id }}), ReadHeaderTimeout: 10 * time.Second}
	g.links = WebLinks(g.addr, token)
	if len(g.links) == 0 {
		g.links = []string{"http://" + g.addr + "/#token=" + token}
	}
	go func() { _ = g.srv.Serve(ln) }()
	return g, nil
}

func init() {
	threadMethods["remote/start"] = func(t *thread, client string, p threadParams) (any, error) {
		if t.gateway != nil {
			t.gateway.srv.Close()
		}
		g, err := t.s.startGateway(t, p.Host, p.Port)
		if err != nil {
			return nil, err
		}
		t.gateway = g
		return map[string]any{"addr": g.addr, "token": g.token, "links": g.links}, nil
	}
	threadMethods["remote/stop"] = func(t *thread, client string, p threadParams) (any, error) {
		if t.gateway != nil {
			t.gateway.srv.Close()
			t.gateway = nil
		}
		return nil, nil
	}
}

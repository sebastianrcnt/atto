package ai

import (
	"context"
	"net/http/httptrace"
	"sync"
)

// ConnInfo describes the connection a request went out on. A request log
// keeps it so that a failure can be matched to the network it used: a
// pooled connection opened over Wi-Fi keeps its local address after the
// Mac moves to Ethernet, and dies with the Wi-Fi link.
type ConnInfo struct {
	Local   string `json:"local,omitempty"`
	Remote  string `json:"remote,omitempty"`
	Reused  bool   `json:"reused,omitempty"`
	WasIdle bool   `json:"wasIdle,omitempty"`
	IdleMs  int64  `json:"idleMs,omitempty"`
}

// ConnTrace collects the connection of the requests sent with its context.
type ConnTrace struct {
	mu   sync.Mutex
	last ConnInfo
	ok   bool
}

type connTraceKey struct{}

// WithConnTrace returns a context whose model requests record their
// connection in the returned trace.
func WithConnTrace(ctx context.Context) (context.Context, *ConnTrace) {
	t := &ConnTrace{}
	return context.WithValue(ctx, connTraceKey{}, t), t
}

// Last is the connection of the latest request, if one got that far.
func (t *ConnTrace) Last() (ConnInfo, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.last, t.ok
}

// traceConn makes ctx report the connection to the ConnTrace it carries.
func traceConn(ctx context.Context) context.Context {
	t, _ := ctx.Value(connTraceKey{}).(*ConnTrace)
	if t == nil {
		return ctx
	}
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn: func(c httptrace.GotConnInfo) {
			info := ConnInfo{Reused: c.Reused, WasIdle: c.WasIdle, IdleMs: c.IdleTime.Milliseconds()}
			if c.Conn != nil {
				info.Local, info.Remote = c.Conn.LocalAddr().String(), c.Conn.RemoteAddr().String()
			}
			t.mu.Lock()
			t.last, t.ok = info, true
			t.mu.Unlock()
		},
	})
}

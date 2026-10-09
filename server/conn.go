package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
)

// A connection is one client speaking JSON lines over a byte stream: a
// pipe in the same process, stdio, or a Unix socket. It gets a client ID,
// every notification of the hub in order (each carries its eventId), and
// its requests are handled one after another, in the order sent. When the
// stream ends the client is detached: its gates are released, and nothing
// it started is stopped (transport lifetime is not session lifetime).

// clientConn is a connected client.
type clientConn struct {
	id          string
	name        string
	interactive bool
	protocol    atomic.Int32
}

type clientKey struct{}

// clientOf is the client a request came from ("" for HTTP and tests).
func clientOf(ctx context.Context) string {
	if c, ok := ctx.Value(clientKey{}).(*clientConn); ok {
		return c.id
	}
	return ""
}

func connOf(ctx context.Context) *clientConn {
	c, _ := ctx.Value(clientKey{}).(*clientConn)
	return c
}

var clientSeq atomic.Int64

// ServeConn serves one client on rw until it closes or ctx ends. rw is
// closed when ctx ends, if it can be.
func (s *Server) ServeConn(ctx context.Context, rw io.ReadWriter) error {
	c := &clientConn{id: fmt.Sprintf("c%d", clientSeq.Add(1))}
	ctx, cancel := context.WithCancel(context.WithValue(ctx, clientKey{}, c))
	defer cancel()
	if cl, ok := rw.(io.Closer); ok {
		go func() {
			<-ctx.Done()
			cl.Close()
		}()
	}
	var mu sync.Mutex
	write := func(b []byte) error {
		mu.Lock()
		defer mu.Unlock()
		line := make([]byte, len(b)+1) // b may be the hub's, shared
		copy(line, b)
		line[len(b)] = '\n'
		_, err := rw.Write(line)
		return err
	}
	s.addClient(c)
	defer s.removeClient(c)
	// Subscribe before handling requests: a fast turn must not outrun its client.
	_, ch, kick, _ := s.events.subscribe(s.events.last())
	forwardDone := make(chan struct{})
	go func() {
		defer close(forwardDone)
		s.forward(ctx, func(b []byte) error {
			if sc := scopeOf(ctx); sc != nil && !sc.event(b) {
				return nil
			}
			return write(b)
		}, ch, kick)
	}()
	defer func() { cancel(); <-forwardDone }()

	sc := bufio.NewScanner(rw)
	// Leave room for the JSON-lines delimiter at the message size limit.
	sc.Buffer(make([]byte, 64*1024), (64<<20)+2)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if resp := s.Handle(ctx, []byte(line)); resp != nil {
			mu.Lock()
			err := encodeJSON(rw, resp)
			mu.Unlock()
			if err != nil {
				break
			}
		}
	}
	return sc.Err()
}

// forward writes the hub's events to a connection from now on. Fallen
// behind, it says events/reset and goes on from the newest event.
func (s *Server) forward(ctx context.Context, write func([]byte) error, ch chan sseEvent, kick chan struct{}) {
	for {
		select {
		case <-ctx.Done():
			s.events.unsubscribe(ch)
			return
		case ev := <-ch:
			if write(ev.data) != nil {
				s.events.unsubscribe(ch)
				return
			}
		case <-kick:
			// A kicked stream needs a snapshot, not a partially replayed transcript.
			// Subscribe first, so events racing the reset are still delivered.
			_, ch, kick, _ = s.events.subscribe(s.events.last())
			if write(s.resetNotification()) != nil {
				s.events.unsubscribe(ch)
				return
			}
		}
	}
}

// ServeStdio speaks JSON-RPC as JSON lines on r/w (codex app-server style):
// one request per line in, responses and notifications out.
func (s *Server) ServeStdio(ctx context.Context, r io.Reader, w io.Writer) error {
	return s.ServeConn(ctx, stdioConn{r, w})
}

func (s *Server) addClient(c *clientConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clients == nil {
		s.clients = map[string]*clientConn{}
	}
	s.clients[c.id] = c
}

func (s *Server) removeClient(c *clientConn) {
	s.mu.Lock()
	delete(s.clients, c.id)
	s.mu.Unlock()
	s.clientGone(c.id)
}

// interactiveClients counts the connected clients that answer prompts.
func (s *Server) interactiveClients() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := int(s.httpStreams.Load())
	for _, c := range s.clients {
		if c.interactive {
			n++
		}
	}
	return n
}

// stdioConn lets cancellation unblock a reader (including os.Stdin).
// Plain non-closing readers and writers remain usable in embedded tests.
type stdioConn struct {
	io.Reader
	io.Writer
}

func (c stdioConn) Close() error {
	if r, ok := c.Reader.(io.Closer); ok {
		_ = r.Close()
	}
	if w, ok := c.Writer.(io.Closer); ok {
		return w.Close()
	}
	return nil
}

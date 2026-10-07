package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
)

// Client speaks the protocol to a server over a byte stream: in the same
// process (Connect, over a pipe: the same JSON, dispatcher and event hub
// as any other client), or over a socket. Requests and their responses
// are matched by ID; notifications arrive in order on Events. Its event
// queue is unbounded, so a consumer that is slow to read never holds up
// responses (after codex-rs app-server-client).
type Client struct {
	rw  io.ReadWriteCloser
	wmu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan rpcReply
	queue   []Notification
	wake    chan struct{}
	err     error
	done    chan struct{}

	events chan Notification
}

// Notification is a server notification as a client gets it.
type Notification struct {
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	EventID int64           `json:"eventId,omitempty"`
}

// ThreadID is the notification's threadId ("" for none).
func (n Notification) ThreadID() string {
	var p struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(n.Params, &p)
	return p.ThreadID
}

type rpcReply struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// ErrClosed is returned by calls on a client whose connection ended.
var ErrClosed = errors.New("connection to the atto server closed")

// NewClient starts a client on rw, which it owns.
func NewClient(rw io.ReadWriteCloser) *Client {
	c := &Client{rw: rw, pending: map[int64]chan rpcReply{}, wake: make(chan struct{}, 1),
		done: make(chan struct{}), events: make(chan Notification)}
	go c.read()
	go c.pump()
	return c
}

// Connect is a client of s in the same process. The connection ends when
// ctx does or the client is closed; neither stops anything the client
// started.
func Connect(ctx context.Context, s *Server) *Client {
	a, b := net.Pipe()
	go func() { _ = s.ServeConn(ctx, a) }()
	return NewClient(b)
}

func (c *Client) read() {
	sc := bufio.NewScanner(c.rw)
	sc.Buffer(make([]byte, 64*1024), 64<<20)
	for sc.Scan() {
		var m struct {
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
			EventID int64           `json:"eventId"`
			rpcReply
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if m.Method != "" {
			c.mu.Lock()
			c.queue = append(c.queue, Notification{Method: m.Method, Params: m.Params, EventID: m.EventID})
			c.mu.Unlock()
			select {
			case c.wake <- struct{}{}:
			default:
			}
			continue
		}
		id, err := strconv.ParseInt(string(m.ID), 10, 64)
		if err != nil {
			continue
		}
		c.mu.Lock()
		ch := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if ch != nil {
			ch <- m.rpcReply
		}
	}
	err := sc.Err()
	if err == nil {
		err = ErrClosed
	}
	c.mu.Lock()
	c.err = err
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	close(c.done)
}

// pump hands queued notifications to Events in order.
func (c *Client) pump() {
	defer close(c.events)
	for {
		c.mu.Lock()
		if len(c.queue) == 0 {
			c.mu.Unlock()
			select {
			case <-c.wake:
				continue
			case <-c.done:
				c.mu.Lock()
				rest := c.queue
				c.queue = nil
				c.mu.Unlock()
				for _, n := range rest {
					c.events <- n
				}
				return
			}
		}
		n := c.queue[0]
		c.queue = c.queue[1:]
		c.mu.Unlock()
		c.events <- n
	}
}

// Events are the server's notifications, in order; closed once the
// connection has ended and they were all read.
func (c *Client) Events() <-chan Notification { return c.events }

// Call sends a request and decodes its result into result (when not nil).
// A server error is a *RPCError.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return ErrClosed
	}
	c.nextID++
	id := c.nextID
	ch := make(chan rpcReply, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	if err := c.send(rpcRequestOut{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return err
	}
	select {
	case r, ok := <-ch:
		if !ok {
			return ErrClosed
		}
		if r.Error != nil {
			return r.Error
		}
		if result != nil && len(r.Result) > 0 {
			return json.Unmarshal(r.Result, result)
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	}
}

type rpcRequestOut struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

func (c *Client) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := c.rw.Write(append(b, '\n')); err != nil {
		return ErrClosed
	}
	return nil
}

// Close ends the connection: the client detaches.
func (c *Client) Close() error { return c.rw.Close() }

package mcp

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/fsutil"
)

// A session's Manager answers "atto mcp" run in the agent's shell over a
// Unix domain socket (which Windows 10+ supports too), so the servers
// that live in the session are used and not started per call. The
// endpoint file <atto dir>/mcp/<session id>.json (0600) says where the
// socket is and holds the random token every request must carry; the
// session ID comes from $ATTO_SESSION_ID. The protocol is one JSON request
// and one JSON response per connection.

type endpoint struct {
	Network string `json:"network"`
	Addr    string `json:"addr"`
	Token   string `json:"token"`
	PID     int    `json:"pid"`
}

type request struct {
	Token  string          `json:"token"`
	Op     string          `json:"op"` // servers, tools, all_tools, call
	Server string          `json:"server,omitempty"`
	Tool   string          `json:"tool,omitempty"`
	Args   json.RawMessage `json:"args,omitempty"`
}

type response struct {
	Error    string        `json:"error,omitempty"`
	Servers  []Info        `json:"servers,omitempty"`
	Tools    []ToolInfo    `json:"tools,omitempty"`
	Problems []ServerError `json:"problems,omitempty"`
	Result   *Result       `json:"result,omitempty"`
}

// maxUnixPath is below the shortest sun_path limit (104 on macOS and BSD).
const maxUnixPath = 100

// listener is the socket of a session's Manager.
type listener struct {
	ln    net.Listener
	token string
	addr  string
	done  chan struct{}
}

func endpointPath(session string) (string, error) {
	if session == "" || strings.ContainsAny(session, `/\`) || session == "." || session == ".." {
		return "", fmt.Errorf("bad session id %q", session)
	}
	return filepath.Join(config.MCPRunDir(), session+".json"), nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// SetSession tells the Manager which session it serves: that is the ID the
// agent's shell finds it by. A /clear or /resume in the TUI starts a new
// session, and moves the endpoint with it.
func (m *Manager) SetSession(id string) {
	m.mu.Lock()
	old, ipc := m.id, m.ipc
	m.id = id
	m.mu.Unlock()
	if old != "" && old != id {
		if p, err := endpointPath(old); err == nil {
			_ = os.Remove(p)
		}
	}
	if ipc != nil && old != id {
		ipc.writeEndpoint(id)
	}
	m.publish()
}

// publish starts the socket and writes the endpoint file once a session is
// set and there is a server to reach: sessions without MCP leave nothing
// behind.
func (m *Manager) publish() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.id == "" || len(m.order) == 0 || m.ipc != nil {
		return
	}
	l, err := listen(m)
	if err != nil {
		return // calls from the shell fall back to starting servers for the call
	}
	m.ipc = l
	l.writeEndpoint(m.id)
}

func listen(m *Manager) (*listener, error) {
	dir := config.MCPRunDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	sock := filepath.Join(dir, randHex(4)+".sock")
	if len(sock) > maxUnixPath { // a long ATTO_DIR: the temp dir is short
		sock = filepath.Join(os.TempDir(), "atto-mcp-"+randHex(6)+".sock")
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(sock, 0o600)
	l := &listener{ln: ln, token: randHex(16), addr: sock, done: make(chan struct{})}
	go l.serve(m)
	return l, nil
}

func (l *listener) writeEndpoint(session string) {
	p, err := endpointPath(session)
	if err != nil {
		return
	}
	data, _ := json.Marshal(endpoint{Network: "unix", Addr: l.addr, Token: l.token, PID: os.Getpid()})
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = fsutil.WriteAtomic(p, data, 0o600)
}

func (l *listener) close(session string) {
	close(l.done)
	_ = l.ln.Close()
	if p, err := endpointPath(session); err == nil {
		_ = os.Remove(p)
	}
}

func (l *listener) serve(m *Manager) {
	for {
		c, err := l.ln.Accept()
		if err != nil {
			select {
			case <-l.done:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		go l.handle(m, c)
	}
}

func (l *listener) handle(m *Manager, c net.Conn) {
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	var req request
	if err := json.NewDecoder(c).Decode(&req); err != nil {
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	reply := func(r response) { _ = json.NewEncoder(c).Encode(r) }
	if subtle.ConstantTimeCompare([]byte(req.Token), []byte(l.token)) != 1 {
		reply(response{Error: "bad token"})
		return
	}
	// The caller going away (a shell command that timed out) cancels the call.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = io.Copy(io.Discard, c)
		cancel()
	}()
	var r response
	switch req.Op {
	case "servers":
		r.Servers, _ = m.Servers(ctx)
	case "tools":
		tools, err := m.Tools(ctx, req.Server)
		r.Tools, r.Error = tools, errText(err)
	case "all_tools":
		r.Tools, r.Problems, _ = m.AllTools(ctx)
	case "call":
		res, err := m.Call(ctx, req.Server, req.Tool, req.Args)
		if err == nil {
			r.Result = &res
		}
		r.Error = errText(err)
	default:
		r.Error = fmt.Sprintf("unknown op %q", req.Op)
	}
	reply(r)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// remote is a session's Manager reached over its socket.
type remote struct{ ep endpoint }

// ErrNoEndpoint is Dial's error for a session with no MCP endpoint: it has
// no servers configured, or it has ended.
var ErrNoEndpoint = errors.New("the session has no MCP endpoint")

// Dial connects to the Manager of session, from its endpoint file. It
// fails when there is none or nothing listens (a session that has since
// ended).
func Dial(session string) (Backend, error) {
	p, err := endpointPath(session)
	if err != nil {
		return nil, err
	}
	data, err := fsutil.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoEndpoint
	}
	if err != nil {
		return nil, err
	}
	var ep endpoint
	if err := json.Unmarshal(data, &ep); err != nil || ep.Addr == "" {
		return nil, fmt.Errorf("%s: not an endpoint file", p)
	}
	r := &remote{ep}
	// Probe it, so a stale file is noticed now and not on the first call.
	if _, err := r.do(context.Background(), request{Op: "servers"}); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *remote) do(ctx context.Context, req request) (response, error) {
	d := net.Dialer{Timeout: 3 * time.Second}
	c, err := d.DialContext(ctx, r.ep.Network, r.ep.Addr)
	if err != nil {
		return response{}, err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	req.Token = r.ep.Token
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return response{}, err
	}
	var resp response
	if err := json.NewDecoder(c).Decode(&resp); err != nil {
		if ctx.Err() != nil {
			return response{}, ctx.Err()
		}
		return response{}, fmt.Errorf("the session's MCP endpoint hung up: %w", err)
	}
	if resp.Error != "" {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

func (r *remote) Servers(ctx context.Context) ([]Info, error) {
	resp, err := r.do(ctx, request{Op: "servers"})
	return resp.Servers, err
}

func (r *remote) Tools(ctx context.Context, server string) ([]ToolInfo, error) {
	resp, err := r.do(ctx, request{Op: "tools", Server: server})
	return resp.Tools, err
}

func (r *remote) AllTools(ctx context.Context) ([]ToolInfo, []ServerError, error) {
	resp, err := r.do(ctx, request{Op: "all_tools"})
	return resp.Tools, resp.Problems, err
}

func (r *remote) Call(ctx context.Context, server, tool string, args json.RawMessage) (Result, error) {
	resp, err := r.do(ctx, request{Op: "call", Server: server, Tool: tool, Args: args})
	if err != nil {
		return Result{}, err
	}
	if resp.Result == nil {
		return Result{}, errors.New("empty response")
	}
	return *resp.Result, nil
}

func (r *remote) Close() error { return nil }

// Connect returns the backend for an "atto mcp" command: the running
// session's Manager when session (from $ATTO_SESSION_ID) has one, else a
// Manager started here, whose servers live for the command only (stop
// them with Close). via says which; note says why a session set but not
// reached fell back.
func Connect(session string, o Options) (b Backend, inSession bool, note string) {
	if session != "" {
		r, err := Dial(session)
		if err == nil {
			return r, true, ""
		}
		if !errors.Is(err, ErrNoEndpoint) {
			note = "could not reach the session's MCP endpoint (" + err.Error() + "); started the server for this call only"
		}
	}
	return New(o), false, note
}

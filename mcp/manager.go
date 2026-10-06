package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Version is what atto tells servers its version is; main sets it.
var Version = "dev"

// StartTimeout bounds starting a server and its initialize handshake.
const StartTimeout = 60 * time.Second

// Options configures a Manager.
type Options struct {
	// Cwd is the session's working directory: stdio servers run there.
	Cwd string
	// Root is the project root, where .mcp.json is.
	Root string
	// Lookup resolves ${VAR}; nil is os.LookupEnv.
	Lookup func(string) (string, bool)
}

// ServerError is a server that could not be used, for calls that cover
// all servers.
type ServerError struct {
	Server string `json:"server"`
	Error  string `json:"error"`
}

// Backend is what runs MCP calls: a Manager in this process, or the
// session's Manager reached over its socket (see Connect).
type Backend interface {
	// Servers lists the configured servers with their status.
	Servers(ctx context.Context) ([]Info, error)
	// Tools lists the tools of one server, starting it if needed.
	Tools(ctx context.Context, server string) ([]ToolInfo, error)
	// AllTools lists the tools of every server that can start, and says
	// why the others cannot.
	AllTools(ctx context.Context) ([]ToolInfo, []ServerError, error)
	// Call runs a tool. args is a JSON object (nil is {}).
	Call(ctx context.Context, server, tool string, args json.RawMessage) (Result, error)
	// Close releases the backend: a Manager stops its servers.
	Close() error
}

// Manager runs the MCP servers of one session. Servers start on first use
// and stay until Close. Its methods are safe from any goroutine.
type Manager struct {
	opts Options

	mu      sync.Mutex
	entries map[string]*entry
	order   []string
	issues  []string
	id      string
	ipc     *listener
	closed  bool
}

// entry is a configured server and, once started, its connection.
type entry struct {
	srv     Server
	exp     ServerConfig // with ${VAR} expanded
	missing []string     // variables that were unset without a default
	stderr  tail

	start sync.Mutex // serializes starting and stopping

	mu    sync.Mutex // guards the rest; never held while starting
	sess  *sdk.ClientSession
	tools int
	err   string // why the last start failed, or the process died
}

// New reads the configuration for o and returns a Manager with every
// server not started.
func New(o Options) *Manager {
	if o.Lookup == nil {
		o.Lookup = os.LookupEnv
	}
	m := &Manager{opts: o, entries: map[string]*entry{}}
	m.Reload()
	return m
}

// Reload reads the configuration again. A server whose entry is the same
// keeps running; one that changed or went away is stopped (a changed one
// starts again on next use).
func (m *Manager) Reload() {
	servers, issues := Load(m.opts.Root)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	old := m.entries
	next := map[string]*entry{}
	var order []string
	for _, s := range servers {
		e := &entry{srv: s, tools: -1}
		e.exp, e.missing = s.Config.Expanded(m.opts.Lookup)
		if prev := old[s.Name]; prev != nil && prev.same(e) {
			e = prev
			delete(old, s.Name)
		}
		next[s.Name] = e
		order = append(order, s.Name)
	}
	m.entries, m.order, m.issues = next, order, issues
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, e := range old {
		wg.Go(func() {
			e.stop()
		})
	}
	wg.Wait()
	m.publish()
}

// same reports whether e is the entry o replaces unchanged.
func (e *entry) same(o *entry) bool {
	return e.srv.Scope == o.srv.Scope && e.srv.Path == o.srv.Path &&
		e.srv.Config.Hash() == o.srv.Config.Hash() && reflect.DeepEqual(e.exp, o.exp)
}

// Issues are the problems found reading the configuration files.
func (m *Manager) Issues() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.issues...)
}

// Ignored is a repository file of local servers that is not read (see
// Ignored), or "".
func (m *Manager) Ignored() string { return Ignored(m.opts.Root) }

// Names are the configured servers, sorted. Whether a server is approved
// or running does not matter: the list changes only when the
// configuration does.
func (m *Manager) Names() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.order...)
}

// PromptServers is Names, for the system prompt.
func (m *Manager) PromptServers() []string { return m.Names() }

func (m *Manager) get(name string) (*entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("MCP is shut down")
	}
	if e := m.entries[name]; e != nil {
		return e, nil
	}
	if len(m.order) == 0 {
		return nil, fmt.Errorf("no MCP server %q: none is configured (atto mcp add)", name)
	}
	return nil, fmt.Errorf("no MCP server %q (configured: %s)", name, strings.Join(m.order, ", "))
}

// Servers lists the configured servers.
func (m *Manager) Servers(context.Context) ([]Info, error) {
	m.mu.Lock()
	var es []*entry
	for _, n := range m.order {
		es = append(es, m.entries[n])
	}
	m.mu.Unlock()
	out := make([]Info, 0, len(es))
	for _, e := range es {
		out = append(out, e.info())
	}
	return out, nil
}

// Info describes the server now.
func (e *entry) info() Info {
	c := e.srv.Config
	in := Info{Name: e.srv.Name, Scope: e.srv.Scope, Transport: c.Transport(), Target: c.Target(),
		Path: e.srv.Path, Hash: c.Hash(), Tools: -1}
	e.mu.Lock()
	running, tools, errText := e.sess != nil, e.tools, e.err
	e.mu.Unlock()
	switch {
	case running:
		in.Status, in.Tools = Running, tools
	case c.Validate() != nil:
		in.Status, in.Error, in.Invalid = Failed, c.Validate().Error(), true
	case ApprovalOf(e.srv) == Pending:
		in.Status = NeedsApproval
	case ApprovalOf(e.srv) == Denied:
		in.Status = DeniedStatus
	case errText != "":
		in.Status, in.Error = Failed, errText
	default:
		in.Status = NotStarted
	}
	return in
}

// Server returns the configured server called name.
func (m *Manager) Server(name string) (Server, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.entries[name]; e != nil {
		return e.srv, true
	}
	return Server{}, false
}

// ErrNotProject is returned by Approve for a server that needs none.
var ErrNotProject = errors.New("only servers from a project's .mcp.json need approval")

func (m *Manager) project(name string) (Server, error) {
	s, ok := m.Server(name)
	switch {
	case !ok:
		_, err := m.get(name)
		return s, err
	case s.Scope != ScopeProject:
		return s, fmt.Errorf("%s is a %s server (%s): %w", name, s.Scope, s.Path, ErrNotProject)
	}
	return s, nil
}

// Approve records that the user lets project server name run as its entry
// is now; a change to the entry needs approval again.
func (m *Manager) Approve(name string) error {
	s, err := m.project(name)
	if err != nil {
		return err
	}
	return Approve(s)
}

// ApproveAll approves every server of the file name comes from, now and
// later.
func (m *Manager) ApproveAll(name string) error {
	s, err := m.project(name)
	if err != nil {
		return err
	}
	return ApproveAll(s)
}

// Deny records that the user does not want project server name to run as
// its entry is now.
func (m *Manager) Deny(name string) error {
	s, err := m.project(name)
	if err != nil {
		return err
	}
	return Deny(s)
}

// ApprovalError says how to approve a project server.
type ApprovalError struct{ Server, State string }

func (e *ApprovalError) Error() string {
	if e.State == DeniedStatus {
		return fmt.Sprintf("MCP server %s was denied by the user. Ask the user to run: atto mcp approve %s", e.Server, e.Server)
	}
	return fmt.Sprintf("MCP server %s comes from the project's .mcp.json and needs the user's approval. Ask the user to run: atto mcp approve %s", e.Server, e.Server)
}

// session returns the connection to e, starting the server if needed.
func (m *Manager) session(ctx context.Context, e *entry) (*sdk.ClientSession, error) {
	e.start.Lock()
	defer e.start.Unlock()
	e.mu.Lock()
	if e.sess != nil {
		s := e.sess
		e.mu.Unlock()
		return s, nil
	}
	e.mu.Unlock()

	name := e.srv.Name
	if err := e.srv.Config.Validate(); err != nil {
		return nil, fmt.Errorf("MCP server %s: %w", name, err)
	}
	switch ApprovalOf(e.srv) {
	case Pending:
		return nil, &ApprovalError{name, NeedsApproval}
	case Denied:
		return nil, &ApprovalError{name, DeniedStatus}
	}
	fail := func(err error) (*sdk.ClientSession, error) {
		msg := err.Error()
		if t := e.stderr.last(); t != "" {
			msg += " (server said: " + t + ")"
		}
		e.mu.Lock()
		e.err = msg
		e.mu.Unlock()
		return nil, fmt.Errorf("MCP server %s failed to start: %s", name, msg)
	}
	if len(e.missing) > 0 {
		return fail(fmt.Errorf("environment variable %s is not set (use ${VAR:-default} to give it a default)", strings.Join(e.missing, ", ")))
	}
	tr, err := m.transport(e)
	if err != nil {
		return fail(err)
	}
	cctx, cancel := context.WithTimeout(ctx, StartTimeout)
	defer cancel()
	client := sdk.NewClient(&sdk.Implementation{Name: "atto", Version: Version}, nil)
	sess, err := client.Connect(cctx, tr, nil)
	if err != nil {
		return fail(err)
	}
	count := -1
	if res, err := sess.ListTools(cctx, nil); err == nil {
		count = len(res.Tools)
	}
	e.mu.Lock()
	e.sess, e.tools, e.err = sess, count, ""
	e.mu.Unlock()
	go func() { // the server going away is noticed, so the next use restarts it
		_ = sess.Wait()
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.sess == sess {
			e.sess, e.tools = nil, -1
			e.err = "the server exited"
			if t := e.stderr.last(); t != "" {
				e.err += " (last output: " + t + ")"
			}
		}
	}()
	return sess, nil
}

func (m *Manager) transport(e *entry) (sdk.Transport, error) {
	c := e.exp
	switch c.Transport() {
	case TransportStdio:
		cmd := exec.Command(c.Command, c.Args...)
		cmd.Dir = m.opts.Cwd
		cmd.Env = os.Environ()
		for k, v := range c.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		cmd.Stderr = &e.stderr
		return &sdk.CommandTransport{Command: cmd, TerminateDuration: 2 * time.Second}, nil
	case TransportHTTP:
		return &sdk.StreamableClientTransport{Endpoint: c.URL, HTTPClient: httpClient(c.Headers)}, nil
	case TransportSSE:
		return &sdk.SSEClientTransport{Endpoint: c.URL, HTTPClient: httpClient(c.Headers)}, nil
	}
	return nil, fmt.Errorf("unknown transport %q", c.Transport())
}

// httpClient adds the configured headers to every request.
func httpClient(h map[string]string) *http.Client {
	return &http.Client{Transport: headerTransport{h}}
}

type headerTransport struct{ h map[string]string }

func (t headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range t.h {
		r.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(r)
}

// stop closes the server's connection (a stdio server's process ends).
func (e *entry) stop() {
	e.start.Lock()
	defer e.start.Unlock()
	e.mu.Lock()
	s := e.sess
	e.sess, e.tools = nil, -1
	e.mu.Unlock()
	if s != nil {
		_ = s.Close()
	}
}

// Tools lists the tools of server, starting it if needed.
func (m *Manager) Tools(ctx context.Context, server string) ([]ToolInfo, error) {
	e, err := m.get(server)
	if err != nil {
		return nil, err
	}
	sess, err := m.session(ctx, e)
	if err != nil {
		return nil, err
	}
	var out []ToolInfo
	for t, err := range sess.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("MCP server %s: listing tools: %w", server, err)
		}
		schema, _ := json.Marshal(t.InputSchema)
		out = append(out, ToolInfo{Server: server, Name: t.Name, Description: t.Description, InputSchema: schema})
	}
	e.mu.Lock()
	if e.sess == sess {
		e.tools = len(out)
	}
	e.mu.Unlock()
	return out, nil
}

// AllTools lists the tools of every configured server, starting those that
// are not running (in parallel). Servers that cannot start are in the
// errors, in name order.
func (m *Manager) AllTools(ctx context.Context) ([]ToolInfo, []ServerError, error) {
	names := m.Names()
	type res struct {
		tools []ToolInfo
		err   error
	}
	results := make([]res, len(names))
	var wg sync.WaitGroup
	for i, n := range names {
		wg.Go(func() {
			results[i].tools, results[i].err = m.Tools(ctx, n)
		})
	}
	wg.Wait()
	var tools []ToolInfo
	var errs []ServerError
	for i, r := range results {
		if r.err != nil {
			errs = append(errs, ServerError{Server: names[i], Error: r.err.Error()})
			continue
		}
		tools = append(tools, r.tools...)
	}
	return tools, errs, nil
}

// Call runs tool of server with args (a JSON object; nil is {}).
func (m *Manager) Call(ctx context.Context, server, tool string, args json.RawMessage) (Result, error) {
	e, err := m.get(server)
	if err != nil {
		return Result{}, err
	}
	var arguments any = map[string]any{}
	if len(args) > 0 {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(args, &obj); err != nil || obj == nil {
			return Result{}, errors.New("the arguments must be a JSON object")
		}
		arguments = json.RawMessage(args)
	}
	sess, err := m.session(ctx, e)
	if err != nil {
		return Result{}, err
	}
	res, err := sess.CallTool(ctx, &sdk.CallToolParams{Name: tool, Arguments: arguments})
	if err != nil {
		return Result{}, fmt.Errorf("MCP server %s: %s: %w", server, tool, err)
	}
	return summarize(res), nil
}

// Close stops every server and the session socket. The Manager is
// unusable afterwards.
func (m *Manager) Close() error {
	m.mu.Lock()
	m.closed = true
	es := m.entries
	m.entries, m.order = map[string]*entry{}, nil
	ipc, id := m.ipc, m.id
	m.ipc = nil
	m.mu.Unlock()
	if ipc != nil {
		ipc.close(id)
	}
	var wg sync.WaitGroup
	for _, e := range es {
		wg.Go(func() {
			e.stop()
		})
	}
	wg.Wait()
	return nil
}

// tail keeps the end of a server's standard error, which atto shows when
// the server fails to start instead of letting it scribble on the screen.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

const tailSize = 2048

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > tailSize {
		t.buf = t.buf[len(t.buf)-tailSize:]
	}
	return len(p), nil
}

// last is the last non-empty line, clipped.
func (t *tail) last() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(string(t.buf)), "\n")
	s := strings.TrimSpace(lines[len(lines)-1])
	if r := []rune(s); len(r) > 200 {
		s = string(r[:199]) + "…"
	}
	return s
}

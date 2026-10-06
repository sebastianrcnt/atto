// Package extensions runs JavaScript and TypeScript extensions, in the
// spirit of pi's: a file exporting a default function that receives the
// atto API and registers event handlers and slash commands.
//
//	export default function (atto: Atto) {
//	  atto.on("tool_call", (e) => e.command.includes("rm -rf /") ? { block: true, reason: "no" } : undefined);
//	  atto.registerCommand("hello", { description: "Say hi", handler: (args, ctx) => ctx.ui.notify("hi " + args) });
//	}
//
// esbuild compiles each extension and the relative files it imports into
// one script (see Bundle); goja runs it, one runtime and one goroutine per
// extension (see ext). They are found in ~/.atto/extensions and in the
// project's .atto/extensions (see Discover); project extensions run only
// once approved (see Approve). Some ship inside atto (see builtin.go).
// atto.d.ts declares the API (Types).
//
// Extensions run inside the hooks: PreToolUse hooks, then tool_call
// handlers, the command, tool_result handlers, then PostToolUse hooks (see
// agent.Extensions). A handler that throws is reported and skipped; an
// extension whose script runs too long without yielding, or that panics
// atto's side of the API, is disabled. Neither stops atto.
package extensions

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/mcp"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

// Statuses of an extension.
const (
	Loaded        = "loaded"
	Failed        = "failed"
	NeedsApproval = "needs approval"
	Disabled      = "disabled" // by settings.json
)

// DefaultTimeout bounds what atto waits for (see config.ExtensionSettings).
const DefaultTimeout = 5 * time.Second

// Types is atto.d.ts, the API's TypeScript declarations.
//
//go:embed atto.d.ts
var Types string

// TypesFile is the name Types is written under next to extensions.
const TypesFile = "atto.d.ts"

// Info describes an extension, for the Loaded block.
type Info struct {
	Name     string   `json:"name"`
	Path     string   `json:"path"`
	Source   string   `json:"source"` // User, Project or Builtin
	Status   string   `json:"status"`
	Error    string   `json:"error,omitempty"`
	Commands []string `json:"commands,omitempty"`
	Events   []string `json:"events,omitempty"`
	Hash     string   `json:"hash,omitempty"` // of the bundled code
	// Completes counts the atto.complete requests the extension made, per
	// model, since it loaded.
	Completes []CompleteStat `json:"completes,omitempty"`
}

// Command is a slash command an extension registered.
type Command struct {
	Name        string
	Description string
	Ext         string
}

// Options configures a Manager.
type Options struct {
	Cwd string
	// Agent, if set, gives atto.session its model.
	Agent *agent.Agent
	// Host is the front end; nil is a Headless host that drops everything.
	Host Host
}

// Manager runs the extensions of one session. Its methods are safe from
// any goroutine.
type Manager struct {
	cwd string
	ag  *agent.Agent

	mu   sync.Mutex
	h    Host
	id   string
	exts []*ext
	to   time.Duration
	mcp  mcp.Backend

	logMu sync.Mutex

	cmdVer atomic.Uint64 // see CommandsVersion
}

// Load discovers and starts the extensions for a session in o.Cwd. It
// never fails: problems are in each extension's Info.
func Load(o Options) *Manager {
	m := &Manager{cwd: o.Cwd, ag: o.Agent, h: o.Host}
	if m.h == nil {
		m.h = &Headless{}
	}
	m.load()
	return m
}

// SetHost changes the front end extensions talk to.
func (m *Manager) SetHost(h Host) {
	m.mu.Lock()
	m.h = h
	m.mu.Unlock()
}

func (m *Manager) host() Host {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.h
}

// SetSession sets the session ID extensions see.
func (m *Manager) SetSession(id string) {
	m.mu.Lock()
	m.id = id
	m.mu.Unlock()
}

func (m *Manager) session() (id, model string) {
	m.mu.Lock()
	id = m.id
	m.mu.Unlock()
	if m.ag != nil {
		if ref, _ := m.ag.Current(); ref.Model.ID != "" {
			model = ref.String()
		}
	}
	return id, model
}

func (m *Manager) timeout() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.to
}

// Ready is the status Inspect gives an extension that would load.
const Ready = "ready"

// settings reads the timeout and the disabled names from settings.json.
func settings() (time.Duration, []string) {
	s, _ := config.LoadSettings() // a broken file was reported by whoever loaded it first
	if s.Extensions == nil {
		return DefaultTimeout, nil
	}
	to := DefaultTimeout
	if s.Extensions.Timeout > 0 {
		to = time.Duration(s.Extensions.Timeout) * time.Second
	}
	return to, s.Extensions.Disabled
}

// candidate is an extension found, checked without running it.
type candidate struct {
	Spec
	code, status, err string
}

// check bundles every extension found for cwd and says which would run
// (Ready) and why the others would not.
func check(cwd string, disabled []string) []candidate {
	var out []candidate
	seen := map[string]string{}
	for _, s := range Discover(cwd) {
		c := candidate{Spec: s, status: Ready}
		if prev, dup := seen[s.Name]; dup {
			c.status, c.err = Failed, "another extension has this name: "+prev
			out = append(out, c)
			continue
		}
		seen[s.Name] = s.Path
		if slices.Contains(disabled, s.Name) {
			c.status = Disabled
			out = append(out, c)
			continue
		}
		code, err := bundleSpec(s)
		switch {
		case err != nil:
			c.status, c.err = Failed, err.Error()
		case s.Source == Project && !approved(s.Path, code):
			c.status = NeedsApproval
		}
		c.code = code
		out = append(out, c)
	}
	return out
}

// Inspect reports the extensions for a session in cwd without running
// any: those that would run have the status Ready.
func Inspect(cwd string) []Info {
	_, disabled := settings()
	out := []Info{}
	for _, c := range check(cwd, disabled) {
		in := Info{Name: c.Name, Path: c.Path, Source: c.Source, Status: c.status, Error: c.err}
		if c.code != "" {
			in.Hash = hash(c.code)
		}
		out = append(out, in)
	}
	return out
}

func (m *Manager) load() {
	to, disabled := settings()
	m.mu.Lock()
	m.to = to
	m.mu.Unlock()

	var exts []*ext
	typesFor := map[string]bool{}
	for _, c := range check(m.cwd, disabled) {
		if c.Source != Builtin {
			typesFor[filepath.Dir(entryDir(c.Spec))] = true
		}
		if c.status != Ready {
			exts = append(exts, stub(m, c.Spec, c.code, c.status, c.err))
			continue
		}
		e := newExt(m, c.Spec, c.code)
		go e.loop()
		e.start(c.code)
		exts = append(exts, e)
	}
	for dir := range typesFor {
		writeTypes(dir)
	}
	m.mu.Lock()
	m.exts = exts
	m.mu.Unlock()
	m.cmdVer.Add(1)
}

// entryDir is the extensions directory s was found in.
func entryDir(s Spec) string {
	if base := filepath.Base(s.Path); base == "index.ts" || base == "index.js" {
		return filepath.Dir(s.Path)
	}
	return s.Path
}

// stub is an extension that does not run.
func stub(m *Manager, s Spec, code, status, err string) *ext {
	e := &ext{m: m, spec: s, status: status, err: err, stop: make(chan struct{}), stopped: make(chan struct{})}
	if code != "" {
		e.hash = hash(code)
	}
	e.stopOnce.Do(func() { close(e.stop) }) // so halt does nothing
	close(e.stopped)
	return e
}

// writeTypes puts atto.d.ts in dir unless it is already there as is, so
// editors and the agent see the API's types.
func writeTypes(dir string) {
	path := filepath.Join(dir, TypesFile)
	if old, err := os.ReadFile(path); err == nil && string(old) == Types {
		return
	}
	_ = os.WriteFile(path, []byte(Types), 0o644)
}

// Reload disposes of every extension (onDispose callbacks run, timers
// stop, status items and widgets go) and loads them again from disk.
func (m *Manager) Reload() {
	m.Close()
	m.load()
}

// Close disposes of every extension.
func (m *Manager) Close() {
	m.mu.Lock()
	exts := m.exts
	m.exts = nil
	m.mu.Unlock()
	m.cmdVer.Add(1)
	var wg sync.WaitGroup
	for _, e := range exts {
		wg.Go(func() {
			e.dispose()
		})
	}
	wg.Wait()
}

// Report describes every extension found, in load order.
func (m *Manager) Report() []Info {
	m.mu.Lock()
	exts := m.exts
	m.mu.Unlock()
	out := []Info{}
	for _, e := range exts {
		out = append(out, e.info())
	}
	return out
}

func (e *ext) info() Info {
	e.mu.Lock()
	defer e.mu.Unlock()
	in := Info{Name: e.spec.Name, Path: e.spec.Path, Source: e.spec.Source, Status: e.status, Error: e.err, Hash: e.hash}
	for _, c := range e.commands {
		in.Commands = append(in.Commands, c.Name)
	}
	in.Events = slices.Clone(e.events)
	in.Completes = slices.Clone(e.completes)
	return in
}

// running are the extensions that run.
func (m *Manager) running() []*ext {
	m.mu.Lock()
	exts := m.exts
	m.mu.Unlock()
	var out []*ext
	for _, e := range exts {
		if e.live() {
			out = append(out, e)
		}
	}
	return out
}

// CommandsVersion changes whenever the commands Commands returns may have:
// when extensions load or are disposed of, or one registers a command. It
// lets callers cache Commands.
func (m *Manager) CommandsVersion() uint64 { return m.cmdVer.Load() }

// Commands are the slash commands extensions registered, in load order.
// A name registered twice keeps its first.
func (m *Manager) Commands() []Command {
	var out []Command
	seen := map[string]bool{}
	for _, e := range m.running() {
		e.mu.Lock()
		for _, c := range e.commands {
			if !seen[c.Name] {
				seen[c.Name] = true
				out = append(out, c)
			}
		}
		e.mu.Unlock()
	}
	return out
}

// RunCommand runs the extension command name with args in the background;
// what it does shows through the host. It reports whether there is such
// a command.
func (m *Manager) RunCommand(name, args string) bool {
	for _, e := range m.running() {
		e.mu.Lock()
		has := slices.ContainsFunc(e.commands, func(c Command) bool { return c.Name == name })
		e.mu.Unlock()
		if has {
			e.post(func() {
				if fn := e.cmdFns[name]; fn != nil {
					e.call("/"+name, fn, e.vm.ToValue(args), e.ctxObj)
				}
			})
			return true
		}
	}
	return false
}

// log appends to the extensions log, which atto.log writes to too.
func (m *Manager) log(name, msg string) {
	m.logMu.Lock()
	defer m.logMu.Unlock()
	path := config.ExtensionLogPath()
	if st, err := os.Stat(path); err == nil && st.Size() > 1<<20 {
		_ = os.Rename(path, path+".old")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	for line := range strings.SplitSeq(strings.TrimRight(msg, "\n"), "\n") {
		fmt.Fprintf(f, "%s [%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), name, line)
	}
}

// start runs the extension's script and its default export. A throw,
// a missing export or a timeout fails it.
func (e *ext) start(code string) {
	_, err := e.await(context.Background(), e.m.timeout(), func() (goja.Value, error) { return e.boot(code) })
	if err == nil {
		return
	}
	msg := err.Error()
	if _, ok := err.(errTimeout); ok {
		msg = "the default export did not finish within " + e.m.timeout().String()
	} else if err != errStopped {
		msg = jsError(err)
	}
	e.mu.Lock()
	if e.status == Loaded {
		e.status, e.err = Failed, msg
	}
	e.mu.Unlock()
	e.halt()
	e.m.log(e.spec.Name, "failed to load: "+msg)
}

// sessionText reads the session's file: its latest name, and the last
// limit user and assistant messages of the active branch that carry text.
func (m *Manager) sessionText(limit int) (name string, msgs []provider.Message) {
	id, _ := m.session()
	path, err := session.Find(id)
	if err != nil {
		return "", nil // not written yet
	}
	_, entries, err := session.Load(path)
	if err != nil {
		return "", nil
	}
	for _, e := range session.Active(entries) {
		switch {
		case e.Type == session.TypeName:
			name = e.Name
		case e.Type == session.TypeMessage && e.Message != nil && (e.Message.Role == "user" || e.Message.Role == "assistant") && strings.TrimSpace(e.Message.Content) != "":
			msgs = append(msgs, provider.Message{Role: e.Message.Role, Content: e.Message.Content})
		}
	}
	if len(msgs) > limit {
		msgs = msgs[len(msgs)-limit:]
	}
	return name, msgs
}

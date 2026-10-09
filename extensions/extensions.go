//go:build !noext

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
// once approved (see Approve). Native Go commands ship inside atto (see builtin.go).
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
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/mcp"
)

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

	native *nativeState

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
		if s.Source == Builtin {
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
		if c.Source == Builtin {
			src, _ := BuiltinSource(c.Name)
			in.Hash = hash(src)
		}
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
	var natives []Spec
	typesFor := map[string]bool{}
	for _, c := range check(m.cwd, disabled) {
		if c.Source == Builtin {
			natives = append(natives, c.Spec)
			continue
		}
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
	m.loadNative(natives, disabled)
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
	m.SessionStart("reload")
}

// Close disposes of every extension.
func (m *Manager) Close() {
	m.closeNative()
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
	return append(out, m.nativeReport()...)
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
	for _, c := range m.nativeCommands() {
		if !seen[c.Name] {
			seen[c.Name] = true
			out = append(out, c)
		}
	}
	return out
}

// RunCommand runs the extension command name with args in the background;
// what it does shows through the host. It reports whether there is such
// a command.
func (m *Manager) RunCommand(name, args string) bool { return m.RunCommandFrom(name, args, "") }
func (m *Manager) RunCommandFrom(name, args, client string) bool {
	for _, e := range m.running() {
		e.mu.Lock()
		has := slices.ContainsFunc(e.commands, func(c Command) bool { return c.Name == name })
		e.mu.Unlock()
		if has {
			e.post(func() {
				if fn := e.cmdFns[name]; fn != nil {
					e.actionClient = client
					e.call("/"+name, fn, e.vm.ToValue(args), e.ctxObj)
					e.actionClient = ""
				}
			})
			return true
		}
	}
	return m.runNative(name, args)
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

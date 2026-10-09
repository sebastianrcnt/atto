package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/provider/providertest"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

// The terminal is a client of the session runtime, so most tests run the
// two together, in this process, as atto without the daemon does: the
// scripted model (provider/providertest) answers, keys and typed lines go
// in, and the blocks and the footer are what comes out.

type nullTerm struct{}

func (nullTerm) Start(func(string), func()) error { return nil }
func (nullTerm) Stop()                            {}
func (nullTerm) Write(string)                     {}
func (nullTerm) Size() (int, int)                 { return 80, 24 }

func writeTestFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func plainLines(lines []string) string {
	var out []string
	for _, l := range lines {
		out = append(out, strings.TrimRight(tui.StripEscapes(l), " "))
	}
	return strings.Join(out, "\n")
}

// testApp is a terminal with no runtime, for what it draws by itself;
// its model is t/m taking input (text, image...).
func testApp(t *testing.T, input ...string) *App {
	t.Setenv("ATTO_DIR", t.TempDir())
	models := config.ModelsFile{Providers: map[string]config.Provider{"t": {Models: []config.Model{{ID: "m", Input: input}}}}}
	a := newApp(nullTerm{}, models, t.TempDir())
	a.info.Model = "t/m"
	return a
}

// testEnv is a home, an ATTO_DIR and a project directory of the test's own,
// with the scripted model as fake/m.
func testEnv(t *testing.T, script ...providertest.Reply) (cwd string, m *providertest.Model) {
	t.Helper()
	home, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, ".atto")
	t.Setenv("ATTO_DIR", dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	m = providertest.New(t, script...)
	m.Install(t, dir)
	cwd = filepath.Join(home, "proj")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	scriptedProviders.Store(cwd, m)
	return cwd, m
}

// startApp is a terminal on a new session of a runtime in this process,
// in cwd, with the configuration the environment has.
func startApp(t *testing.T, cwd string, opts ...Options) *App {
	t.Helper()
	return startAppTerm(t, cwd, nullTerm{}, opts...)
}

// startAppTerm is startApp drawing to term.
func startAppTerm(t *testing.T, cwd string, term tui.Terminal, opts ...Options) *App {
	return startAppStartup(t, cwd, term, true, opts...)
}

func startAppStartup(t *testing.T, cwd string, term tui.Terminal, release bool, opts ...Options) *App {
	t.Helper()
	a := startAppRuntime(t, cwd, term, release, opts...)
	settle(a)
	return a
}

// startAppRuntime attaches exactly as production does. Most feature tests then
// preload the tree in settle; attach-memory tests deliberately do not.
func startAppRuntime(t *testing.T, cwd string, term tui.Terminal, release bool, opts ...Options) *App {
	t.Helper()
	models, err := config.LoadModels()
	if err != nil {
		t.Fatal(err)
	}
	a := newApp(term, models, cwd)
	if s, err := config.LoadSettings(); err == nil {
		a.applySettings(s)
	}
	srv := server.New("test", cwd)
	srv.LockKind = session.KindTUI
	srv.Retire = true
	if err := a.connect(server.Connect(context.Background(), srv), srv); err != nil {
		t.Fatal(err)
	}
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	if err := a.open(o); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		a.ui.Do(func() { a.quitting = true })
		a.shutdown()
	})
	if release {
		a.ui.Do(func() { a.rpcErr("thread/sessionStart", nil) })
	}
	a.syncRPC(10 * time.Second)
	return a
}

// liveApp is startApp in a fresh environment with the scripted model.
func liveApp(t *testing.T, script ...providertest.Reply) (*App, *providertest.Model) {
	t.Helper()
	cwd, m := testEnv(t, script...)
	return startApp(t, cwd), m
}

// settle waits for the requests sent so far to be answered and the
// notifications before their answers to arrive.
func settle(a *App) {
	a.syncRPC(10 * time.Second)
	a.ui.Do(func() { a.readTree(nil) })
	a.syncRPC(10 * time.Second)
	time.Sleep(20 * time.Millisecond)
}

// typeLine submits text as typed with Enter.
func typeLine(a *App, text string) { a.ui.Do(func() { a.submit(text, nil) }) }

// key sends a key as typed.
func key(a *App, data string) {
	a.ui.Do(func() {
		if !a.onInput(data) {
			if a.modal != nil {
				a.modal.HandleInput(data)
			} else {
				a.editor.HandleInput(data)
			}
		}
	})
}

// waitIdle waits until no run goes on and nothing is pending.
func waitIdle(t *testing.T, a *App) {
	t.Helper()
	settle(a)
	within(t, a, "the session to be idle", func() bool {
		return !a.busy && len(a.pending.Steers) == 0 && len(a.pending.Queued) == 0
	})
	settle(a)
}

// send types text and waits for what it starts to finish.
func send(t *testing.T, a *App, text string) {
	t.Helper()
	typeLine(a, text)
	waitIdle(t, a)
}

// within polls cond under the UI lock.
func within(t *testing.T, a *App, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		ok := false
		a.ui.Do(func() { ok = cond() })
		if ok {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func bodyText(a *App) string {
	var out []string
	for _, c := range a.ui.Body.Children {
		out = append(out, c.Render(100)...)
	}
	return plainLines(out)
}

// shown is the transcript as drawn, under the UI lock.
func shown(a *App) string {
	var s string
	a.ui.Do(func() { s = bodyText(a) })
	return s
}

// testBuilders are the builders of tr, by App.
var testBuilders sync.Map

// tr is a transcript builder that feeds a's blocks the way the runtime's
// notifications do, through the items' protocol form: tests drive the
// rendering with agent events.
func (a *App) tr() *transcript.Builder {
	if b, ok := testBuilders.Load(a); ok {
		return b.(*transcript.Builder)
	}
	b := &transcript.Builder{IDPrefix: "s-i"}
	b.Handler = transcript.Handler{
		Started:   func(it *transcript.Item) { a.wireStarted(server.WireItem("s", it)) },
		Delta:     func(it *transcript.Item, d string) { a.wireDelta(it.ID, d) },
		Updated:   func(it *transcript.Item) { a.wireUpdated(server.WireItem("s", it)) },
		Completed: func(it *transcript.Item) { a.wireCompleted(server.WireItem("s", it)) },
	}
	testBuilders.Store(a, b)
	return b
}

// onEvent is an agent event as the terminal sees it: its items, and the
// activity the runtime would report.
func (a *App) onEvent(ev any) {
	if a.busy {
		a.lastEvent = a.clock()
	}
	a.tr().Event(ev)
	switch e := ev.(type) {
	case agent.StepEnd:
		u := a.usage
		total := server.Usage{InputTokens: u.input, CachedInputTokens: u.cached, CacheWriteTokens: u.cacheWrite, OutputTokens: u.output, Cost: u.cost}
		total.Add(e.Usage)
		a.stepEnded(total, server.StepUsage(e.Usage), &e.Context)
	case agent.ToolDraft:
		a.activity = "Working"
	case agent.ToolStart:
		a.activity = "Working"
		a.toolsRunning++
	case agent.ToolEnd:
		a.activity = "Thinking"
		a.toolsRunning = max(0, a.toolsRunning-1)
	}
}

// replay shows a saved branch as a snapshot of it would.
func (a *App) replay(branch []session.Entry) {
	info := a.info
	info.Items = server.ItemsFromEntries("s", branch)
	a.applySnapshot(info)
}

// footer is the footer as drawn.
func footer(a *App, width int) string {
	var s string
	a.ui.Do(func() { s = plainLines(a.ui.Footer.Render(width)) })
	return s
}

var scriptedProviders sync.Map

func scriptedModel(a *App) *providertest.Model {
	if m, ok := scriptedProviders.Load(a.cwd); ok {
		return m.(*providertest.Model)
	}
	return nil
}

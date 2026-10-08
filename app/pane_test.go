package app

import (
	"strings"
	"sync"
	"testing"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/daemon"
	"github.com/sebastianrcnt/atto/tui"
)

// recTerm records what is written to the terminal.
type recTerm struct {
	mu  sync.Mutex
	out strings.Builder
}

func (*recTerm) Start(func(string), func()) error { return nil }
func (*recTerm) Stop()                            {}
func (*recTerm) Size() (int, int)                 { return 80, 24 }
func (r *recTerm) Write(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.out.WriteString(s)
}

func (r *recTerm) take() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.out.String()
	r.out.Reset()
	return s
}

// paneApp is a treeApp in a daemon pane, writing to a recTerm.
func paneApp(t *testing.T, on bool) (*App, *recTerm) {
	t.Helper()
	t.Setenv("ATTO_DIR", t.TempDir())
	cwd := t.TempDir()
	rec := &recTerm{}
	a := &App{
		ui:    tui.New(rec),
		agent: agent.New(config.ModelRef{ProviderName: "t", Model: config.Model{ID: "m"}}, "", cwd),
		tools: map[string]*toolBlock{},
		cwd:   cwd,
		quit:  make(chan struct{}),
	}
	a.build()
	a.newSession("")
	t.Cleanup(a.closeSession)
	a.pane.on = on
	return a, rec
}

func TestPaneReportsSessionOnce(t *testing.T) {
	a, rec := paneApp(t, true)
	a.paneSync()
	want := daemon.MarkerSeq("session", a.sess.ID, "") + daemon.MarkerSeq("state", "idle")
	if got := rec.take(); got != want {
		t.Fatalf("first report %q, want %q", got, want)
	}
	a.paneSync()
	if got := rec.take(); got != "" {
		t.Fatalf("unchanged session reported again: %q", got)
	}
	a.nameSession("fix\x07 parser")
	a.paneSync()
	if got := rec.take(); got != daemon.MarkerSeq("session", a.sess.ID, "fix parser") {
		t.Fatalf("rename report %q", got)
	}

	// A running turn, then a question, change the state it reports.
	a.busy = true
	a.paneSync()
	if got := rec.take(); got != daemon.MarkerSeq("state", "working") {
		t.Fatalf("busy report %q", got)
	}
	a.busy = false
	a.cmdSessions("")
	a.paneSync()
	if got := rec.take(); got != daemon.MarkerSeq("state", "waiting") {
		t.Fatalf("picker report %q", got)
	}
	a.closeModal()
	a.cmdAgents("") // the center itself is not waiting for anything
	waitCenter(t, a)
	a.paneSync()
	if got := rec.take(); got != daemon.MarkerSeq("state", "idle") {
		t.Fatalf("center report %q", got)
	}
	a.closeModal()

	// Outside a pane, nothing is said.
	b, rec := paneApp(t, false)
	b.paneSync()
	if got := rec.take(); got != "" {
		t.Fatalf("direct atto wrote %q", got)
	}
}

func TestDetachCommand(t *testing.T) {
	a, rec := paneApp(t, true)
	a.cmdDetach("")
	if got := rec.take(); got != daemon.MarkerSeq("detach") {
		t.Fatalf("detach wrote %q", got)
	}
	b, rec := paneApp(t, false)
	b.cmdDetach("")
	if got := rec.take(); strings.Contains(got, "7337") {
		t.Fatalf("direct atto wrote a marker: %q", got)
	}
	if !strings.Contains(bodyText(b), "isn't running in the atto daemon") {
		t.Fatalf("notice %q", bodyText(b))
	}
}

func TestExitMenuDetachesInPane(t *testing.T) {
	a, rec := paneApp(t, true)
	canceled := 0
	a.record("user", "do the thing")
	a.busy, a.runKind = true, "turn"
	a.cancel = func() { canceled++ }
	a.requestQuit()
	if !strings.Contains(menuText(a), "2. Detach") || strings.Contains(menuText(a), "Run in background") {
		t.Fatalf("menu:\n%s", menuText(a))
	}
	rec.take()
	a.modal.HandleInput("2")
	if quitting(a) || canceled != 0 || a.modal != nil || !a.busy {
		t.Fatalf("detach must leave the task running: quit=%v canceled=%d", quitting(a), canceled)
	}
	if got := rec.take(); got != daemon.MarkerSeq("detach") {
		t.Fatalf("wrote %q", got)
	}
}

func TestAppFixturesReleaseSessionLease(t *testing.T) {
	for _, tt := range []struct {
		name string
		make func(*testing.T) *App
	}{
		{"tree", treeApp},
		{"pane", func(t *testing.T) *App { a, _ := paneApp(t, false); return a }},
		{"loaded", loadedApp},
	} {
		released := false
		t.Run(tt.name, func(t *testing.T) {
			a := tt.make(t)
			if a.unlock == nil {
				t.Fatal("fixture has no session lease")
			}
			unlock := a.unlock
			a.unlock = func() {
				unlock()
				released = true
			}
		})
		if !released {
			t.Errorf("%s fixture retained its session lease", tt.name)
		}
	}
}

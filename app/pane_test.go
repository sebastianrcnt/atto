package app

import (
	"strings"
	"sync"
	"testing"

	"github.com/sebastianrcnt/atto/daemon"
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

// paneApp is a terminal on a session in a daemon pane, writing to a
// recTerm.
func paneApp(t *testing.T, on bool) (*App, *recTerm) {
	t.Helper()
	cwd, _ := testEnv(t)
	rec := &recTerm{}
	a := startAppTerm(t, cwd, rec)
	a.ui.Do(func() { a.pane.on = on })
	return a, rec
}

func TestPaneReportsSessionOnce(t *testing.T) {
	a, rec := paneApp(t, true)
	a.paneSync()
	want := daemon.MarkerSeq("session", a.threadID, "") + daemon.MarkerSeq("state", "idle")
	if got := rec.take(); got != want {
		t.Fatalf("first report %q, want %q", got, want)
	}
	a.paneSync()
	if got := rec.take(); got != "" {
		t.Fatalf("unchanged session reported again: %q", got)
	}
	a.sessName = "fix\x07 parser"
	a.paneSync()
	if got := rec.take(); got != daemon.MarkerSeq("session", a.threadID, "fix parser") {
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
	a.busy, a.runKind = true, "turn"
	a.requestQuit()
	if !strings.Contains(menuText(a), "2. Detach") || strings.Contains(menuText(a), "Run in background") {
		t.Fatalf("menu:\n%s", menuText(a))
	}
	rec.take()
	a.modal.HandleInput("2")
	if quitting(a) || a.modal != nil || !a.busy {
		t.Fatalf("detach must leave the task running: quit=%v", quitting(a))
	}
	if got := rec.take(); got != daemon.MarkerSeq("detach") {
		t.Fatalf("wrote %q", got)
	}
}

// quitting reports whether the App was asked to quit.
func quitting(a *App) bool {
	select {
	case <-a.quit:
		return true
	default:
		return false
	}
}

func menuText(a *App) string {
	if a.modal == nil {
		return ""
	}
	return plainLines(a.modal.Render(100))
}

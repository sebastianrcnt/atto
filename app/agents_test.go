package app

import (
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/daemon"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

func centerText(a *App) string {
	return tui.StripEscapes(strings.Join(a.modal.Render(160), "\n"))
}

// fakeCenter replaces the daemon's panes and the saved sessions.
func fakeCenter(t *testing.T, panes []daemon.Pane, saved []session.Summary) {
	oldP, oldS := listPanes, listSaved
	t.Cleanup(func() { listPanes, listSaved = oldP, oldS })
	listPanes = func() ([]daemon.Pane, error) { return panes, nil }
	listSaved = func() []session.Summary { return saved }
}

func TestLeftOnEmptyPromptOpensCenter(t *testing.T) {
	a, _ := paneApp(t, false)
	fakeCenter(t, nil, nil)
	a.editor.SetText("draft")
	a.onInput("\x1b[D")
	if a.modal != nil {
		t.Fatal("← with text in the prompt moves the cursor, not into the center")
	}
	a.editor.SetText("")
	a.onInput("\x1b[D")
	if _, ok := a.modal.(*agentCenter); !ok {
		t.Fatalf("modal %T", a.modal)
	}
	a.modal.HandleInput("\x1b[D")
	if a.modal != nil {
		t.Fatal("← goes back")
	}
}

func TestCenterListsSessionsByProjectAndState(t *testing.T) {
	a, rec := paneApp(t, true)
	t.Setenv(daemon.EnvPane, "1")
	now := time.Now()
	fakeCenter(t,
		[]daemon.Pane{
			{ID: 1, Cwd: a.cwd, Session: a.sess.ID, Clients: 1, State: "idle", Active: now},
			{ID: 2, Cwd: "/w/api", Session: "s2", Name: "fix the api", State: "working", Active: now.Add(-time.Minute)},
			{ID: 3, Cwd: "/w/api", Session: "s3", Name: "answer me", State: "waiting", Active: now.Add(-2 * time.Minute)},
		},
		[]session.Summary{
			{ID: "s2", Cwd: "/w/api", Preview: "fix the api please", Branch: "main", Updated: now},
			{ID: "old", Cwd: "/w/web", Name: "css cleanup", Branch: "dev", Updated: now.Add(-3 * time.Hour)},
			{ID: "kid", Cwd: "/w/api", Preview: "a subagent", AgentOf: "s2", Updated: now},
		})
	a.cmdAgents("")
	text := centerText(a)
	for _, want := range []string{"Agent command center", "All 4", "Needs you 1", "Working 1", "Ready 1", "Inactive 1",
		"/w/api  2", "fix the api", "Working", "answer me", "Needs you", "/w/web  1", "css cleanup", "Inactive", "3h ago", "(here)", "Task details"} {
		if !strings.Contains(text, want) {
			t.Fatalf("center lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "a subagent") {
		t.Fatalf("an agent's session is listed:\n%s", text)
	}

	// Tabs filter; enter on a running session switches to its pane.
	c := a.modal.(*agentCenter)
	c.HandleInput("\t") // Needs you
	if sh := c.shown(); len(sh) != 1 || sh[0].id != "s3" {
		t.Fatalf("needs you: %+v", sh)
	}
	rec.take()
	c.HandleInput("\r")
	if a.modal != nil || !strings.Contains(rec.take(), daemon.MarkerSeq("switch", "3")) {
		t.Fatal("enter switches to the pane")
	}

	// A saved session opens in a new pane; n starts one in its project.
	a.cmdAgents("")
	c = a.modal.(*agentCenter)
	for range 4 {
		c.HandleInput("\t") // Inactive
	}
	c.HandleInput("\x1b[C") // →
	if got := rec.take(); !strings.Contains(got, daemon.MarkerSeq("open", "old", "/w/web")) {
		t.Fatalf("open wrote %q", got)
	}
	a.cmdAgents("")
	a.modal.(*agentCenter).HandleInput("n")
	if got := rec.take(); !strings.Contains(got, daemon.MarkerSeq("new", a.cwd)) {
		t.Fatalf("new wrote %q", got)
	}

	// / searches; esc clears the search, then closes.
	a.cmdAgents("")
	c = a.modal.(*agentCenter)
	for _, k := range []string{"/", "c", "s", "s"} {
		c.HandleInput(k)
	}
	if sh := c.shown(); len(sh) != 1 || sh[0].id != "old" {
		t.Fatalf("search: %+v", sh)
	}
	c.HandleInput("\r")
	c.HandleInput("\x1b")
	if a.modal == nil || len(c.shown()) != 4 {
		t.Fatal("esc clears the search first")
	}
	c.HandleInput("\x1b")
	if a.modal != nil {
		t.Fatal("esc closes")
	}
}

func TestCenterDirectResumesInPlace(t *testing.T) {
	a, _ := paneApp(t, false)
	a.nameSession("here now")
	other := session.New(a.cwd)
	other.Append(session.Entry{Type: session.TypeName, Name: "earlier work"})
	other.Close()
	fakeCenter(t, nil, []session.Summary{{ID: other.ID, Cwd: a.cwd, Name: "earlier work", Updated: time.Now().Add(-time.Hour)}})
	a.cmdResume("")
	c := a.modal.(*agentCenter)
	if c.tab != tabInactive {
		t.Fatalf("/resume opens on Inactive, tab %d", c.tab)
	}
	c.HandleInput("\r")
	if a.sess.ID != other.ID {
		t.Fatalf("resumed %s, want %s", a.sess.ID, other.ID)
	}
}

func TestStandaloneCenterPicks(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	t.Setenv(daemon.EnvPane, "")
	fakeCenter(t, []daemon.Pane{{ID: 4, Cwd: "/w", Session: "s4", Name: "four", State: "idle", Active: time.Now()}},
		[]session.Summary{{ID: "s9", Cwd: "/x", Name: "nine", Updated: time.Now().Add(-time.Hour)}})
	var picked string
	c := &agentCenter{onClose: func() {}, onSwitch: func(id int) { picked = "pane" }, onOpen: func(id, cwd string) { picked = "open " + id + " " + cwd }, onNew: func(cwd string) { picked = "new " + cwd }}
	c.reload()
	text := tui.StripEscapes(strings.Join(c.Render(160), "\n"))
	if !strings.Contains(text, "four") || !strings.Contains(text, "nine") || strings.Contains(text, "(here)") || !strings.Contains(text, "← quit") {
		t.Fatalf("center:\n%s", text)
	}
	c.HandleInput("\r")
	if picked != "pane" {
		t.Fatalf("picked %q", picked)
	}
	c.HandleInput("\x1b[B")
	c.HandleInput("\r")
	if picked != "open s9 /x" {
		t.Fatalf("picked %q", picked)
	}
	c.HandleInput("n")
	if picked != "new /x" {
		t.Fatalf("picked %q", picked)
	}
}

package app

import (
	"fmt"
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
	waitCenter(t, a)
	if a.modal != nil {
		t.Fatal("← with text in the prompt moves the cursor, not into the center")
	}
	a.editor.SetText("")
	a.onInput("\x1b[D")
	waitCenter(t, a)
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
	waitCenter(t, a)
	text := centerText(a)
	for _, want := range []string{"Agent command center", "All 5", "Needs you 1", "Working 1", "Ready 1", "Inactive 2",
		"/w/api  3", "fix the api", "Working", "answer me", "Needs you", "/w/web  1", "css cleanup", "Inactive", "3h ago", "(here)", "Task details"} {
		if !strings.Contains(text, want) {
			t.Fatalf("center lacks %q:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "└─ /root/a subagent") {
		t.Fatalf("agent tree missing:\n%s", text)
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
	waitCenter(t, a)
	c = a.modal.(*agentCenter)
	for range 4 {
		c.HandleInput("\t") // Inactive
	}
	for i, it := range c.shown() {
		if it.id == "old" {
			c.sel = i
		}
	}
	c.HandleInput("\x1b[C") // →
	if got := rec.take(); !strings.Contains(got, daemon.MarkerSeq("open", "old", "/w/web")) {
		t.Fatalf("open wrote %q", got)
	}
	a.cmdAgents("")
	waitCenter(t, a)
	a.modal.(*agentCenter).HandleInput("n")
	if got := rec.take(); !strings.Contains(got, daemon.MarkerSeq("new", a.cwd)) {
		t.Fatalf("new wrote %q", got)
	}

	// / searches; esc clears the search, then closes.
	a.cmdAgents("")
	waitCenter(t, a)
	c = a.modal.(*agentCenter)
	for _, k := range []string{"/", "c", "s", "s"} {
		c.HandleInput(k)
	}
	if sh := c.shown(); len(sh) != 1 || sh[0].id != "old" {
		t.Fatalf("search: %+v", sh)
	}
	c.HandleInput("\r")
	c.HandleInput("\x1b")
	if a.modal == nil || len(c.shown()) != 5 {
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
	waitCenter(t, a)
	c := a.modal.(*agentCenter)
	if c.tab != tabInactive {
		t.Fatalf("/resume opens on Inactive, tab %d", c.tab)
	}
	c.HandleInput("\r")
	if a.sess.ID != other.ID {
		t.Fatalf("resumed %s, want %s", a.sess.ID, other.ID)
	}
}

func TestCenterDefersResumeWhileBusy(t *testing.T) {
	a, _ := paneApp(t, false)
	original := a.sess.ID
	other := session.New(a.cwd)
	other.Append(session.Entry{Type: session.TypeName, Name: "other work"})
	other.Close()
	fakeCenter(t, nil, []session.Summary{{ID: other.ID, Cwd: a.cwd, Name: "other work", Updated: time.Now()}})

	canceled := false
	a.busy = true
	a.cancel = func() { canceled = true }
	a.cmdResume("")
	waitCenter(t, a)
	c := a.modal.(*agentCenter)
	c.HandleInput("\r")

	if !canceled {
		t.Fatal("opening another session did not cancel the active turn")
	}
	if a.pendingResume != other.Path {
		t.Fatalf("pending resume %q, want %q", a.pendingResume, other.Path)
	}
	if a.sess.ID != original {
		t.Fatalf("session changed while busy: got %s, want %s", a.sess.ID, original)
	}
	a.busy = false
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
	if !strings.Contains(text, "four") || !strings.Contains(text, "nine") || strings.Contains(text, "(here)") || !strings.Contains(text, "esc quit") {
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

// Fullscreen, the center takes the whole screen at the terminal's size; on
// a phone-narrow terminal rows drop the status and age columns.
func TestCenterScreenLayouts(t *testing.T) {
	a, _ := paneApp(t, false)
	now := time.Now().Add(-time.Minute) // saved sessions predate the current one
	var saved []session.Summary
	for i := range 40 {
		saved = append(saved, session.Summary{ID: fmt.Sprintf("s%d", i), Cwd: fmt.Sprintf("/w/p%d", i%5), Name: fmt.Sprintf("task %d", i), Updated: now.Add(-time.Duration(i) * time.Hour)})
	}
	fakeCenter(t, nil, saved)
	a.cmdAgents("")
	waitCenter(t, a)
	if a.ui.Screen == nil {
		t.Fatal("the center is not the screen")
	}
	c := a.modal.(*agentCenter)
	for _, size := range [][2]int{{160, 40}, {45, 50}, {45, 12}} {
		rows := c.RenderScreen(size[0], size[1])
		if len(rows) != size[1] {
			t.Fatalf("%v: %d rows", size, len(rows))
		}
		for _, r := range rows {
			if w := tui.VisibleWidth(r); w > size[0] {
				t.Fatalf("%v: row %d wide: %q", size, w, tui.StripEscapes(r))
			}
		}
		text := tui.StripEscapes(strings.Join(rows, "\n"))
		if size[0] < 60 && strings.Contains(text, "Inactive  ") {
			t.Fatalf("narrow rows keep the status column:\n%s", text)
		}
		if !strings.Contains(text, "more") {
			t.Fatalf("%v: no sign of more rows:\n%s", size, text)
		}
	}
	// The selection stays in view while moving down a long list.
	for range 30 {
		c.HandleInput("\x1b[B")
	}
	if text := tui.StripEscapes(strings.Join(c.RenderScreen(45, 20), "\n")); !strings.Contains(text, c.shown()[c.sel].title) {
		t.Fatalf("the selection scrolled out:\n%s", text)
	}
	// g: one list, newest first.
	c.HandleInput("g")
	if sh := c.shown(); !c.flat || !sh[0].current || sh[1].id != "s0" || sh[2].id != "s1" {
		t.Fatalf("flat order %v %v", c.shown()[0].id, c.shown()[1].id)
	}
	c.HandleInput("\x1b")
	if a.ui.Screen != nil {
		t.Fatal("closing the center gives the screen back")
	}
}

func waitCenter(t *testing.T, a *App) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ready := false
		a.ui.Do(func() { c, ok := a.modal.(*agentCenter); ready = !ok || c.loaded })
		if ready {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("center refresh did not finish")
}

func TestCenterRefreshDoesNotBlockUI(t *testing.T) {
	a, _ := paneApp(t, false)
	fakeCenter(t, nil, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	listSaved = func() []session.Summary {
		close(entered)
		<-release
		return []session.Summary{{ID: "saved", Cwd: "/work", Preview: "task", LastMessage: "fresh answer"}}
	}
	a.cmdAgents("")
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("scan did not start")
	}
	done := make(chan struct{})
	go func() { a.ui.Do(func() { close(done) }) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("disk scan held the UI lock")
	}
	close(release)
	waitCenter(t, a)
	c := a.modal.(*agentCenter)
	if c.lastMessage("saved") != "fresh answer" {
		t.Fatal("snapshot did not swap in")
	}
	a.closeModal()
}

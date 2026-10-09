package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/daemon"
	"github.com/sebastianrcnt/atto/provider/providertest"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

func centerText(a *App) string {
	return tui.StripEscapes(strings.Join(a.modal.Render(160), "\n"))
}

// fakeCenter replaces the worker and saved-session discovery.
func fakeCenter(t *testing.T, workers []daemon.Worker, saved []session.Summary) {
	oldP, oldS := listWorkers, listSaved
	t.Cleanup(func() { listWorkers, listSaved = oldP, oldS })
	listWorkers = func() ([]daemon.Worker, error) { return workers, nil }
	listSaved = func() []session.Summary { return saved }
}

func TestLeftOnEmptyPromptOpensCenter(t *testing.T) {
	a, _ := recordedApp(t)
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

func TestCenterDirectResumesInPlace(t *testing.T) {
	a, _ := recordedApp(t)
	a.sessName = "here now"
	other := session.New(a.cwd)
	other.Append(session.Entry{Type: session.TypeName, Name: "earlier work"})
	other.Close()
	fakeCenter(t, nil, []session.Summary{{ID: other.ID, Cwd: a.cwd, Name: "earlier work", Updated: time.Now().Add(-time.Hour)}})
	a.cmdResume("")
	waitCenter(t, a)
	c := a.modal.(*agentCenter)
	if c.tab != tabAll {
		t.Fatalf("/resume opens on All, tab %d", c.tab)
	}
	a.ui.Do(func() {
		for i, it := range c.shown() {
			if it.id == other.ID {
				c.sel = i
			}
		}
		c.HandleInput("\r")
	})
	within(t, a, "the resumed session", func() bool { return a.threadID == other.ID })
}

// Opening another session detaches the busy one rather than canceling it.
// Its accepted work finishes, then retention zero releases the writer.
func TestCenterDefersResumeWhileBusy(t *testing.T) {
	gate := make(chan struct{})
	a, model := liveApp(t, providertest.Reply{Text: "finished old work", Gate: gate})
	original, path := a.threadID, a.sessPath
	other := session.New(a.cwd)
	other.Append(session.Entry{Type: session.TypeName, Name: "other work"})
	other.Close()
	fakeCenter(t, nil, []session.Summary{{ID: other.ID, Cwd: a.cwd, Name: "other work", Updated: time.Now()}})
	typeLine(a, "keep working")
	model.Started(5 * time.Second)
	a.ui.Do(func() { a.cmdResume("") })
	waitCenter(t, a)
	a.ui.Do(func() {
		c := a.modal.(*agentCenter)
		for i, it := range c.shown() {
			if it.id == other.ID {
				c.sel = i
			}
		}
	})
	key(a, "\r")
	within(t, a, "resume while the old session runs", func() bool { return a.threadID == other.ID })
	var old server.ThreadInfo
	if err := a.conn.c.Call(context.Background(), "thread/read", map[string]any{"threadId": original}, &old); err != nil || !old.Busy {
		t.Fatalf("old runtime %v: %+v", err, old)
	}
	close(gate)
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		_, entries, _ := session.Load(path)
		for _, entry := range entries {
			if entry.Message != nil && strings.Contains(entry.Message.Content, "finished old work") {
				for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
					if _, locked := session.LockedBy(path); !locked && !a.conn.own.Loaded(original) {
						return
					}
				}
				t.Fatal("idle detached session retained its lease")
			}
		}
	}
	t.Fatal("detached session did not save its answer")
}

func TestStandaloneCenterPicks(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	fakeCenter(t, []daemon.Worker{{Cwd: "/w", Session: "s4", Name: "four", Started: time.Now()}},
		[]session.Summary{{ID: "s9", Cwd: "/x", Name: "nine", Updated: time.Now().Add(-time.Hour)}})
	var picked string
	c := &agentCenter{onClose: func() {}, onOpen: func(id, cwd string) { picked = "open " + id + " " + cwd }, onNew: func(cwd string) { picked = "new " + cwd }}
	c.reload()
	text := tui.StripEscapes(strings.Join(c.Render(160), "\n"))
	if !strings.Contains(text, "four") || !strings.Contains(text, "nine") || strings.Contains(text, "(here)") || !strings.Contains(text, "esc quit") {
		t.Fatalf("center:\n%s", text)
	}
	c.HandleInput("\r")
	if picked != "open s4 /w" {
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
	a, _ := recordedApp(t)
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
	a, _ := recordedApp(t)
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

// The startup picker is directory-local, with live workers first, but its
// default cursor still selects the most recently used saved conversation.
func TestResumeCenterLiveOrderAndDefault(t *testing.T) {
	now := time.Now()
	c := &agentCenter{scope: "/work", resume: true, flat: true}
	c.apply(centerSnapshot{
		workers: []daemon.Worker{{Session: "live", Cwd: "/work", Started: now.Add(-time.Hour)}},
		saved: []session.Summary{
			{ID: "recent", Cwd: "/work", Name: "last conversation", Updated: now},
			{ID: "live", Cwd: "/work", Name: "running conversation", Updated: now.Add(-time.Hour)},
			{ID: "elsewhere", Cwd: "/other", Updated: now},
		},
	})
	c.selectCurrent()
	shown := c.shown()
	if len(shown) != 2 || shown[0].id != "live" || shown[c.sel].id != "recent" {
		t.Fatalf("rows %+v selected %d", shown, c.sel)
	}
	if text := tui.StripEscapes(strings.Join(c.Render(160), "\n")); !strings.Contains(text, "(live)") {
		t.Fatalf("live marker missing: %s", text)
	}
}

func TestCenterWorkerProjectStateAndNavigation(t *testing.T) {
	now := time.Now()
	c := &agentCenter{}
	var picked string
	c.onClose = func() {}
	c.onOpen = func(id, cwd string) { picked = id + " " + cwd }
	c.onNew = func(cwd string) { picked = "new " + cwd }
	c.apply(centerSnapshot{
		workers: []daemon.Worker{
			{Session: "working", Name: "fix the API", Cwd: "/api", Busy: true, State: "working", Started: now},
			{Session: "waiting", Name: "answer me", Cwd: "/api", State: "waiting", Started: now.Add(-time.Minute)},
		},
		saved: []session.Summary{{ID: "saved", Name: "CSS cleanup", Cwd: "/web", Updated: now.Add(-time.Hour)}},
	})
	text := tui.StripEscapes(strings.Join(c.Render(160), "\n"))
	for _, want := range []string{"All 3", "Needs you 1", "Working 1", "Inactive 1", "/api  2", "/web  1", "fix the API", "CSS cleanup", "(live)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	c.HandleInput("\t")
	c.HandleInput("\r")
	if picked != "waiting /api" {
		t.Fatalf("picked %q", picked)
	}
	c.HandleInput("n")
	if picked != "new /api" {
		t.Fatalf("new %q", picked)
	}
	c.tab = tabAll
	for _, k := range []string{"/", "C", "S", "S", "\r"} {
		c.HandleInput(k)
	}
	if sh := c.shown(); len(sh) != 1 || sh[0].id != "saved" {
		t.Fatalf("search %+v", sh)
	}
	c.HandleInput("\r")
	if picked != "saved /web" {
		t.Fatalf("saved %q", picked)
	}
}

package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

type spawnCall struct{ id, path, cwd string }

// bgApp is a treeApp with a running turn, whose cancel is counted and
// whose background spawner is a stub.
func bgApp(t *testing.T) (a *App, canceled *int, spawned *[]spawnCall) {
	t.Helper()
	a = treeApp(t)
	canceled, spawned = new(int), new([]spawnCall)
	a.record("user", "do the thing")
	a.busy, a.runKind = true, "turn"
	a.cancel = func() { *canceled++ }
	a.bgx.spawn = func(id, path, cwd string) (int, string, error) {
		*spawned = append(*spawned, spawnCall{id, path, cwd})
		return 4321, filepath.Join(filepath.Dir(path), "x.bg.log"), nil
	}
	return a, canceled, spawned
}

func quitting(a *App) bool {
	select {
	case <-a.quit:
		return true
	default:
		return false
	}
}

func menuText(a *App) string {
	return tui.StripEscapes(strings.Join(a.modal.Render(80), "\n"))
}

func TestExitMenuOnlyWithRunningTurn(t *testing.T) {
	a := treeApp(t)
	a.requestQuit()
	if !quitting(a) || a.modal != nil {
		t.Fatal("an idle atto exits straight away")
	}

	a, _, _ = bgApp(t)
	a.requestQuit()
	if quitting(a) || a.modal == nil {
		t.Fatal("a running turn asks first")
	}
	got := menuText(a)
	for _, want := range []string{"› 1. Cancel task", "Stop the current task and stay in atto", "2. Run in background", "Exit atto and leave the task running", "3. Exit", "Stop the current task and exit atto", "enter select · esc back"} {
		if !strings.Contains(got, want) {
			t.Fatalf("menu lacks %q:\n%s", want, got)
		}
	}

	// A compaction or summary is not a turn.
	a, _, _ = bgApp(t)
	a.runKind = "compact"
	a.requestQuit()
	if !quitting(a) {
		t.Fatal("only turns ask")
	}

	// An active goal that will go on asks too.
	a = treeApp(t)
	g, _ := goal.New("ship it", 0)
	a.goal.Goal = g
	a.requestQuit()
	if quitting(a) || a.modal == nil {
		t.Fatal("an active goal asks first")
	}
}

func TestExitMenuKeys(t *testing.T) {
	// Esc goes back to the session untouched.
	a, canceled, spawned := bgApp(t)
	a.requestQuit()
	a.modal.HandleInput("\x1b")
	if a.modal != nil || quitting(a) || *canceled != 0 || len(*spawned) != 0 || !a.busy {
		t.Fatal("esc leaves everything as it was")
	}

	// Enter on the first row cancels the task and stays.
	a.requestQuit()
	a.modal.HandleInput("\r")
	if a.modal != nil || quitting(a) || *canceled != 1 || len(*spawned) != 0 {
		t.Fatalf("cancel: canceled=%d quit=%v", *canceled, quitting(a))
	}

	// Down, down, enter (and "3") exit; the turn is stopped by Run's exit.
	a, canceled, spawned = bgApp(t)
	a.requestQuit()
	a.modal.HandleInput("\x1b[B")
	a.modal.HandleInput("\x1b[B")
	a.modal.HandleInput("\r")
	if !quitting(a) || len(*spawned) != 0 {
		t.Fatal("exit quits without a background run")
	}
	a, _, _ = bgApp(t)
	a.requestQuit()
	a.modal.HandleInput("3")
	if !quitting(a) {
		t.Fatal("3 exits")
	}
}

func TestExitMenuCtrlKeys(t *testing.T) {
	// ctrl+c while the turn runs interrupts it, as before.
	a, canceled, _ := bgApp(t)
	a.onInput("\x03")
	if *canceled != 1 || a.modal != nil || quitting(a) {
		t.Fatal("ctrl+c still interrupts")
	}
	// ctrl+d (empty prompt) is a way to exit: menu.
	a.onInput("\x04")
	if a.modal == nil || quitting(a) {
		t.Fatal("ctrl+d asks")
	}
	// /quit asks as well.
	a, _, _ = bgApp(t)
	a.runCommand("/quit")
	if a.modal == nil || quitting(a) {
		t.Fatal("/quit asks")
	}
	// Idle: ctrl+c exits as always.
	a = treeApp(t)
	a.onInput("\x03")
	if !quitting(a) {
		t.Fatal("idle ctrl+c exits")
	}
}

func TestExitMenuDisabledBySetting(t *testing.T) {
	a, _, _ := bgApp(t)
	dir := os.Getenv("ATTO_DIR")
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"backgroundExit": false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := config.LoadSettings()
	if err != nil || st.BackgroundExit == nil || *st.BackgroundExit {
		t.Fatalf("setting not read: %v %+v", err, st.BackgroundExit)
	}
	a.bgx.off = true
	a.requestQuit()
	if !quitting(a) || a.modal != nil {
		t.Fatal("with the menu off, quitting is as it was")
	}
	// ctrl+d while busy is the editor's, as before.
	b, _, _ := bgApp(t)
	b.bgx.off = true
	b.onInput("\x04")
	if quitting(b) || b.modal != nil {
		t.Fatal("ctrl+d while busy does nothing special")
	}
}

func TestRunInBackground(t *testing.T) {
	a, canceled, spawned := bgApp(t)
	a.sessName = "my task"
	a.requestQuit()
	a.modal.HandleInput("2")
	if *canceled != 1 || !a.agent.DiscardPartial.Load() || a.modal != nil {
		t.Fatalf("the request in flight is stopped, its partial step dropped: canceled=%d", *canceled)
	}
	if quitting(a) || len(*spawned) != 0 {
		t.Fatal("nothing is handed over before the turn has stopped")
	}
	// The turn ends, interrupted.
	a.busy, a.cancel = false, nil
	a.afterRun(context.Canceled)
	if len(*spawned) != 1 || (*spawned)[0].id != a.sess.ID || (*spawned)[0].path != a.sess.Path || (*spawned)[0].cwd != a.cwd {
		t.Fatalf("spawned %+v", *spawned)
	}
	if !quitting(a) || a.agent.DiscardPartial.Load() {
		t.Fatal("atto exits")
	}
	if want := "Running in background: my task · atto resume " + a.sess.ID + " to check · log: "; !strings.HasPrefix(a.bgx.line, want) || !strings.HasSuffix(a.bgx.line, "x.bg.log") {
		t.Fatalf("line %q", a.bgx.line)
	}
	if !a.printExit() {
		t.Fatal("printExit")
	}
	// The user's message is in the file, and nothing half-written.
	_, entries, err := session.Load(a.sess.Path)
	last := ""
	for _, e := range entries {
		if e.Type == session.TypeMessage {
			last = e.Message.Content
		}
	}
	if err != nil || last != "do the thing" {
		t.Fatalf("%v %+v", err, entries)
	}
}

func TestRunInBackgroundWhenTurnFinishedFirst(t *testing.T) {
	a, _, spawned := bgApp(t)
	a.requestQuit()
	a.modal.HandleInput("2")
	a.busy, a.cancel = false, nil
	a.afterRun(nil) // it finished before the cancel landed
	if len(*spawned) != 0 || !quitting(a) || a.bgx.line != "" {
		t.Fatal("nothing left to run: plain exit")
	}
}

func TestRunInBackgroundKeepsGoal(t *testing.T) {
	a, _, spawned := bgApp(t)
	g, _ := goal.New("ship it", 5000)
	a.goal.Goal = g
	a.goal.Snapshot = a.snapshotGoal
	a.requestQuit()
	a.modal.HandleInput("2")
	a.busy, a.cancel = false, nil
	a.afterRun(context.Canceled)
	if len(*spawned) != 1 || a.goal.Goal.Status != goal.Active {
		t.Fatalf("an interrupted goal turn stays active: %+v", a.goal.Goal)
	}
	var snap bool
	_, entries, _ := session.Load(a.sess.Path)
	for _, e := range entries {
		snap = snap || e.Type == session.TypeGoal
	}
	if !snap {
		t.Fatal("the goal is in the session for the background run")
	}
}

func TestRunInBackgroundIdleGoal(t *testing.T) {
	a := treeApp(t)
	a.bgx.spawn = func(id, path, cwd string) (int, string, error) { return 1, "log", nil }
	g, _ := goal.New("ship it", 0)
	a.goal.Goal = g
	a.requestQuit()
	a.modal.HandleInput("2")
	if !quitting(a) || a.bgx.line == "" {
		t.Fatal("an idle goal is handed over directly")
	}
}

func TestCancelTaskPausesIdleGoal(t *testing.T) {
	a := treeApp(t)
	g, _ := goal.New("ship it", 0)
	a.goal.Goal = g
	a.requestQuit()
	a.modal.HandleInput("1")
	if quitting(a) || a.modal != nil || a.goal.Active() {
		t.Fatalf("cancel pauses the goal and stays: %+v", a.goal.Goal)
	}
}

func TestRunInBackgroundSpawnFails(t *testing.T) {
	a, _, _ := bgApp(t)
	a.bgx.spawn = func(id, path, cwd string) (int, string, error) { return 0, "", errors.New("no exe") }
	a.requestQuit()
	a.modal.HandleInput("2")
	a.busy, a.cancel = false, nil
	a.afterRun(context.Canceled)
	if quitting(a) || a.bgx.line != "" {
		t.Fatal("a failed hand-over stays in atto")
	}
}

func TestBackgroundWaitsForMovedCommand(t *testing.T) {
	a, canceled, _ := bgApp(t)
	a.bgx.pending, a.bgx.awaitTool = true, true
	a.backgroundEvent(agent.StepEnd{})
	if *canceled != 0 {
		t.Fatal("only the end of the command stops the turn")
	}
	a.backgroundEvent(agent.ToolEnd{})
	if *canceled != 1 {
		t.Fatal("the model request after the command is canceled")
	}
}

func TestReadOnlyLockedSession(t *testing.T) {
	a := treeApp(t)
	path := saveSession(t, a.cwd, "earlier")
	release, err := session.Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	a.resume(path)
	why := a.sess.ReadOnly()
	if !strings.HasPrefix(why, "Running in background (pid ") || !strings.HasSuffix(why, ") — read-only until it finishes") {
		t.Fatalf("banner %q", why)
	}
	if banner := tui.StripEscapes(strings.Join(a.renderReadOnly(120), "")); !strings.Contains(banner, why) {
		t.Fatalf("banner line %q", banner)
	}
	if !strings.Contains(strings.Join(userBlocks(a), "|"), "earlier") {
		t.Fatal("the saved transcript is shown")
	}
	before, _ := os.ReadFile(path)

	a.submit("hello", nil)
	a.onInput("\t")
	if a.busy || a.editor.Text() != "hello" {
		t.Fatalf("input is refused and kept: busy=%v text=%q", a.busy, a.editor.Text())
	}
	a.sess.Append(session.Entry{Type: session.TypeName, Name: "x"})
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("a read-only session is never written")
	}
	shown := false
	for _, c := range a.ui.Body.Children {
		shown = shown || strings.Contains(tui.StripEscapes(strings.Join(c.Render(100), "")), why)
	}
	if !shown {
		t.Fatal("the refusal says why")
	}

	// Commands that don't write still work; quitting leaves the run alone.
	a.editor.SetText("")
	a.requestQuit()
	if !quitting(a) || a.leaveCore() != 0 {
		t.Fatal("quit")
	}

	// ctrl+r reads it again; once the run is over it opens for writing.
	release()
	a.onInput("\x12")
	if a.sess.ReadOnly() != "" {
		t.Fatal("writable after the lock is gone")
	}
}

func TestResumePickerMarksRunning(t *testing.T) {
	a := treeApp(t)
	path := saveSession(t, a.cwd, "long job")
	p := newResumePicker(a.cwd, "")
	if got := tui.StripEscapes(strings.Join(p.Render(100), "\n")); strings.Contains(got, "running") {
		t.Fatalf("not running:\n%s", got)
	}
	release, err := session.Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	p = newResumePicker(a.cwd, "")
	if got := tui.StripEscapes(strings.Join(p.Render(100), "\n")); !strings.Contains(got, "running") {
		t.Fatalf("running not shown:\n%s", got)
	}
}

// A session open in another terminal is not opened here: the two would
// write over each other (and each other's goal). The terminal holds its
// own session, and moves the lock when it opens another.
func TestSessionOpenInAnotherTerminal(t *testing.T) {
	a := treeApp(t)
	if l, ok := session.LockedBy(a.sess.Path); !ok || l.Kind != session.KindTUI || l.PID != os.Getpid() {
		t.Fatalf("the new session is not held: %+v %v", l, ok)
	}
	path := saveSession(t, a.cwd, "earlier")
	body, _ := json.Marshal(session.LockInfo{PID: os.Getppid(), Kind: session.KindTUI}) // another atto, alive
	if err := os.WriteFile(session.LockPath(path), body, 0o644); err != nil {
		t.Fatal(err)
	}
	before := a.sess.Path
	a.resume(path)
	if a.sess.Path != before || a.sess.ReadOnly() != "" {
		t.Fatalf("opened: %s %q", a.sess.Path, a.sess.ReadOnly())
	}
	if got := goalText(a); !strings.Contains(got, "session is open in another atto (pid ") {
		t.Fatalf("not said:\n%s", got)
	}

	// Once the other has closed it, it opens here and the lock moves.
	os.Remove(session.LockPath(path))
	a.resume(path)
	if a.sess.Path != path {
		t.Fatalf("not opened: %s", a.sess.Path)
	}
	if _, ok := session.LockedBy(before); ok {
		t.Fatal("the previous session is still held")
	}
	if l, ok := session.LockedBy(path); !ok || l.PID != os.Getpid() {
		t.Fatalf("not held: %+v", l)
	}
	a.closeSession()
	if _, ok := session.LockedBy(path); ok {
		t.Fatal("closing releases it")
	}
}

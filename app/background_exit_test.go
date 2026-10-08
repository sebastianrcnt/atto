package app

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/provider/providertest"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

type spawnCall struct{ id, path, cwd string }

// bgApp has a real blocked provider turn and a stub of the process handoff.
func bgApp(t *testing.T) (*App, <-chan spawnCall) {
	t.Helper()
	gate := make(chan struct{})
	a, m := liveApp(t, providertest.Reply{Text: "partial must not be saved", Gate: gate})
	t.Cleanup(func() { close(gate) })
	spawned := make(chan spawnCall, 2)
	old := server.Spawn
	server.Spawn = func(id, path, cwd string) (string, error) {
		spawned <- spawnCall{id, path, cwd}
		return filepath.Join(filepath.Dir(path), "x.bg.log"), nil
	}
	t.Cleanup(func() { server.Spawn = old })
	typeLine(a, "do the thing")
	if m.Started(5*time.Second) == 0 {
		t.Fatal("request did not start")
	}
	within(t, a, "running turn", func() bool { return a.busy })
	return a, spawned
}

func TestExitMenuOnlyWithRunningTurn(t *testing.T) {
	a := treeApp(t)
	a.ui.Do(a.requestQuit)
	if !quitting(a) || a.modal != nil {
		t.Fatal("idle terminal did not exit")
	}
	a, _ = bgApp(t)
	a.ui.Do(a.requestQuit)
	if quitting(a) || a.modal == nil {
		t.Fatal("running turn did not ask")
	}
	for _, want := range []string{"1. Cancel task", "Stop the current task and stay in atto", "2. Run in background", "Exit atto and leave the task running", "3. Exit", "Stop the current task and exit atto", "enter select · esc back"} {
		if !strings.Contains(screen(a), want) {
			t.Fatalf("missing %q: %s", want, screen(a))
		}
	}
	a.ui.Do(func() { a.closeModal(); a.runKind = "compact"; a.requestQuit() })
	if !quitting(a) {
		t.Fatal("compaction should not show a turn exit menu")
	}
	b := testApp(t)
	g, _ := goal.New("ship it")
	b.info.Goal = &server.GoalInfo{Goal: g}
	b.conn = &conn{own: &server.Server{}}
	b.requestQuit()
	if quitting(b) || b.modal == nil {
		t.Fatal("active goal did not ask")
	}
}

func TestExitMenuKeys(t *testing.T) {
	a, spawned := bgApp(t)
	a.ui.Do(a.requestQuit)
	key(a, "\x1b")
	if a.modal != nil || quitting(a) || !a.busy {
		t.Fatal("escape changed execution")
	}
	select {
	case <-spawned:
		t.Fatal("escape spawned background work")
	default:
	}
	a.ui.Do(a.requestQuit)
	key(a, "\r")
	within(t, a, "canceled turn", func() bool { return !a.busy })
	if quitting(a) || a.modal != nil {
		t.Fatal("cancel did not stay in terminal")
	}
	for _, keys := range [][]string{{"\x1b[B", "\x1b[B", "\r"}, {"3"}} {
		b, ch := bgApp(t)
		b.ui.Do(b.requestQuit)
		for _, k := range keys {
			key(b, k)
		}
		if !quitting(b) {
			t.Fatal("exit did not quit")
		}
		select {
		case <-ch:
			t.Fatal("exit handed off")
		default:
		}
	}
}

func TestExitMenuCtrlKeys(t *testing.T) {
	a, _ := bgApp(t)
	key(a, "\x03")
	within(t, a, "interrupted turn", func() bool { return !a.busy })
	if a.modal != nil || quitting(a) {
		t.Fatal("Ctrl+C did not interrupt and stay")
	}
	a, _ = bgApp(t)
	key(a, "\x04")
	if a.modal == nil || quitting(a) {
		t.Fatal("Ctrl+D did not ask")
	}
	a, _ = bgApp(t)
	typeLine(a, "/quit")
	if a.modal == nil || quitting(a) {
		t.Fatal("/quit did not ask")
	}
	b := treeApp(t)
	key(b, "\x03")
	if !quitting(b) {
		t.Fatal("idle Ctrl+C did not exit")
	}
}

func TestExitMenuDisabledBySetting(t *testing.T) {
	a, _ := bgApp(t)
	writeTestFile(t, config.SettingsPath(), `{"backgroundExit":false}`)
	st, err := config.LoadSettings()
	if err != nil || st.BackgroundExit == nil || *st.BackgroundExit {
		t.Fatalf("setting: %+v, %v", st, err)
	}
	a.ui.Do(func() { a.applySettings(st); a.requestQuit() })
	if !quitting(a) || a.modal != nil {
		t.Fatal("disabled menu still asks")
	}
	b, _ := bgApp(t)
	b.ui.Do(func() { b.bgx.off = true })
	key(b, "\x04")
	if quitting(b) || b.modal != nil {
		t.Fatal("disabled Ctrl+D opened menu")
	}
}

func TestRunInBackground(t *testing.T) {
	a, spawned := bgApp(t)
	typeLine(a, "/name my task")
	settle(a)
	a.ui.Do(a.requestQuit)
	key(a, "2")
	within(t, a, "handoff exit", func() bool { return quitting(a) && a.bgLine != "" })
	select {
	case call := <-spawned:
		if call.id != a.threadID || call.path != a.sessPath || call.cwd != a.cwd {
			t.Fatalf("spawn: %+v", call)
		}
	default:
		t.Fatal("no handoff")
	}
	if !strings.Contains(a.bgLine, "my task") || !strings.Contains(a.bgLine, "atto resume "+a.threadID) || !strings.HasSuffix(a.bgLine, "x.bg.log") {
		t.Fatalf("line: %q", a.bgLine)
	}
	_, entries, err := session.Load(a.sessPath)
	if err != nil {
		t.Fatal(err)
	}
	var last string
	for _, e := range entries {
		if e.Message != nil {
			last = e.Message.Content
		}
	}
	if last != "do the thing" {
		t.Fatalf("partial step saved: %q", last)
	}
}

func TestRunInBackgroundWhenTurnFinishedFirst(t *testing.T) {
	a, _ := liveApp(t, providertest.Reply{Text: "finished"})
	send(t, a, "do the thing")
	a.ui.Do(a.requestQuit)
	if !quitting(a) || a.bgLine != "" {
		t.Fatal("finished turn should exit without handoff")
	}
}

func TestRunInBackgroundKeepsGoal(t *testing.T) {
	a, ch := bgApp(t)
	typeLine(a, "/goal ship it")
	settle(a)
	a.ui.Do(a.requestQuit)
	key(a, "2")
	within(t, a, "goal handoff", func() bool { return quitting(a) })
	select {
	case <-ch:
	default:
		t.Fatal("goal was not handed off")
	}
	var id, path string
	a.ui.Do(func() { id, path = a.threadID, a.sessPath })
	g, err := goal.Load(id)
	if err != nil || g == nil || g.Status != goal.Active {
		t.Fatalf("goal: %+v %v", g, err)
	}
	_, entries, _ := session.Load(path)
	snap := false
	for _, e := range entries {
		snap = snap || e.Type == session.TypeGoal
	}
	if !snap {
		t.Fatal("goal was not persisted for background continuation")
	}
}

func TestRunInBackgroundIdleGoal(t *testing.T) {
	a, ch := bgApp(t)
	typeLine(a, "/goal ship it")
	settle(a)
	// Picker gating keeps automatic goal work idle after this turn stops.
	a.ui.Do(func() { a.openModal(&tui.SelectList{}); a.rpcErr("turn/interrupt", map[string]any{"mode": "cancel"}) })
	within(t, a, "idle held goal", func() bool { return !a.busy })
	a.ui.Do(func() { a.closeModal(); a.runInBackground() })
	within(t, a, "idle goal handoff", func() bool { return quitting(a) && a.bgLine != "" })
	select {
	case <-ch:
	default:
		t.Fatal("idle goal not handed off")
	}
}

func TestCancelTaskPausesIdleGoal(t *testing.T) {
	a, _ := liveApp(t)
	a.ui.Do(func() { a.openModal(&tui.SelectList{}) })
	typeLine(a, "/goal ship it")
	settle(a)
	a.ui.Do(a.cancelTask)
	settle(a)
	if a.goalActive() || quitting(a) {
		t.Fatal("cancel did not pause the idle goal")
	}
}

func TestRunInBackgroundSpawnFails(t *testing.T) {
	a, _ := bgApp(t)
	server.Spawn = func(string, string, string) (string, error) { return "", errors.New("no exe") }
	a.ui.Do(a.requestQuit)
	key(a, "2")
	within(t, a, "handoff failure", func() bool { return !a.bgx.pending && !a.busy })
	if quitting(a) || a.bgLine != "" || !strings.Contains(shown(a), "no exe") {
		t.Fatal("failed handoff did not stay with its error")
	}
	if _, held := session.LockedBy(a.sessPath); !held {
		t.Fatal("failed handoff lost writer lease")
	}
}

func TestBackgroundWaitsForMovedCommand(t *testing.T) {
	gate := make(chan struct{})
	a, m := liveApp(t, providertest.Reply{Command: "sleep 30", Description: "long command"}, providertest.Reply{Text: "never", Gate: gate})
	t.Cleanup(func() { close(gate) })
	old := server.Spawn
	spawned := make(chan struct{}, 1)
	server.Spawn = func(string, string, string) (string, error) { spawned <- struct{}{}; return "log", nil }
	t.Cleanup(func() { server.Spawn = old })
	typeLine(a, "work")
	within(t, a, "running command", func() bool { return a.toolsRunning > 0 })
	a.ui.Do(a.runInBackground)
	within(t, a, "command handoff", func() bool { return quitting(a) })
	select {
	case <-spawned:
	default:
		t.Fatal("command was not handed off")
	}
	if len(m.Requests()) != 1 {
		t.Fatal("model continued before handoff")
	}
	_, entries, _ := session.Load(a.sessPath)
	found := false
	for _, e := range entries {
		if e.Message != nil {
			found = found || strings.Contains(e.Message.Content, "[canceled by user]")
		}
	}
	if !found {
		t.Fatal("moved command result not persisted")
	}
}

func TestReadOnlyLockedSession(t *testing.T) {
	a := treeApp(t)
	path := saveSession(t, a.cwd, "earlier")
	release, err := session.LockKind(path, session.KindBackground)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	a.ui.Do(func() { a.requestResume(path) })
	settle(a)
	why := a.readOnly
	if !strings.HasPrefix(why, "Running in background (pid ") || !strings.HasSuffix(why, ") — read-only until it finishes") {
		t.Fatalf("banner: %q", why)
	}
	if !strings.Contains(plainLines(a.renderReadOnly(150)), why) || !strings.Contains(users(a), "earlier") {
		t.Fatal("read-only banner or transcript missing")
	}
	before, _ := os.ReadFile(path)
	typeLine(a, "hello")
	key(a, "\t")
	settle(a)
	if a.busy || a.editor.Text() != "hello" {
		t.Fatal("read-only input was not refused and retained")
	}
	typeLine(a, "/name x")
	settle(a)
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("read-only view changed the session")
	}
	a.ui.Do(func() { a.editor.SetText(""); a.requestQuit() })
	if !quitting(a) {
		t.Fatal("read-only view could not quit")
	}
	release()
	key(a, "\x12")
	settle(a)
	if a.readOnly != "" {
		t.Fatal("Ctrl+R did not reopen after writer left")
	}
}

func TestResumePickerMarksRunning(t *testing.T) {
	a := treeApp(t)
	path := saveSession(t, a.cwd, "long job")
	p := newResumePicker(a.cwd, "")
	if got := p.rowMeta(p.list.Items[0].Data.(session.Summary)); strings.Contains(got, "running") {
		t.Fatalf("not running:\n%s", got)
	}
	release, err := session.LockKind(path, session.KindBackground)
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
	if l, ok := session.LockedBy(a.sessPath); !ok || l.Kind != session.KindTUI || l.PID != os.Getpid() {
		t.Fatalf("the new session is not held: %+v %v", l, ok)
	}
	path := saveSession(t, a.cwd, "earlier")
	cmd := exec.Command(os.Args[0], "-test.run=^TestTerminalLockHelper$")
	cmd.Env = append(os.Environ(), "ATTO_TEST_TERMINAL_LOCK="+path)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "held\n" {
		t.Fatalf("child: %q %v", line, err)
	}
	before := a.sessPath
	a.ui.Do(func() { a.requestResume(path) })
	settle(a)
	if a.sessPath != before || a.readOnly != "" {
		t.Fatalf("opened: %s %q", a.sessPath, a.readOnly)
	}
	if got := shown(a); !strings.Contains(got, "session is open in another atto (pid ") {
		t.Fatalf("not said:\n%s", got)
	}

	// Once the other has closed it, it opens here and the lock moves.
	_ = in.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	a.ui.Do(func() { a.requestResume(path) })
	settle(a)
	if a.sessPath != path {
		t.Fatalf("not opened: %s", a.sessPath)
	}
	if _, ok := session.LockedBy(before); ok {
		t.Fatal("the previous session is still held")
	}
	if l, ok := session.LockedBy(path); !ok || l.PID != os.Getpid() {
		t.Fatalf("not held: %+v", l)
	}
	a.shutdown()
	if _, ok := session.LockedBy(path); ok {
		t.Fatal("closing releases it")
	}
}

func TestTerminalLockHelper(t *testing.T) {
	path := os.Getenv("ATTO_TEST_TERMINAL_LOCK")
	if path == "" {
		return
	}
	release, err := session.LockTUI(path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	fmt.Println("held")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

package app

import (
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/daemon"
	"github.com/sebastianrcnt/atto/subagent"
	"github.com/sebastianrcnt/atto/tui"
)

func centerText(a *App) string {
	return tui.StripEscapes(strings.Join(a.modal.Render(300), "\n"))
}

func TestLeftOnEmptyPromptOpensCenter(t *testing.T) {
	a, _ := paneApp(t, false)
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

func TestCenterDirectShowsSessionAndSubagents(t *testing.T) {
	a, _ := paneApp(t, false)
	a.nameSession("parser work")
	st := subagent.State{Name: "tests", Parent: a.sess.ID, Session: "nosuch", Preset: "general", Model: "t/m", Task: "write tests\nmore", Created: time.Now(), Turns: 1}
	if err := subagent.Save(st); err != nil {
		t.Fatal(err)
	}
	_ = subagent.SaveTurn(a.sess.ID, "tests", subagent.Turn{N: 1, Status: subagent.Done, Queued: time.Now()})
	a.cmdAgents("")
	text := centerText(a)
	for _, want := range []string{"parser work", "this terminal", "└ tests", "general · t/m · done", "not in the daemon"} {
		if !strings.Contains(text, want) {
			t.Fatalf("center lacks %q:\n%s", want, text)
		}
	}
	// Enter on the subagent: its report, read-only; ← back to the list.
	a.modal.HandleInput("\x1b[B")
	a.modal.HandleInput("\r")
	text = centerText(a)
	if !strings.Contains(text, "tests · general · t/m · turn 1 done") || !strings.Contains(text, "task: write tests") || !strings.Contains(text, "(no answer yet)") {
		t.Fatalf("report:\n%s", text)
	}
	a.modal.HandleInput("\x1b")
	if !strings.Contains(centerText(a), "└ tests") {
		t.Fatal("esc in a report goes back to the list")
	}
}

func TestCenterSwitchesPane(t *testing.T) {
	a, rec := paneApp(t, true)
	t.Setenv(daemon.EnvPane, "1")
	old := listPanes
	t.Cleanup(func() { listPanes = old })
	listPanes = func() ([]daemon.Pane, error) {
		return []daemon.Pane{
			{ID: 1, Cwd: a.cwd, Session: a.sess.ID, Clients: 1},
			{ID: 2, Cwd: "/elsewhere", Session: "s2", Name: "other work", Clients: 0},
		}, nil
	}
	a.cmdAgents("")
	text := centerText(a)
	if !strings.Contains(text, "#1 (new session)") || !strings.Contains(text, "#2 other work") || !strings.Contains(text, "detached") {
		t.Fatalf("center:\n%s", text)
	}
	rec.take()
	a.modal.HandleInput("\x1b[B")
	a.modal.HandleInput("\r")
	if a.modal != nil {
		t.Fatal("switching closes the center")
	}
	if got := rec.take(); !strings.Contains(got, daemon.MarkerSeq("switch", "2")) {
		t.Fatalf("wrote %q", got)
	}
	// Enter on this session just closes.
	a.cmdAgents("")
	rec.take()
	a.modal.HandleInput("\r")
	if a.modal != nil || strings.Contains(rec.take(), "switch") {
		t.Fatal("enter on this session closes without switching")
	}
}

func TestStandaloneCenterPicksAPane(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	t.Setenv(daemon.EnvPane, "")
	old := listPanes
	t.Cleanup(func() { listPanes = old })
	listPanes = func() ([]daemon.Pane, error) {
		return []daemon.Pane{{ID: 4, Cwd: "/w", Session: "s4", Name: "four", Clients: 1}, {ID: 7, Cwd: "/x", Name: "seven"}}, nil
	}
	closed, picked := false, 0
	c := &agentCenter{onClose: func() { closed = true }, onSwitch: func(id int) { picked = id }}
	c.reload()
	text := tui.StripEscapes(strings.Join(c.Render(200), "\n"))
	if !strings.Contains(text, "#4 four") || !strings.Contains(text, "1 terminal") || !strings.Contains(text, "#7 seven") || !strings.Contains(text, "enter attach") || strings.Contains(text, "this terminal") {
		t.Fatalf("center:\n%s", text)
	}
	c.HandleInput("\x1b[B")
	c.HandleInput("\r")
	if picked != 7 || !closed {
		t.Fatalf("picked %d closed %v", picked, closed)
	}

	listPanes = func() ([]daemon.Pane, error) { return nil, nil }
	c = &agentCenter{onClose: func() {}, onSwitch: func(int) {}}
	c.reload()
	if text := tui.StripEscapes(strings.Join(c.Render(200), "\n")); !strings.Contains(text, "no atto is running") {
		t.Fatalf("empty center:\n%s", text)
	}
	c.HandleInput("\r") // nothing to open: no panic
}

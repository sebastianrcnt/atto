package app

import (
	"slices"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/goal"
)

const ctrlEnterKey = "\x1b[27;5;13~" // modifyOtherKeys; kitty's is \x1b[13;5u

// press delivers a key as the TUI does: the app first, then the editor.
func press(a *App, key string) {
	if !a.onInput(key) {
		a.editor.HandleInput(key)
	}
}

// Ctrl+Enter during a turn interrupts it and starts a new one with the
// steers not yet delivered and the draft, in order. A goal is not paused:
// it waits for the user after the new turn.
func TestCtrlEnterSendsNow(t *testing.T) {
	model := newRemoteModel(t)
	a := remoteApp(t, model)
	a.ui.Do(func() {
		a.queuePaused = true // no goal turn starts by itself
		a.cmdGoal("ship it")
		a.startTurn("block", nil)
	})
	within(t, a, "the blocking request", func() bool { return model.blocks() == 1 })
	a.ui.Do(func() {
		a.editor.SetText("steer me")
		press(a, "\r")
		a.editor.SetText("now")
		press(a, ctrlEnterKey)
		if a.editor.Text() != "" || a.sendNow == nil {
			t.Errorf("editor %q, sendNow %v", a.editor.Text(), a.sendNow)
		}
	})
	within(t, a, "the new turn's answer", func() bool {
		return !a.busy && strings.Contains(bodyText(a), "answer to steer me")
	})
	a.ui.Do(func() {
		if got := userBlocks(a); !slices.Equal(got, []string{"block", "steer me\n\nnow"}) {
			t.Errorf("user messages %q", got)
		}
		if g := a.goal.Goal; g.Status != goal.Active || g.Note != "" || !a.goal.Held() {
			t.Errorf("goal %+v, held %v", g, a.goal.Held())
		}
		if a.sendNow != nil || len(a.pendingSteers) != 0 || a.sendSteersAfterInterrupt {
			t.Error("send-now state left over")
		}
	})
}

// With nothing to send, Ctrl+Enter during a turn does nothing; with only
// steers pending it sends those, as Esc does; commands and shell lines are
// not sent over the turn.
func TestCtrlEnterWhileBusy(t *testing.T) {
	a := treeApp(t)
	a.busy, a.runKind = true, "turn"
	canceled := 0
	a.cancel = func() { canceled++ }

	press(a, ctrlEnterKey)
	if canceled != 0 {
		t.Fatal("interrupted with nothing to send")
	}
	a.editor.SetText("/nope")
	press(a, ctrlEnterKey)
	if canceled != 0 || a.sendNow != nil {
		t.Fatal("a command interrupted the turn")
	}
	a.steer("first")
	press(a, "\x07") // the fallback key
	if canceled != 1 || !a.sendSteersAfterInterrupt || a.sendNow != nil {
		t.Fatalf("canceled %d, send steers %v, sendNow %v", canceled, a.sendSteersAfterInterrupt, a.sendNow)
	}
	a.sendSteersAfterInterrupt = false
	a.editor.SetText("draft")
	press(a, "\x1b[13;5u")
	if canceled != 2 || a.sendNow == nil || a.sendNow.text != "draft" {
		t.Fatalf("canceled %d, sendNow %+v", canceled, a.sendNow)
	}
	// A second one while the first is on its way is a steer, as Enter.
	a.editor.SetText("again")
	press(a, ctrlEnterKey)
	if canceled != 2 || a.sendNow.text != "draft" || a.pendingSteers[len(a.pendingSteers)-1] != "again" {
		t.Fatalf("canceled %d, sendNow %+v, steers %q", canceled, a.sendNow, a.pendingSteers)
	}
}

// When no turn runs, Ctrl+Enter is Enter.
func TestCtrlEnterIdleIsEnter(t *testing.T) {
	model := newRemoteModel(t)
	a := remoteApp(t, model)
	a.ui.Do(func() {
		a.editor.SetText("hello")
		press(a, ctrlEnterKey)
		if a.editor.Text() != "" || !a.busy || a.sendNow != nil {
			t.Errorf("editor %q, busy %v", a.editor.Text(), a.busy)
		}
	})
	within(t, a, "the answer", func() bool { return !a.busy && strings.Contains(bodyText(a), "answer to hello") })
	a.ui.Do(func() {
		if got := userBlocks(a); !slices.Equal(got, []string{"hello"}) {
			t.Errorf("user messages %q", got)
		}
	})
}

// The hint shows while a turn runs.
func TestSendNowHint(t *testing.T) {
	a := treeApp(t)
	a.busy, a.runKind, a.activity = true, "turn", "Thinking"
	a.runStart = a.clock()
	got := strings.Join(a.renderActivity(120), "\n")
	if !strings.Contains(got, "esc to interrupt") || !strings.Contains(got, "ctrl+enter to send now") {
		t.Fatalf("activity line %q", got)
	}
}

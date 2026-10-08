package app

import "testing"

// Shift+Left takes back the last steer the turn has not taken yet; once
// delivered, it stays.
func TestEditLastSteer(t *testing.T) {
	a := treeApp(t)
	a.steer("first")
	a.steer("why so slow?")
	a.editor.SetText("draft")
	a.onInput("\x1b[1;2D") // shift+left
	if a.editor.Text() != "why so slow?\ndraft" || len(a.turns.Steers) != 1 || a.turns.Steers[0] != "first" {
		t.Fatalf("editor %q, pending %q", a.editor.Text(), a.turns.Steers)
	}
	if got := a.agent.DrainSteers(); len(got) != 1 || got[0] != "first" {
		t.Fatalf("agent steers %q", got)
	}
	// "first" was delivered (drained): it is not taken back.
	a.editor.SetText("")
	if a.editLastSteer() || a.editor.Text() != "" {
		t.Fatalf("a delivered steer came back: %q", a.editor.Text())
	}
}

// A turn the model never answered gives the typed text back, so it can be
// sent again; a draft typed meanwhile is not overwritten, and messages the
// user did not type (skills, goal turns) do not come back.
func TestFailedTurnRestoresTypedText(t *testing.T) {
	model := newRemoteModel(t)
	a := remoteApp(t, model)
	a.ui.Do(func() {
		a.turns.QueuePaused = true
		a.startTurn("fail", nil)
	})
	within(t, a, "the failed turn", func() bool { return !a.turns.Busy })
	a.ui.Do(func() {
		if a.editor.Text() != "fail" {
			t.Errorf("editor %q, want the failed message back", a.editor.Text())
		}
		a.editor.SetText("")
		a.startTurn("fail", nil)
		a.editor.SetText("typed meanwhile")
	})
	within(t, a, "the second failed turn", func() bool { return !a.turns.Busy })
	a.ui.Do(func() {
		if a.editor.Text() != "typed meanwhile" {
			t.Errorf("editor %q, the draft was overwritten", a.editor.Text())
		}
		a.editor.SetText("")
		a.runTurn("fail", nil, false)
	})
	within(t, a, "the untyped failed turn", func() bool { return !a.turns.Busy })
	a.ui.Do(func() {
		if a.editor.Text() != "" {
			t.Errorf("editor %q, only typed messages come back", a.editor.Text())
		}
		a.startTurn("hello", nil)
	})
	within(t, a, "the answer", func() bool { return !a.turns.Busy })
	a.ui.Do(func() {
		if a.editor.Text() != "" {
			t.Errorf("editor %q after a good turn", a.editor.Text())
		}
	})
}

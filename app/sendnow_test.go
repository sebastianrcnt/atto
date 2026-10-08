package app

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/provider/providertest"
)

const ctrlEnterKey = "\x1b[27;5;13~"

func press(a *App, k string) {
	if !a.onInput(k) {
		a.editor.HandleInput(k)
	}
}

func TestCtrlEnterSendsNow(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	a, m := liveApp(t, providertest.Reply{Gate: gate}, providertest.Reply{Text: "answered"})
	// Pause the goal before input; resuming during a user turn holds it until
	// the user explicitly continues, but send-now must not pause the goal.
	typeLine(a, "/goal ship it")
	m.Started(5 * time.Second)
	typeLine(a, "/goal pause")
	key(a, "\x03")
	waitIdle(t, a)
	m.SetScript(providertest.Reply{Gate: gate}, providertest.Reply{Text: "answer to steer me"})
	typeLine(a, "block")
	m.Started(5 * time.Second)
	typeLine(a, "/goal resume")
	settle(a)
	typeLine(a, "steer me")
	settle(a)
	a.ui.Do(func() { a.editor.SetText("now") })
	key(a, ctrlEnterKey)
	within(t, a, "replacement answer", func() bool { return !a.busy && strings.Contains(bodyText(a), "answer to steer me") })
	waitIdle(t, a)
	a.ui.Do(func() {
		us := userBlocks(a)
		if len(us) < 2 || !slices.Equal(us[len(us)-2:], []string{"block", "steer me\n\nnow"}) {
			t.Errorf("users %q", us)
		}
		if g := a.theGoal(); g == nil || g.Status != goal.Active || g.Note != "" || !a.goalHeld() {
			t.Errorf("goal %+v held=%v", g, a.goalHeld())
		}
		if len(a.pending.Steers) != 0 {
			t.Errorf("pending %+v", a.pending)
		}
	})
}

func TestCtrlEnterWhileBusy(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	a, m := liveApp(t, providertest.Reply{Gate: gate}, providertest.Reply{Text: "first"})
	typeLine(a, "block")
	m.Started(5 * time.Second)
	key(a, ctrlEnterKey)
	settle(a)
	a.ui.Do(func() { a.editor.SetText("/nope") })
	key(a, ctrlEnterKey)
	settle(a)
	if len(m.Requests()) != 1 {
		t.Fatal("empty input or slash command interrupted")
	}
	typeLine(a, "first")
	settle(a)
	key(a, "\x07")
	within(t, a, "pending steer sent", func() bool { return !a.busy && strings.Contains(bodyText(a), "first") })
	m.SetScript(providertest.Reply{Gate: gate}, providertest.Reply{Text: "draft"})
	typeLine(a, "block again")
	within(t, a, "running second turn", func() bool { return a.busy })
	m.Started(5 * time.Second)
	a.ui.Do(func() { a.editor.SetText("draft") })
	key(a, "\x1b[13;5u")
	within(t, a, "draft replacement", func() bool { return !a.busy && strings.Contains(strings.Join(userBlocks(a), ","), "draft") })
}

func TestCtrlEnterIdleIsEnter(t *testing.T) {
	a, _ := liveApp(t, providertest.Reply{Text: "answer to hello"})
	a.ui.Do(func() { a.editor.SetText("hello") })
	key(a, ctrlEnterKey)
	within(t, a, "answer", func() bool { return !a.busy && strings.Contains(bodyText(a), "answer to hello") })
	if got := users(a); got != "hello" {
		t.Fatalf("users %q", got)
	}
}

func TestSendNowHint(t *testing.T) {
	a := testApp(t)
	a.busy, a.runKind, a.activity = true, "turn", "Thinking"
	a.runStart = a.clock()
	got := strings.Join(a.renderActivity(120), "\n")
	if !strings.Contains(got, "esc to interrupt") || !strings.Contains(got, "ctrl+enter to send now") {
		t.Fatalf("activity %q", got)
	}
}

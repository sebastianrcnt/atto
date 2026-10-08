package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agent"
)

func TestTurnCancellationCauses(t *testing.T) {
	for _, tt := range []struct {
		name string
		stop func(*App)
		user bool
	}{
		{"Esc", func(a *App) { press(a, "\x1b") }, true},
		{"Ctrl+C", func(a *App) { press(a, "\x03") }, true},
		{"Ctrl+Enter", func(a *App) { a.editor.SetText("now"); press(a, ctrlEnterKey) }, true},
		{"remote interrupt", func(a *App) { a.interrupt() }, true},
		{"cancel task", func(a *App) { a.cancelTask() }, true},
		{"quit", func(a *App) { a.turns.Cancel(nil) }, false},
		{"navigation", func(a *App) { a.moveTo("old", nil) }, false},
		{"resume", func(a *App) { a.resume("other") }, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := treeApp(t)
			ctx, err := a.turns.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer a.turns.End()
			a.runKind = "turn"
			tt.stop(a)
			if ctx.Err() == nil || errors.Is(context.Cause(ctx), agent.ErrUserInterrupt) != tt.user {
				t.Fatalf("cause %v, want user=%v", context.Cause(ctx), tt.user)
			}
		})
	}
}

func TestCtrlBRequestsUserShellBackground(t *testing.T) {
	a := treeApp(t)
	a.shell = &shellRun{background: make(chan struct{}, 1)}
	press(a, "\x02")
	select {
	case <-a.shell.background:
	default:
		t.Fatal("Ctrl+B did not reach the user-entered command")
	}
}

func TestQuitDoesNotStartQueuedTurn(t *testing.T) {
	model := newRemoteModel(t)
	a := remoteApp(t, model)
	a.ui.Do(func() { a.startTurn("block", nil) })
	within(t, a, "blocking request", func() bool { return model.blocks() == 1 })
	a.ui.Do(func() {
		a.turns.Queued = append(a.turns.Queued, queuedInput{text: "next"})
		a.doQuit()
		a.turns.Cancel(nil)
	})
	select {
	case <-a.runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("quitting turn did not finish")
	}
	a.ui.Do(func() {
		if a.turns.Busy || len(a.turns.Queued) != 1 {
			t.Fatal("quitting started queued work")
		}
	})
}

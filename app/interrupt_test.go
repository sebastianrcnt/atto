package app

import (
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/provider/providertest"
	"github.com/sebastianrcnt/atto/session"
)

func TestClientInterruptKeys(t *testing.T) {
	for _, k := range []string{"\x1b", "\x03", ctrlEnterKey, "remote", "cancel"} {
		t.Run(k, func(t *testing.T) {
			a, m := liveApp(t, providertest.Reply{Command: "sleep 30", Description: "long command"}, providertest.Reply{Text: "done"})
			typeLine(a, "run")
			m.Started(5 * time.Second)
			within(t, a, "running command", func() bool { return a.toolsRunning > 0 })
			switch k {
			case "remote":
				a.ui.Do(func() { a.interrupt() })
			case "cancel":
				a.ui.Do(a.cancelTask)
			case ctrlEnterKey:
				a.ui.Do(func() { a.editor.SetText("now") })
				key(a, k)
			default:
				key(a, k)
			}
			within(t, a, "interrupted command detached", func() bool {
				return strings.Contains(bodyText(a), "Interrupted") || strings.Contains(bodyText(a), "background")
			})
			waitIdle(t, a)
			_, es, err := session.Load(a.sessPath)
			if err != nil {
				t.Fatal(err)
			}
			text := ""
			for _, e := range es {
				if e.Message != nil {
					text += e.Message.Content
				}
			}
			if !strings.Contains(text, "[canceled by user]") {
				t.Fatalf("user interrupt did not detach: %s", text)
			}
		})
	}
}

func TestQuitDoesNotStartQueuedTurn(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	a, m := liveApp(t, providertest.Reply{Gate: gate})
	typeLine(a, "block")
	m.Started(5 * time.Second)
	a.ui.Do(func() { a.editor.SetText("next") })
	key(a, "\t")
	settle(a)
	a.ui.Do(a.doQuit)
	if err := a.shutdown(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if len(m.Requests()) != 1 {
		t.Fatalf("queued turn started on quit: %v", m.Requests())
	}
}

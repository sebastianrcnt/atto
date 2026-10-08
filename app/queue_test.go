package app

import (
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/provider/providertest"
)

func TestEditLastSteer(t *testing.T) {
	gate := make(chan struct{})
	defer func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	}()
	a, m := liveApp(t, providertest.Reply{Text: "answer", Gate: gate})
	typeLine(a, "start")
	m.Started(5 * time.Second)
	typeLine(a, "first")
	typeLine(a, "why so slow?")
	settle(a)
	a.ui.Do(func() { a.editor.SetText("draft") })
	key(a, "\x1b[1;2D")
	within(t, a, "takeback", func() bool {
		return a.editor.Text() == "why so slow?\ndraft" && len(a.pending.Steers) == 1 && a.pending.Steers[0] == "first"
	})
	// The model consumes the remaining steer when the stream ends. Once
	// consumed, taking it back cannot recover it.
	a.ui.Do(func() { a.editor.SetText("") })
	close(gate)
	waitIdle(t, a)
	key(a, "\x1b[1;2D")
	settle(a)
	a.ui.Do(func() {
		if a.editor.Text() != "" {
			t.Errorf("delivered steer came back: %q", a.editor.Text())
		}
	})

	if reqs := m.Requests(); len(reqs) < 2 || !strings.Contains(reqs[1], "first") || strings.Contains(reqs[1], "why so slow?") {
		t.Fatalf("requests %v", reqs)
	}
}

func TestFailedTurnRestoresTypedText(t *testing.T) {
	a, m := liveApp(t, providertest.Reply{Status: 404})
	typeLine(a, "fail")
	within(t, a, "failed message", func() bool { return a.editor.Text() == "fail" })
	gate := make(chan struct{})
	m.SetScript(providertest.Reply{Status: 404, Gate: gate})
	a.ui.Do(func() { a.editor.SetText("") })
	typeLine(a, "fail")
	m.Started(5 * time.Second)
	a.ui.Do(func() { a.editor.SetText("typed meanwhile") })
	close(gate)
	waitIdle(t, a)
	a.ui.Do(func() {
		if a.editor.Text() != "typed meanwhile" {
			t.Errorf("draft overwritten: %q", a.editor.Text())
		}
		a.editor.SetText("")
	})
	// Skill prompts are runtime-generated, not typed input, and are not recovered.
	writeTestFile(t, a.cwd+"/.atto/skills/fail/SKILL.md", "---\nname: fail\ndescription: fail\n---\nfail")
	typeLine(a, "/reload")
	settle(a)
	typeLine(a, "/fail")
	waitIdle(t, a)
	a.ui.Do(func() {
		if a.editor.Text() != "" {
			t.Errorf("untyped input recovered: %q", a.editor.Text())
		}
	})
	m.SetScript(providertest.Reply{Text: "ok"})
	send(t, a, "hello")
	a.ui.Do(func() {
		if a.editor.Text() != "" {
			t.Errorf("good turn recovered: %q", a.editor.Text())
		}
	})
}

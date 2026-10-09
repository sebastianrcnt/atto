package app

import (
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/provider/providertest"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

func TestShellMode(t *testing.T) {
	for in, want := range map[string]string{"": "", "x": "", "!": "!", "  !ls": "!", "!!": "!!", " !!ls": "!!"} {
		if got := shellMode(in); got != want {
			t.Errorf("shellMode(%q)=%q want %q", in, got, want)
		}
	}
}

// waitShell waits for the command being run to finish.
func waitShell(t *testing.T, a *App) {
	t.Helper()
	settle(a)
	within(t, a, "shell completion", func() bool { return a.shellBlk != nil && a.shellBlk.done })
	settle(a)
}

func shellBlocks(a *App) []*shellBlock {
	var out []*shellBlock
	for _, c := range a.ui.Body.Children {
		if g, ok := c.(gap); ok {
			if b, ok := g.Component.(*shellBlock); ok {
				out = append(out, b)
			}
		}
	}
	return out
}

func shellText(a *App) string {
	var b strings.Builder
	for _, s := range shellBlocks(a) {
		b.WriteString(tui.StripEscapes(strings.Join(s.Render(80), "\n")) + "\n")
	}
	return b.String()
}

func contextText(a *App) string {
	var b strings.Builder
	for _, m := range restoredAgent(a).Messages() {
		b.WriteString(m.Role + ": " + m.Content + "\n")
	}
	return b.String()
}

func bashEntries(a *App) []session.BashExec {
	var out []session.BashExec
	_, entries, _ := session.Load(a.sessPath)
	for _, e := range entries {
		if e.Type == session.TypeBashExecution {
			out = append(out, *e.Bash)
		}
	}
	return out
}

func TestShellCommandJoinsContext(t *testing.T) {
	a, _ := liveApp(t)
	typeLine(a, "!echo hello")
	waitShell(t, a)
	if got := shellText(a); !strings.Contains(got, "! echo hello") || !strings.Contains(got, "hello") || !strings.Contains(got, "✓") {
		t.Fatalf("block:\n%s", got)
	}
	msgs := restoredAgent(a).Messages()
	if len(msgs) != 1 || msgs[0].Role != "user" || msgs[0].Content != "Ran `echo hello`\n```\nhello\n```" {
		t.Fatalf("context: %+v", msgs)
	}
	if bs := bashEntries(a); len(bs) != 1 || bs[0].Command != "echo hello" || bs[0].Exclude || bs[0].Output != "hello" {
		t.Fatalf("entries: %+v", bs)
	}
	if a.busy || len(userBlocks(a)) != 0 {
		t.Fatal("a shell command is not a turn")
	}
}

func TestShellExcludedStaysOutOfContext(t *testing.T) {
	a, _ := liveApp(t)
	typeLine(a, "!!echo secret")
	waitShell(t, a)
	if len(restoredAgent(a).Messages()) != 0 {
		t.Fatalf("context: %s", contextText(a))
	}
	if bs := bashEntries(a); len(bs) != 1 || !bs[0].Exclude {
		t.Fatalf("entries: %+v", bs)
	}
	if !strings.Contains(shellText(a), "not sent to the model") {
		t.Fatalf("block:\n%s", shellText(a))
	}
}

func TestShellEmptyCommandIsAMessage(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	a, m := liveApp(t, providertest.Reply{Gate: gate})
	typeLine(a, "start")
	m.Started(5 * time.Second)
	typeLine(a, "!")
	typeLine(a, "!!")
	settle(a)
	a.ui.Do(func() {
		if len(a.pending.Steers) != 2 || len(shellBlocks(a)) != 0 {
			t.Fatalf("pending %+v", a.pending)
		}
	})
}

func TestShellExitCodeInContext(t *testing.T) {
	a, _ := liveApp(t)
	typeLine(a, "!exit 3")
	waitShell(t, a)
	if got := contextText(a); !strings.Contains(got, "(no output)\n\nCommand exited with code 3") {
		t.Fatalf("context: %s", got)
	}
	if got := shellText(a); !strings.Contains(got, "exit 3") {
		t.Fatalf("block:\n%s", got)
	}
}

func TestShellDuringTurnWaitsForTheEnd(t *testing.T) {
	gate := make(chan struct{})
	a, m := liveApp(t, providertest.Reply{Gate: gate, Text: "ok"})
	typeLine(a, "start")
	m.Started(5 * time.Second)
	typeLine(a, "!echo hello")
	waitShell(t, a)
	if bs := bashEntries(a); len(bs) != 0 {
		t.Fatalf("persisted during turn: %+v", bs)
	}
	a.ui.Do(func() {
		if !strings.Contains(shellText(a), "after this turn") {
			t.Errorf("block %s", shellText(a))
		}
	})
	close(gate)
	waitIdle(t, a)
	if got := contextText(a); !strings.Contains(got, "Ran `echo hello`") {
		t.Fatalf("context %q", got)
	}
	if len(bashEntries(a)) != 1 {
		t.Fatal("missing shell entry")
	}
	a.ui.Do(func() {
		if strings.Contains(shellText(a), "after this turn") {
			t.Errorf("block %s", shellText(a))
		}
	})
}

func TestShellRefusedWhileOneRuns(t *testing.T) {
	a, _ := liveApp(t)
	typeLine(a, "!sleep 30")
	within(t, a, "running shell", func() bool { return a.shellBlk != nil && !a.shellBlk.done })
	typeLine(a, "!echo second")
	within(t, a, "refused command recovered", func() bool { return a.editor.Text() == "!echo second" })
	a.ui.Do(func() {
		if len(shellBlocks(a)) != 1 {
			t.Fatal("second command ran")
		}
	})
	key(a, "\x1b")
	waitShell(t, a)
}

func TestShellCancel(t *testing.T) {
	a, _ := liveApp(t)
	typeLine(a, "!sleep 30")
	within(t, a, "running shell", func() bool { return a.shellBlk != nil && !a.shellBlk.done })
	key(a, "\x1b")
	waitShell(t, a)
	if got := contextText(a); !strings.Contains(got, "(command cancelled)") {
		t.Fatalf("context %s", got)
	}
	a.ui.Do(func() {
		if !strings.Contains(shellText(a), "canceled") {
			t.Fatalf("block %s", shellText(a))
		}
	})
}

func TestShellPersistsAndReplaysTheSame(t *testing.T) {
	a, _ := liveApp(t)
	typeLine(a, "!echo one; echo two")
	waitShell(t, a)
	typeLine(a, "!!exit 2")
	waitShell(t, a)
	var live string
	a.ui.Do(func() { live = shellText(a) })
	before := contextText(a)

	_, entries, _ := session.Load(a.sessPath)
	a.ui.Do(func() {
		a.ui.Body.Clear()
		a.replay(session.Active(entries))
		if got := shellText(a); got != live {
			t.Fatalf("replay differs\nlive:\n%s\nreplayed:\n%s", live, got)
		}
	})

	if got := contextText(a); got != before || strings.Contains(got, "exit") {
		t.Fatalf("restored context %q, was %q", got, before)
	}
}

func TestShellBashModeInput(t *testing.T) {
	a, _ := liveApp(t)
	plain := tui.StripEscapes(strings.Join(a.renderInput(60), "\n"))
	if strings.Contains(plain, "bash mode") {
		t.Fatal("hint without a command")
	}
	a.editor.SetText("  !ls")
	// The typed "!" is the mode; the prompt stays, so it shows once.
	if got := tui.StripEscapes(strings.Join(a.renderInput(60), "\n")); !strings.Contains(got, "bash mode") || !strings.Contains(got, "› ") || strings.Contains(got, "! ") {
		t.Fatalf("input:\n%s", got)
	}
	a.editor.SetText("!!ls")
	if got := tui.StripEscapes(strings.Join(a.renderInput(60), "\n")); !strings.Contains(got, "not sent to the model") {
		t.Fatalf("input:\n%s", got)
	}
}

func TestShellQueueKeyRunsAtOnce(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	a, m := liveApp(t, providertest.Reply{Gate: gate})
	typeLine(a, "start")
	m.Started(5 * time.Second)
	a.ui.Do(func() { a.editor.SetText("!echo hi") })
	key(a, "\t")
	waitShell(t, a)
	a.ui.Do(func() {
		if len(a.pending.Queued) != 0 || len(shellBlocks(a)) != 1 {
			t.Fatalf("queued %+v", a.pending)
		}
	})
}

// Restore a separate reader from the persisted branch to check precisely
// which shell results the runtime saves for the model. App never owns it.
func restoredAgent(a *App) *agent.Agent {
	r := agent.New(config.ModelRef{}, "", a.cwd)
	_, entries, _ := session.Load(a.sessPath)
	r.Restore(session.Active(entries))
	return r
}

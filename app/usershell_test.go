package app

import (
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

func TestParseShell(t *testing.T) {
	for _, c := range []struct {
		in      string
		cmd     string
		exclude bool
		ok      bool
	}{
		{"!ls", "ls", false, true},
		{"! ls -la ", "ls -la", false, true},
		{"!!ls", "ls", true, true},
		{"!! ls", "ls", true, true},
		{"!!!x", "!x", true, true},
		{"!", "", false, false},
		{"!!", "", true, false},
		{"!  ", "", false, false},
		{"hello !ls", "", false, false},
		{"/help", "", false, false},
	} {
		cmd, ex, ok := parseShell(c.in)
		if cmd != c.cmd || ok != c.ok || (ok && ex != c.exclude) {
			t.Errorf("parseShell(%q) = %q %v %v; want %q %v %v", c.in, cmd, ex, ok, c.cmd, c.exclude, c.ok)
		}
	}
	for in, want := range map[string]string{"": "", "x": "", "!": "!", "  !ls": "!", "!!": "!!", " !!ls": "!!"} {
		if got := shellMode(in); got != want {
			t.Errorf("shellMode(%q) = %q, want %q", in, got, want)
		}
	}
}

// waitShell waits for the command being run to finish.
func waitShell(t *testing.T, a *App) {
	t.Helper()
	for range 500 {
		running := true
		a.ui.Do(func() { running = a.shell != nil })
		if !running {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the shell command did not finish")
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
	for _, m := range a.agent.Messages() {
		b.WriteString(m.Role + ": " + m.Content + "\n")
	}
	return b.String()
}

func bashEntries(a *App) []session.BashExec {
	var out []session.BashExec
	for _, e := range a.loadSession() {
		if e.Type == session.TypeBashExecution {
			out = append(out, *e.Bash)
		}
	}
	return out
}

func TestShellCommandJoinsContext(t *testing.T) {
	a := treeApp(t)
	a.submit("!echo hello", nil)
	waitShell(t, a)
	if got := shellText(a); !strings.Contains(got, "! echo hello") || !strings.Contains(got, "hello") || !strings.Contains(got, "✓") {
		t.Fatalf("block:\n%s", got)
	}
	msgs := a.agent.Messages()
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
	a := treeApp(t)
	a.submit("!!echo secret", nil)
	waitShell(t, a)
	if len(a.agent.Messages()) != 0 {
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
	// During a turn a message steers it; a command would run.
	a := treeApp(t)
	a.busy, a.runKind = true, "turn"
	a.cancel = func() {}
	for _, text := range []string{"!", "!!"} {
		a.submit(text, nil)
	}
	if len(a.pendingSteers) != 2 || a.shell != nil || len(shellBlocks(a)) != 0 {
		t.Fatalf("steers %q", a.pendingSteers)
	}
}

func TestShellExitCodeInContext(t *testing.T) {
	a := treeApp(t)
	a.submit("!exit 3", nil)
	waitShell(t, a)
	if got := contextText(a); !strings.Contains(got, "(no output)\n\nCommand exited with code 3") {
		t.Fatalf("context: %s", got)
	}
	if got := shellText(a); !strings.Contains(got, "exit 3") {
		t.Fatalf("block:\n%s", got)
	}
}

func TestShellDuringTurnWaitsForTheEnd(t *testing.T) {
	a := treeApp(t)
	a.busy, a.runKind = true, "turn"
	a.submit("!echo hello", nil)
	waitShell(t, a)
	a.ui.Do(func() {
		if len(a.agent.Messages()) != 0 || len(bashEntries(a)) != 0 {
			t.Errorf("added during the turn: %s", contextText(a))
		}
		if !strings.Contains(shellText(a), "after this turn") {
			t.Errorf("block:\n%s", shellText(a))
		}
		a.busy = false
		a.afterRun(nil)
	})
	if got := contextText(a); !strings.Contains(got, "Ran `echo hello`") {
		t.Fatalf("context after the turn: %q", got)
	}
	if len(bashEntries(a)) != 1 || strings.Contains(shellText(a), "after this turn") {
		t.Fatalf("entries %d\n%s", len(bashEntries(a)), shellText(a))
	}
}

func TestShellRefusedWhileOneRuns(t *testing.T) {
	a := treeApp(t)
	a.shell = &shellRun{cancel: func() {}}
	a.submit("!echo second", nil)
	if a.editor.Text() != "!echo second" {
		t.Fatalf("editor %q", a.editor.Text())
	}
	if len(shellBlocks(a)) != 0 || len(a.agent.Messages()) != 0 {
		t.Fatal("the second command ran")
	}
}

func TestShellCancel(t *testing.T) {
	a := treeApp(t)
	a.submit("!sleep 30", nil)
	if a.shell == nil {
		t.Fatal("not running")
	}
	if !a.onInput("\x1b") {
		t.Fatal("esc not handled")
	}
	waitShell(t, a)
	if got := contextText(a); !strings.Contains(got, "(command cancelled)") {
		t.Fatalf("context: %s", got)
	}
	if got := shellText(a); !strings.Contains(got, "canceled") {
		t.Fatalf("block:\n%s", got)
	}
}

func TestShellPersistsAndReplaysTheSame(t *testing.T) {
	a := treeApp(t)
	a.submit("!echo one; echo two", nil)
	waitShell(t, a)
	a.submit("!!exit 2", nil)
	waitShell(t, a)
	live := shellText(a)
	before := contextText(a)

	a.ui.Body.Clear()
	a.replay(session.Active(a.loadSession()))
	if got := shellText(a); got != live {
		t.Fatalf("replay differs\nlive:\n%s\nreplayed:\n%s", live, got)
	}
	a.agent.Restore(session.Active(a.loadSession()))
	if got := contextText(a); got != before || strings.Contains(got, "exit") {
		t.Fatalf("restored context %q, was %q", got, before)
	}
}

func TestShellBashModeInput(t *testing.T) {
	a := treeApp(t)
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
	a := treeApp(t)
	a.busy, a.runKind = true, "turn"
	a.editor.SetText("!echo hi")
	a.queueFromEditor()
	if len(a.queued) != 0 || len(shellBlocks(a)) == 0 {
		t.Fatalf("queued %d", len(a.queued))
	}
	waitShell(t, a)
}

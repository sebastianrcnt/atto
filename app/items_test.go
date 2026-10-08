package app

import (
	"context"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/provider/providertest"
	"github.com/sebastianrcnt/atto/tui"
)

// transcriptLines renders the conversation's blocks, without atto's own
// notices (which differ: "Worked for" live, "Resumed session" after).
func transcriptLines(a *App) string {
	var out []string
	for _, c := range a.ui.Body.Children {
		g, ok := c.(gap)
		if !ok {
			continue
		}
		switch g.Component.(type) {
		case *noticeBlock, *loadedBlock: // the Loaded block names where the model came from
			continue
		}
		for _, l := range g.Render(80) {
			out = append(out, strings.TrimRight(tui.StripEscapes(l), " "))
		}
	}
	return strings.Join(out, "\n")
}

// TestLiveBlocksMatchResume runs a turn against a scripted model, then
// resumes its session in another terminal: the transcript looks the same.
func TestLiveBlocksMatchResume(t *testing.T) {
	a, _ := liveApp(t,
		providertest.Reply{Text: "Let me look.", Command: "echo hi; exit 4", Description: "Say hi"},
		providertest.Reply{Text: "It exited with 4."})
	send(t, a, "why does it fail?")
	live := lines(a)
	for _, want := range []string{"why does it fail?", "• Let me look.", "✗ Say hi", "exit 4", "└ hi", "• It exited with 4."} {
		if !strings.Contains(live, want) {
			t.Fatalf("live transcript lacks %q:\n%s", want, live)
		}
	}
	b := reopen(t, a)
	if got := lines(b); got != live {
		t.Fatalf("resumed:\n%s\nlive:\n%s", got, live)
	}
}

// lines is transcriptLines under the UI lock.
func lines(a *App) string {
	var s string
	a.ui.Do(func() { s = transcriptLines(a) })
	return s
}

// reopen closes a's session and opens it in another terminal, with a
// runtime of its own: a cold resume.
func reopen(t *testing.T, a *App) *App {
	t.Helper()
	id := a.threadID
	if err := a.conn.c.Call(context.Background(), "thread/close", map[string]any{"threadId": id}, nil); err != nil {
		t.Fatal(err)
	}
	return startApp(t, a.cwd, Options{Session: id})
}

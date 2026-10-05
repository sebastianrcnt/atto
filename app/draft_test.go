package app

import (
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/agent"
)

// feed applies agent events as the UI goroutine would and returns the
// rendered transcript after each.
func feed(a *App, evs ...any) (lines []string) {
	for _, ev := range evs {
		a.ui.Do(func() {
			a.onEvent(ev)
			lines = append(lines, transcriptLines(a))
		})
	}
	return lines
}

func TestPendingToolBlockBecomesRunning(t *testing.T) {
	a := treeApp(t)
	got := feed(a,
		agent.ToolDraft{Index: 0},
		agent.ToolDraft{Index: 0, Args: agent.BashArgs{Description: "Write file", Command: "cat > a <<'EOF'\nhi"}},
		agent.ToolStart{ID: "c1", Index: 0, Args: agent.BashArgs{Description: "Write file", Command: "cat > a <<'EOF'\nhi\nEOF"}, Timeout: agent.DefaultBashTimeout},
		agent.ToolEnd{ID: "c1", Result: agent.BashResult{Output: "ok\n"}, Text: "ok"},
	)
	if !strings.Contains(got[0], "Preparing command · writing…") {
		t.Fatalf("pending block:\n%s", got[0])
	}
	if !strings.Contains(got[1], "● Write file · writing…") || !strings.Contains(got[1], "$ cat > a <<'EOF' …") {
		t.Fatalf("filled in:\n%s", got[1])
	}
	if strings.Contains(got[2], "writing") || !strings.Contains(got[2], " / 1m 00s") || !strings.Contains(got[2], "● Write file · ") {
		t.Fatalf("running:\n%s", got[2])
	}
	if !strings.Contains(got[3], "✓ Write file") || strings.Count(got[3], "Write file") != 1 {
		t.Fatalf("finished: one block per call:\n%s", got[3])
	}
}

func TestPendingToolBlockEndsWhenTurnIsInterrupted(t *testing.T) {
	a := treeApp(t)
	feed(a, agent.ToolDraft{Index: 0, Args: agent.BashArgs{Description: "Write file"}}, agent.ToolDraftEnd{Index: 0})
	var out string
	a.ui.Do(func() { out = transcriptLines(a) })
	if strings.Contains(out, "writing") || !strings.Contains(out, "✗ Write file") {
		t.Fatalf("an interrupted call is canceled:\n%s", out)
	}

	// A call the response ended on without a ToolDraftEnd is closed by End.
	b := treeApp(t)
	feed(b, agent.ToolDraft{Index: 0, Args: agent.BashArgs{Description: "Run tests"}})
	b.ui.Do(func() { b.tr().End(); out = transcriptLines(b) })
	if strings.Contains(out, "writing") || !strings.Contains(out, "✗ Run tests · canceled") {
		t.Fatalf("End leaves nothing pending:\n%s", out)
	}
}

package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

// call is a command for runCalls: what it is and how it ends.
type call struct {
	desc, cmd, out string
	exit           int
	err            string
	dur            time.Duration
}

var exploreCalls = []call{
	{desc: "List files", cmd: "ls", out: "a.go\nb.go", dur: 100 * time.Millisecond},
	{desc: "Read main.go", cmd: "cat main.go", out: "package main", dur: 200 * time.Millisecond},
	{desc: "Search for TODOs", cmd: "rg TODO", exit: 1, dur: 300 * time.Millisecond},
	{desc: "Check git status", cmd: "git status", out: "clean", dur: 400 * time.Millisecond},
	{desc: "Run the tests", cmd: "go test ./...", out: "ok\nok\nok\nok\nok\nok\nPASS", dur: 2 * time.Second},
}

// runCalls feeds the events of calls, run one after another, to the
// app's transcript.
func runCalls(a *App, prefix string, calls []call) {
	for i, c := range calls {
		id := fmt.Sprintf("%s%d", prefix, i)
		a.tr().Event(agent.ToolStart{ID: id, Args: agent.BashArgs{Description: c.desc, Command: c.cmd}, Timeout: time.Minute})
		if c.out != "" {
			a.tr().Event(agent.ToolOutput{ID: id, Chunk: c.out + "\n"})
		}
		res := agent.BashResult{ExitCode: c.exit, Duration: c.dur}
		if c.err != "" {
			res.Err = errors.New(c.err)
		}
		a.tr().Event(agent.ToolEnd{ID: id, Result: res})
	}
}

func runText(a *App, width int) string {
	return plainLines(a.ui.Body.Render(width))
}

func TestToolGroupCollapsed(t *testing.T) {
	a := testApp(t)
	a.tr().Event(transcript.Input{Text: "look around"})
	runCalls(a, "c", exploreCalls)
	a.tr().Event(agent.TextDelta{Text: "Done."})
	a.tr().Event(agent.StepEnd{})
	out := runText(a, 100)
	t.Logf("\n%s", out)

	want := "▸ 4 commands · 1.0s · 1 failed  List files, Read main.go, Search for TODOs, Check git status"
	if !strings.Contains(out, want) {
		t.Fatalf("summary line missing:\n%s", out)
	}
	for _, hidden := range []string{"$ ls", "$ cat main.go", "$ git status"} {
		if strings.Contains(out, hidden) {
			t.Errorf("%q shown in a collapsed group", hidden)
		}
	}
	// The failed call and the last one stay.
	for _, shown := range []string{"✗ Search for TODOs · exit 1", "$ rg TODO", "✓ Run the tests", "$ go test ./...", "Done."} {
		if !strings.Contains(out, shown) {
			t.Errorf("%q not shown", shown)
		}
	}
}

func TestToolGroupBreaksOnText(t *testing.T) {
	a := testApp(t)
	runCalls(a, "a", exploreCalls[:2])
	a.tr().Event(agent.TextDelta{Text: "Now the rest."})
	a.tr().Event(agent.StepEnd{})
	runCalls(a, "b", exploreCalls[3:4]) // one call: as it always was
	out := runText(a, 100)
	if !strings.Contains(out, "▸ 1 command · 0.1s  List files") || !strings.Contains(out, "$ cat main.go") {
		t.Fatalf("first run:\n%s", out)
	}
	if strings.Count(out, "▸") != 1 || !strings.Contains(out, "$ git status") {
		t.Fatalf("a lone call after the text grouped:\n%s", out)
	}
	// Another kind of item ends the run too.
	a.tr().Add(transcript.Item{Kind: transcript.Notice, Text: "note"})
	runCalls(a, "c", exploreCalls[:1])
	if out := runText(a, 100); strings.Count(out, "▸") != 1 {
		t.Fatalf("a call after a notice joined the run:\n%s", out)
	}
}

func TestToolGroupRunning(t *testing.T) {
	a := testApp(t)
	runCalls(a, "a", exploreCalls[:2])
	a.tr().Event(agent.ToolStart{ID: "x", Args: agent.BashArgs{Description: "Build", Command: "go build"}, Timeout: time.Minute})
	a.tr().Event(agent.ToolOutput{ID: "x", Chunk: "compiling\n"})
	out := runText(a, 100)
	t.Logf("\n%s", out)
	if !strings.Contains(out, "▸ 2 commands · 0.3s  List files, Read main.go") {
		t.Fatalf("summary so far:\n%s", out)
	}
	if !strings.Contains(out, "● Build · ") || !strings.Contains(out, "compiling") {
		t.Fatalf("the running call is not live:\n%s", out)
	}
	// A call the model is still writing shows too.
	a.tr().Event(agent.ToolEnd{ID: "x", Result: agent.BashResult{Duration: time.Second}})
	a.tr().Event(agent.ToolDraft{Index: 0, Args: agent.BashArgs{Description: "Look"}})
	if out := runText(a, 100); !strings.Contains(out, "▸ 3 commands · 1.3s  List files, Read main.go, Build") || !strings.Contains(out, "Look · writing") {
		t.Fatalf("pending call:\n%s", out)
	}
}

// summaryLine finds the group's summary line in the body.
func summaryLine(t *testing.T, a *App, width int) int {
	t.Helper()
	for i, l := range a.ui.Body.Render(width) {
		if p := tui.StripEscapes(l); strings.HasPrefix(p, "▸ ") || strings.HasPrefix(p, "▾ ") {
			return i
		}
	}
	t.Fatal("no summary line")
	return -1
}

func TestToolGroupExpand(t *testing.T) {
	a := testApp(t)
	runCalls(a, "c", exploreCalls)
	const width = 100
	line := summaryLine(t, a, width)
	if !a.ui.Body.Click(line) {
		t.Fatal("click on the summary did nothing")
	}
	lines := a.ui.Body.Render(width)
	out := plainLines(lines)
	t.Logf("\n%s", out)
	if !strings.HasPrefix(tui.StripEscapes(lines[line]), "▾ 4 commands") {
		t.Fatalf("header %q", lines[line])
	}
	for _, c := range exploreCalls {
		if strings.Count(out, "$ "+c.cmd) != 1 {
			t.Errorf("%q not shown once", c.cmd)
		}
	}
	// The region: every line after the header on the background, filled
	// to the width, the color reset at its end.
	bg := fmt.Sprintf("\x1b[48;5;%dm", groupBG())
	region := lines[line+1:]
	if len(region) < 10 {
		t.Fatalf("region %d lines", len(region))
	}
	for i, l := range region {
		if !strings.HasPrefix(l, bg) || !strings.HasSuffix(l, "\x1b[49m") {
			t.Fatalf("line %d not on the background: %q", i, l)
		}
		if w := tui.VisibleWidth(l); w != width {
			t.Fatalf("line %d is %d wide: %q", i, w, l)
		}
	}
	if tui.StripEscapes(region[0]) != strings.Repeat(" ", width) {
		t.Fatalf("no padding row above: %q", region[0])
	}

	// A call in the region expands on its own (the last one has hidden
	// output); its header is where it was rendered.
	head := -1
	for i, l := range region {
		if strings.Contains(tui.StripEscapes(l), "✓ Run the tests") {
			head = line + 1 + i
		}
	}
	if !a.ui.Body.Click(head) || !strings.Contains(runText(a, width), "… +3 lines") {
		t.Fatal("a call in the region does not open")
	}
	for i, l := range a.ui.Body.Render(width) {
		if strings.Contains(tui.StripEscapes(l), "… +3 lines") {
			if !a.ui.Body.Click(i) || !strings.Contains(runText(a, width), "− Show less") {
				t.Fatal("a call in the region does not expand")
			}
		}
	}
	if !a.ui.Body.Click(line) {
		t.Fatal("click on the expanded header did nothing")
	}
	if got := plainLines(a.ui.Body.Render(width)); strings.Count(got, "$ ls") != 0 {
		t.Fatalf("still expanded:\n%s", got)
	}

	// ctrl+t expands every group, and again collapses them.
	if !a.onInput("\x14") || !strings.Contains(runText(a, width), "$ ls") {
		t.Fatal("ctrl+t did not expand the group")
	}
	a.onInput("\x14")
	if strings.Contains(runText(a, width), "$ ls") {
		t.Fatal("ctrl+t again did not collapse it")
	}
}

func TestToolGroupSettingOff(t *testing.T) {
	a := testApp(t)
	a.noToolGroups = true
	runCalls(a, "c", exploreCalls)
	out := runText(a, 100)
	if strings.Contains(out, "▸") {
		t.Fatalf("grouped with toolGroups false:\n%s", out)
	}
	for _, c := range exploreCalls {
		if !strings.Contains(out, "$ "+c.cmd) {
			t.Errorf("%q not shown", c.cmd)
		}
	}
	// As blocks have always looked: each after a blank line.
	var want []string
	for _, c := range exploreCalls {
		b := &toolBlock{d: &a.details, args: agent.BashArgs{Description: c.desc, Command: c.cmd}, done: true,
			res: agent.BashResult{ExitCode: c.exit, Duration: c.dur}}
		if c.out != "" {
			b.append(c.out)
		}
		want = append(want, gap{b}.Render(100)...)
	}
	r := a.ui.Body.Children[len(a.ui.Body.Children)-1]
	if got := plainLines(r.Render(100)); got != plainLines(want) {
		t.Fatalf("got:\n%s\nwant:\n%s", got, plainLines(want))
	}
	a.noToolGroups = false
	if !strings.Contains(runText(a, 100), "▸") {
		t.Fatal("turning the setting back on did not group")
	}
}

func TestToolGroupReasoning(t *testing.T) {
	a := testApp(t)
	a.tr().Event(agent.ReasoningDelta{Text: "first I look"})
	runCalls(a, "a", exploreCalls[:1])
	a.tr().Event(agent.ReasoningDelta{Text: "between the calls"})
	runCalls(a, "b", exploreCalls[1:2])
	a.tr().Event(agent.ReasoningDelta{Text: "after the calls"})
	a.tr().Event(agent.TextDelta{Text: "Answer"})
	a.tr().Event(agent.StepEnd{})
	// The header, the reasoning before the first call, the run, the text.
	if n := len(a.ui.Body.Children); n != 4 {
		t.Fatalf("%d children", n)
	}
	r := a.ui.Body.Children[2].(gap).Component.(*toolRun)
	if len(r.members) != 4 {
		t.Fatalf("run members %d, want 2 calls and 2 reasoning", len(r.members))
	}
	out := runText(a, 100)
	t.Logf("\n%s", out)
	if n := strings.Count(out, "∴ Thought"); n != 2 { // before the run, and the trailing one
		t.Fatalf("thinking lines %d:\n%s", n, out)
	}
	if !strings.Contains(out, "▸ 1 command · 0.1s  List files") {
		t.Fatalf("no group:\n%s", out)
	}
}

// TestToolGroupThoughtsBetweenCalls: call, thought, call, thought, call is
// one group; the thoughts show only when it is expanded.
func TestToolGroupThoughtsBetweenCalls(t *testing.T) {
	a := testApp(t)
	for i, c := range exploreCalls[:3] {
		if i > 0 {
			a.tr().Event(agent.ReasoningDelta{Text: fmt.Sprintf("thought %d", i)})
		}
		runCalls(a, fmt.Sprint(i), []call{c})
	}
	if n := len(a.ui.Body.Children); n != 2 { // the header and the run
		t.Fatalf("%d children", n)
	}
	out := runText(a, 100)
	if strings.Count(out, "▸") != 1 || strings.Contains(out, "Thought") || !strings.Contains(out, "▸ 2 commands · 0.3s  List files, Read main.go") {
		t.Fatalf("collapsed:\n%s", out)
	}
	a.ui.Body.Click(summaryLine(t, a, 100))
	out = runText(a, 100)
	if strings.Count(out, "∴ Thought") != 2 {
		t.Fatalf("expanded:\n%s", out)
	}
	// A thought in the region expands on its own.
	for i, l := range a.ui.Body.Render(100) {
		if strings.Contains(tui.StripEscapes(l), "∴ Thought") {
			if !a.ui.Body.Click(i) || !strings.Contains(runText(a, 100), "thought 1") {
				t.Fatal("thought did not expand")
			}
			break
		}
	}
}

// TestToolGroupWidths renders a group at several widths, with wide
// characters: no line overflows, the summary leads with how many calls it
// holds, and the region fills the width.
func TestToolGroupWidths(t *testing.T) {
	calls := append([]call{
		{desc: "파일 목록 보기", cmd: "ls 디렉터리", out: "가나다라마바사", dur: time.Second},
		{desc: "読み込み main.go", cmd: "cat main.go", out: "こんにちは世界", dur: time.Second},
	}, exploreCalls...)
	for _, width := range []int{20, 41, 60, 100, 160} {
		a := testApp(t)
		runCalls(a, "c", calls)
		check := func(lines []string) {
			for i, l := range lines {
				if w := tui.VisibleWidth(l); w > width {
					t.Fatalf("width %d: line %d is %d wide: %q", width, i, w, tui.StripEscapes(l))
				}
			}
		}
		lines := a.ui.Body.Render(width)
		check(lines)
		head := tui.StripEscapes(lines[summaryLine(t, a, width)])
		if width >= 41 && !strings.Contains(head, fmt.Sprintf("%d commands", len(calls)-1)) {
			t.Errorf("width %d: %q does not say how many", width, head)
		}
		a.ui.Body.Click(summaryLine(t, a, width))
		lines = a.ui.Body.Render(width)
		check(lines)
		for _, l := range lines[summaryLine(t, a, width)+1:] {
			if tui.VisibleWidth(l) != width {
				t.Fatalf("width %d: region line not filled: %q", width, l)
			}
		}
		if width == 100 {
			t.Logf("\n%s", plainLines(lines))
		}
	}
}

func TestRunSummaryCount(t *testing.T) {
	var calls []*toolBlock
	for _, d := range []string{"Read main.go", "Search for TODOs", "List src", "Check git status", "Read a", "Read b", "Read c"} {
		calls = append(calls, &toolBlock{args: agent.BashArgs{Description: d}, done: true})
	}
	got := tui.StripEscapes(runSummary(calls, runHeadKey{n: len(calls), dur: 12 * time.Second}, 80))
	if want := "▸ 7 commands · 12.0s  Read main.go, Search for TODOs, List src, Check git statu…"; got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	got = tui.StripEscapes(runSummary(calls, runHeadKey{n: len(calls), dur: time.Second, failed: 2}, 200))
	if !strings.HasPrefix(got, "▸ 7 commands · 1.0s · 2 failed  Read main.go") || !strings.HasSuffix(got, "Read c") {
		t.Fatalf("got %q", got)
	}
	if got := tui.StripEscapes(runSummary(calls[:1], runHeadKey{n: 1}, 80)); !strings.HasPrefix(got, "▸ 1 command · ") {
		t.Fatalf("singular: %q", got)
	}
}

func TestOnBGKeepsBackground(t *testing.T) {
	bg := "\x1b[48;5;236m"
	line := tui.Truncate(tui.Dim("a long line that gets cut"), 10, "…") + tui.BG(1, "x")
	got := onBG(236, line, 20)
	if !strings.HasPrefix(got, bg) || !strings.HasSuffix(got, "\x1b[49m") || tui.VisibleWidth(got) != 20 {
		t.Fatalf("%q", got)
	}
	// After every reset the background is set again.
	for _, reset := range []string{"\x1b[0m", "\x1b[49m"} {
		rest := got[:len(got)-len("\x1b[49m")]
		for i := strings.Index(rest, reset); i >= 0; i = strings.Index(rest, reset) {
			rest = rest[i+len(reset):]
			if reset == "\x1b[0m" && !strings.HasPrefix(rest, bg) {
				t.Fatalf("no background after a reset: %q", got)
			}
		}
	}
	if strings.Count(got, "\x1b[49m") != 1 {
		t.Fatalf("a background reset left inside: %q", got)
	}
}

func TestLightBackground(t *testing.T) {
	for v, want := range map[string]bool{"": false, "15;0": false, "0;15": true, "0;7": true, "7;default;0": false,
		"0;default;15": true, "12;8": false, "x": false} {
		if got := lightBackground(v); got != want {
			t.Errorf("%q: %v", v, got)
		}
	}
}

// TestToolGroupCache: a settled group gives the same lines without
// rendering again.
func TestToolGroupCache(t *testing.T) {
	a := testApp(t)
	runCalls(a, "c", exploreCalls)
	r := a.ui.Body.Children[1].(gap).Component.(*toolRun)
	first := r.Render(100)
	if again := r.Render(100); &again[0] != &first[0] {
		t.Fatal("a settled group rendered again")
	}
	r.toggle()
	exp := r.Render(100)
	if again := r.Render(100); &again[0] != &exp[0] {
		t.Fatal("an expanded group rendered again")
	}
	r.toggle()
	if got := r.Render(100); plainLines(got) != plainLines(first) {
		t.Fatal("collapsed again differs")
	}
}

// TestToolGroupResume: a session saved with a run of calls looks the same
// resumed as it did live.
func TestToolGroupResume(t *testing.T) {
	a := treeApp(t)
	a.tr().Event(transcript.Input{Text: "look around"})
	a.sess.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "look around"}})
	for i, c := range exploreCalls {
		id := fmt.Sprintf("call%d", i)
		// Reasoning between the calls, hidden in the group.
		thought := ""
		if i > 0 {
			thought = "next"
			a.tr().Event(agent.ReasoningDelta{Text: thought})
		}
		args, _ := json.Marshal(agent.BashArgs{Description: c.desc, Command: c.cmd})
		a.sess.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", ReasoningContent: thought,
			ToolCalls: []provider.ToolCall{{ID: id, Type: "function", Function: provider.FunctionCall{Name: "bash", Arguments: string(args)}}}}})
		runCalls(a, id, []call{c})
		content := c.out
		if c.exit != 0 {
			content = fmt.Sprintf("[exit code %d]", c.exit) // the failing call has no output
		}
		a.sess.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "tool", ToolCallID: id, Content: content},
			Tool: &session.ToolMeta{Description: c.desc, ExitCode: c.exit, DurationMs: c.dur.Milliseconds()}})
	}
	// From the prompt on: the header and the loaded block are not part of
	// the transcript.
	transcriptText := func() string {
		out := runText(a, 100)
		return out[strings.Index(out, "› look around"):]
	}
	live := transcriptText()
	a.ui.Body.Clear()
	a.replay(session.Active(a.loadSession()))
	if got := transcriptText(); got != live {
		t.Fatalf("resumed differs\nlive:\n%s\nresumed:\n%s", live, got)
	}
	t.Logf("\n%s", live)
}

// TestActivityWhileCallRuns: the line above the editor does not repeat
// the running call's description, which its block shows.
func TestActivityWhileCallRuns(t *testing.T) {
	a := testApp(t)
	a.busy, a.runStart, a.activity = true, time.Now(), "Thinking"
	a.onEvent(agent.ToolDraft{Index: 0, Args: agent.BashArgs{Description: "Wait for the full regression"}})
	a.onEvent(agent.ToolStart{ID: "x", Args: agent.BashArgs{Description: "Wait for the full regression", Command: "sleep 1"}, Timeout: time.Minute})
	got := plainLines(a.renderActivity(100))
	if strings.Contains(got, "regression") || !strings.Contains(got, "Working…") || !strings.Contains(got, "esc to interrupt") {
		t.Fatalf("activity %q", got)
	}
	a.onEvent(agent.ToolEnd{ID: "x"})
	if a.activity != "Thinking" {
		t.Fatalf("after the call: %q", a.activity)
	}
}

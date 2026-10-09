package app

import (
	"fmt"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/tui"
	"github.com/sebastianrcnt/atto/ui"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func builtinGolden(t *testing.T, name string, width int, lines []string) {
	t.Helper()
	got := plainLines(lines) + "\n"
	path := fmt.Sprintf("testdata/ui-%s-%d.txt", name, width)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		t.Fatalf("%s mismatch:\n%s", path, got)
	}
	for _, l := range lines {
		if tui.VisibleWidth(l) > width {
			t.Fatalf("%s overflow", name)
		}
	}
}

func TestPortableStatusGoldens(t *testing.T) {
	a := statusApp(t, nil)
	oldRSS := rssBytes.Load()
	rssBytes.Store(0)
	defer rssBytes.Store(oldRSS)
	builtinGolden(t, "status-before", 80, a.renderMainStatus(80))
	applyStatusFixture(a)
	for _, w := range []int{40, 80, 120, 160} {
		builtinGolden(t, "status", w, a.renderStatus(w))
	}
}

func TestPortableGoalPane(t *testing.T) {
	a := goalApp(t)
	goalCommand(a, "ship it")
	goalCommand(a, "")
	within(t, a, "goal pane", func() bool { return len(a.liveUI(ui.Pane)) == 1 })
	a.ui.Do(func() {
		before := &contextBlock{lines: goalSummaryLines(a.theGoal(), false)}
		builtinGolden(t, "goal-before", 80, before.Render(80))
		for _, w := range []int{40, 80, 120, 160} {
			builtinGolden(t, "goal", w, a.renderPortable(w))
		}
		e := a.elements[ui.Match{Site: ui.Pane, ID: "atto/goal"}]
		e.SetFocused(true)
		e.HandleInput("p")
	})
	settle(a)
	within(t, a, "pane paused", func() bool { return a.theGoal().Status == goal.Paused })
	a.ui.Do(func() { a.elements[ui.Match{Site: ui.Pane, ID: "atto/goal"}].HandleInput("c") })
	settle(a)
	within(t, a, "pane cleared", func() bool { return a.theGoal() == nil })
}

func TestPortableJobsPaneStop(t *testing.T) {
	a, _ := liveApp(t)
	control := filepath.Join(t.TempDir(), "stop")
	j, err := jobs.StartWorkerTurn(a.threadID, a.cwd, "sleep 60", os.Getpid(), jobs.Control{File: control, Content: "stop"}, true)
	if err != nil {
		t.Fatal(err)
	}
	typeLine(a, "/jobs")
	within(t, a, "jobs pane", func() bool { return a.elements[ui.Match{Site: ui.Pane, ID: "atto/jobs"}] != nil })
	a.ui.Do(func() {
		e := a.elements[ui.Match{Site: ui.Pane, ID: "atto/jobs"}]
		for _, w := range []int{40, 80, 120, 160} {
			builtinGolden(t, "jobs", w, e.Render(w))
		}
		before := &contextBlock{lines: []string{tui.Bold("Background jobs") + tui.Dim("  · /stop stops all · output: atto job output <id>"), fmt.Sprintf("%-4d %-10s %-12s %-8s %s", j.ID, "agent turn", "running", "0s", "sleep 60")}}
		builtinGolden(t, "jobs-before", 80, before.Render(80))
		e.SetFocused(true)
		e.HandleInput("\r")
	})
	settle(a)
	data, err := os.ReadFile(control)
	if err != nil || string(data) != "stop" {
		t.Fatalf("stop service didn't run: %q %v", data, err)
	}
}

func TestPortableDialogGoldens(t *testing.T) {
	for _, p := range []server.Prompt{{Kind: server.PromptSelect, Title: "Pick one", Subtitle: "(demo)", Options: []server.PromptOption{{Label: "red"}, {Label: "green"}}}, {Kind: server.PromptSelect, Title: "Sure?", Confirm: true, Options: []server.PromptOption{{Label: "Yes"}, {Label: "No"}}}, {Kind: server.PromptInput, Title: "Name?", Text: "Ann"}} {
		name := "select"
		if p.Confirm {
			name = "confirm"
		}
		if p.Kind == server.PromptInput {
			name = "input"
		}
		n := server.DialogTree(p)
		e := &tui.Elements{}
		if err := e.SetTree(ui.Dialog, "atto/test", 1, &n); err != nil {
			t.Fatal(err)
		}
		e.SetFocused(true)
		for _, w := range []int{40, 80, 120, 160} {
			builtinGolden(t, name, w, e.Render(w))
		}
		var before []string
		if p.Kind == server.PromptInput {
			before = (&labelInput{title: p.Title, hint: "enter submit  esc cancel", text: p.Text}).Render(80)
		} else {
			l := &tui.SelectList{Title: p.Title}
			if p.Subtitle != "" {
				l.Title += tui.Dim("  " + p.Subtitle)
			}
			for _, o := range p.Options {
				l.Items = append(l.Items, tui.SelectItem{Label: o.Label, Detail: o.Description})
			}
			before = l.Render(80)
		}
		builtinGolden(t, name+"-before", 80, before)
	}
}

func TestUIEngineReferenceNativeAndOriginal(t *testing.T) {
	a := testApp(t)
	native := server.Item{ID: "answer", Type: server.ItemAgent, Status: "completed", Text: "original answer"}
	a.wireStarted(native)
	a.wireCompleted(native)
	reference := ui.Node{Type: "engine", Props: map[string]any{"site": string(ui.AssistantMessage), "id": native.ID, "overrides": map[string]any{"text": "display override"}}}
	tree := ui.Box(ui.BoxProps{}, reference, ui.Text(ui.TextProps{Text: "review footer"}))
	native.UIDisplay = &server.UIDisplay{Rev: 1, Tree: &tree}
	a.applyUIItem(native)
	got := bodyText(a)
	if !strings.Contains(got, "display override") || !strings.Contains(got, "review footer") {
		t.Fatal(got)
	}
	b := a.itemUI[native.ID]
	b.Click(b.toggleLine)
	got = bodyText(a)
	if !strings.Contains(got, "original answer") || strings.Contains(got, "review footer") {
		t.Fatal(got)
	}
}

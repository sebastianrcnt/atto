package server

import (
	"context"
	"fmt"

	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/ui"
)

func goalPaneTree(info *GoalInfo) ui.Node {
	if info == nil || info.Goal == nil {
		return ui.Text(ui.TextProps{Text: "No goal is currently set."})
	}
	g := info.Goal
	hint := "Commands: /goal edit, /goal clear"
	if g.Status == goal.Active {
		hint = "Commands: /goal edit, /goal pause, /goal clear"
	} else if g.Status != goal.Complete {
		hint = "Commands: /goal edit, /goal resume, /goal clear"
	}
	action, label, hotkey := "resume", "Resume", "r"
	if g.Status == goal.Active && !info.Held {
		action, label, hotkey = "pause", "Pause", "p"
	}
	children := []ui.Node{ui.Text(ui.TextProps{Color: ui.Muted, Text: "Status: " + g.Status.Label()}), ui.Text(ui.TextProps{Text: "Objective: " + g.Objective}), ui.Text(ui.TextProps{Color: ui.Muted, Text: fmt.Sprintf("Time used: %s · Tokens used: %s", info.Elapsed, info.Tokens)})}
	if g.Note != "" {
		children = append(children, ui.Text(ui.TextProps{Color: ui.Warning, Text: g.Note}))
	}
	if g.Status == goal.Active && !info.Held {
		children = append(children, ui.Progress(ui.ProgressProps{Label: "Working on goal"}))
	}
	children = append(children, ui.Box(ui.BoxProps{FlexDirection: "row", Gap: 1}, ui.Button(ui.ButtonProps{Key: "edit", Label: "Edit", Hotkey: "e"}), ui.Button(ui.ButtonProps{Key: action, Disabled: g.Status == goal.Complete, Label: label, Hotkey: hotkey}), ui.Button(ui.ButtonProps{Key: "clear", Label: "Clear", Hotkey: "c"})), ui.Text(ui.TextProps{Color: ui.Muted, Text: hint, Wrap: "truncate"}))
	return ui.Box(ui.BoxProps{}, children...)
}
func (t *thread) initUIGoal() {
	m := ui.Match{Site: ui.Pane, ID: "atto/goal"}
	r := t.uiRegistry()
	r.Render("atto", m, func(ui.Event, ui.Next) (*ui.Node, error) {
		n := goalPaneTree(t.goalInfo())
		t.bindUIGoal(m)
		return &n, nil
	})
}
func (t *thread) bindUIGoal(m ui.Match) {
	r := t.uiRegistry()
	for _, key := range []string{"edit", "pause", "resume", "clear"} {
		r.Bind("atto", m, key, ui.Press, func(ctx context.Context, a ui.Action) error {
			switch a.Key {
			case "pause":
				t.pauseGoal()
			case "resume":
				t.resumeGoalCmd()
			case "clear":
				t.clearGoal()
			case "edit":
				t.editGoalDialog()
			}
			return nil
		})
	}
}
func (t *thread) openGoalPane(client string) {
	tree := goalPaneTree(t.goalInfo())
	_ = t.uiRegistry().OpenDefault("atto", ui.OpenOptions{Site: ui.Pane, ID: "atto/goal", Title: "Goal", FocusClientID: client}, nil, &tree)
}
func (t *thread) editGoalDialog() {
	g := t.goal.Goal
	if g == nil {
		t.errorNotice(errNoGoal)
		return
	}
	t.ask(&openPrompt{origin: "goal", wire: Prompt{Kind: PromptInput, Title: "Edit goal", Text: g.Objective}, submit: func(text string) {
		if err := t.setObjective(text); err != nil {
			t.errorNotice(err)
		}
	}, cancel: func() {}})
}

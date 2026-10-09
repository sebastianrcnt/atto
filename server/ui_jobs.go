package server

import (
	"context"
	"fmt"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/ui"
	"strconv"
	"time"
)

func jobsPaneTree(list []Job) ui.Node {
	if len(list) == 0 {
		return ui.Text(ui.TextProps{Text: "No background jobs. The agent starts them with `atto job start -- <command>`."})
	}
	var rows []ui.Row
	var actions []ui.Node
	for _, j := range list[:min(len(list), 80)] {
		status := j.Status
		if j.ExitCode != nil {
			status += fmt.Sprintf(" (%d)", *j.ExitCode)
		}
		rows = append(rows, ui.Row{Key: strconv.Itoa(j.ID), Cells: []string{strconv.Itoa(j.ID), j.Kind, status, (time.Duration(j.RuntimeMs) * time.Millisecond).Round(time.Second).String(), ui.CleanText(j.Label)}})
		if j.Status == "running" || j.Status == "starting" {
			actions = append(actions, ui.Button(ui.ButtonProps{Key: fmt.Sprintf("stop-%d", j.ID), Label: fmt.Sprintf("Stop job %d", j.ID)}))
		}
	}
	children := []ui.Node{ui.List(ui.ListProps{Mode: "table", Columns: []ui.Column{{Label: "Job", Width: 4}, {Label: "Kind"}, {Label: "State"}, {Label: "Time"}, {Label: "Command"}}, Rows: rows})}
	children = append(children, actions...)
	if len(list) > 80 {
		children = append(children, ui.Text(ui.TextProps{Color: ui.Muted, Text: fmt.Sprintf("… %d more jobs; atto job list for all", len(list)-80)}))
	}
	children = append(children, ui.Text(ui.TextProps{Color: ui.Muted, Text: "Output: atto job output <id> · /stop stops all", Wrap: "truncate"}))
	return ui.Box(ui.BoxProps{}, children...)
}
func (t *thread) uiJobs() []Job {
	var out []Job
	for _, j := range jobs.List(t.id) {
		out = append(out, wireJob(j))
	}
	return out
}
func (t *thread) initUIJobs() {
	m := ui.Match{Site: ui.Pane, ID: "atto/jobs"}
	r := t.uiRegistry()
	r.Render("atto", m, func(ui.Event, ui.Next) (*ui.Node, error) {
		list := t.uiJobs()
		for _, j := range list {
			if j.Status != "running" && j.Status != "starting" {
				continue
			}
			job := j.ID
			r.Bind("atto", m, fmt.Sprintf("stop-%d", job), ui.Press, func(context.Context, ui.Action) error {
				_, err := background("job/stop", t.id, threadParams{Job: job})
				r.Invalidate(m)
				return err
			})
		}
		n := jobsPaneTree(list)
		return &n, nil
	})
}
func (t *thread) openJobsPane(client string) {
	n := jobsPaneTree(t.uiJobs())
	_ = t.uiRegistry().OpenDefault("atto", ui.OpenOptions{Site: ui.Pane, ID: "atto/jobs", Title: "Background jobs", FocusClientID: client}, nil, &n)
}

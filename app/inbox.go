package app

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/tui"
)

// The inbox (job exits, timers, monitors) is the runtime's: it delivers
// events to the model and says so (the "event" notification, shown as an
// eventBlock). /jobs and /timers show what it reports.

// eventBlock shows an [atto event] (job exit, timer, monitor) in the
// transcript; the model receives the full text.
type eventBlock struct {
	title string
	cache tui.RenderCache[string]
}

func (e *eventBlock) Render(width int) []string {
	return e.cache.Render(width, e.title, func() []string {
		return []string{tui.Truncate("  "+tui.FG(5, e.title), width, "…")}
	})
}

func (a *App) cmdJobs(string) {
	a.rpc("job/list", nil, func(raw json.RawMessage, err error) {
		if err != nil {
			a.errorNotice(err)
			return
		}
		var r struct {
			Jobs []server.Job `json:"jobs"`
		}
		_ = json.Unmarshal(raw, &r)
		if len(r.Jobs) == 0 {
			a.notice("No background jobs. The agent starts them with `atto job start -- <command>`.")
			return
		}
		var lines []string
		for _, j := range r.Jobs {
			st := j.Status
			if j.ExitCode != nil {
				st += fmt.Sprintf(" (%d)", *j.ExitCode)
			}
			runtime := (time.Duration(j.RuntimeMs) * time.Millisecond).Round(time.Second)
			lines = append(lines, fmt.Sprintf("%-4d %-10s %-12s %-8s %s", j.ID, j.Kind, st, runtime, j.Label))
		}
		a.add(&contextBlock{lines: append([]string{tui.Bold("Background jobs") + tui.Dim("  · /stop stops all · output: atto job output <id>")}, lines...)})
	})
}

func (a *App) cmdTimers(string) {
	a.rpc("timer/list", nil, func(raw json.RawMessage, err error) {
		if err != nil {
			a.errorNotice(err)
			return
		}
		var r struct {
			Timers []server.Timer `json:"timers"`
		}
		_ = json.Unmarshal(raw, &r)
		if len(r.Timers) == 0 {
			a.notice("No timers. Set one with /timer 10m <message>.")
			return
		}
		var lines []string
		for _, t := range r.Timers {
			due := time.UnixMilli(t.Due)
			sched := ""
			if t.Schedule != "" {
				sched = " [" + t.Schedule + "]"
			}
			lines = append(lines, fmt.Sprintf("%s  %s (in %s)%s  %s", t.ID, due.Format("15:04"), time.Until(due).Round(time.Second), sched, t.Message))
		}
		a.add(&contextBlock{lines: append([]string{tui.Bold("Timers") + tui.Dim("  · cancel: /timer cancel <id>")}, lines...)})
	})
}

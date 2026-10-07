package server

import (
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/subagent"
)

// What runs beside a thread's turns, for the web client's panels: the
// session's background jobs (atto job, as /jobs lists them) and its
// subagents (atto agent). Both live in files under ~/.atto keyed by the
// session ID, so a live session reads them the same way.

// clientSettings is what of settings.json clients follow (initialize).
func clientSettings() map[string]any {
	s, _ := config.LoadSettings() // unreadable: the defaults
	return map[string]any{"toolGroups": s.ToolGroups == nil || *s.ToolGroups}
}

// background serves job/* and subagent/* for session sid.
func background(method, sid string, p threadParams) (any, error) {
	switch method {
	case "job/list":
		out := []Job{}
		for _, j := range jobs.List(sid) {
			out = append(out, wireJob(j))
		}
		return map[string]any{"jobs": out}, nil
	case "job/output":
		if _, err := jobs.Get(sid, p.Job); err != nil {
			return nil, invalid("%v", err)
		}
		n := p.Lines
		if n <= 0 {
			n = 200
		}
		out, err := jobs.Tail(sid, p.Job, min(n, 2000))
		if err != nil {
			return nil, err
		}
		return map[string]any{"output": out}, nil
	case "job/stop":
		j, err := jobs.Get(sid, p.Job)
		if err != nil {
			return nil, invalid("%v", err)
		}
		if !j.Active() {
			return nil, invalid("job %d is not running (%s)", j.ID, j.Status)
		}
		if j, err = jobs.Kill(sid, p.Job); err != nil {
			return nil, err
		}
		return map[string]any{"job": wireJob(j)}, nil
	case "subagent/list":
		out := []Subagent{}
		for _, st := range subagent.List(sid) {
			out = append(out, wireSubagent(st))
		}
		return map[string]any{"subagents": out}, nil
	case "subagent/read":
		st, err := subagent.Load(sid, p.Name)
		if err != nil {
			return nil, invalid("%v", err)
		}
		items := []Item{}
		if path, err := session.Find(st.Session); err == nil {
			if _, entries, err := session.Load(path); err == nil {
				items = ItemsFromEntries(st.Session, session.Active(entries))
			}
		}
		// Its report is its last message, as atto agent report prints it.
		msg := ""
		for i := len(items) - 1; i >= 0 && msg == ""; i-- {
			if items[i].Type == ItemAgent {
				msg = items[i].Text
			}
		}
		return map[string]any{"subagent": wireSubagent(st), "message": msg, "items": items}, nil
	}
	return nil, &rpcError{Code: codeMethodNotFound, Message: "unknown method " + method}
}

func wireJob(j jobs.Job) Job {
	return Job{
		ID: j.ID, Label: j.Label(), Kind: j.KindLabel(), Command: j.Command, Status: string(j.Status),
		ExitCode: j.ExitCode, Error: j.Error, Started: j.Started.UnixMilli(), RuntimeMs: j.Runtime().Milliseconds(),
	}
}

func wireSubagent(st subagent.State) Subagent {
	t := st.Latest()
	return Subagent{
		Name: st.Name, Preset: st.Preset, Model: st.Model, Effort: st.Effort, ThreadID: st.Session,
		Task: st.Task, Prompt: st.Prompt, Turn: t.N, Status: string(t.Status),
		DurationMs: t.Duration().Milliseconds(), Error: t.Error,
		InputTokens: t.PromptTokens, CachedTokens: t.CachedTokens, OutputTokens: t.OutputTokens, Cost: t.Cost,
		Created: st.Created.UnixMilli(),
	}
}

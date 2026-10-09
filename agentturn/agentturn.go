// Package agentturn is what an agent's turn does around the run itself, the
// same whether a job process (atto _agent-turn) or the agent session's worker
// runs it: prepare the next turn in the agent's record, and on its end
// record how it went, tell the parent and start a successor when waking work
// arrived as it ended.
package agentturn

import (
	"fmt"
	"time"

	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

// FinalAnswerMax caps the answer a turn's end delivers; the rest is in
// atto agent report.
const FinalAnswerMax = 8000

// Prepare starts the next turn of the agent in its record: the turn counter,
// the prompt and the owner of the job that will run it (the parent for a
// child, the agent itself for a root). The caller holds the tree
// (agentstate.StartWork) and sets the job, which it makes, when it has it.
func Prepare(st agentstate.State, text string) (agentstate.State, error) {
	cur, err := agentstate.Load(st.Session)
	if err != nil {
		return st, err
	}
	cur.Turns++
	cur.Prompt, cur.Job = text, 0
	cur.JobOwner = cur.Parent
	if cur.IsRoot() {
		cur.JobOwner = cur.Session
	}
	if err := agentstate.Save(cur); err != nil {
		return st, err
	}
	return cur, nil
}

// Result is how a turn's run went.
type Result struct {
	Prompt, Cached, Output int
	Cost                   float64
	Steps                  int
	// Error is the run's own error, "" if it had none.
	Error string
	// Stopped: a user interrupt ended the turn.
	Stopped bool
}

// Finish records how turn t of the agent st went: the answer first (a wait
// that sees the turn over takes it from the inbox, so it is not delivered
// twice), pushed only to a recorded parent; then the turn. A stopped turn
// ends there. Otherwise, tasks accepted after the final poll need a
// successor, not an idle inbox: start makes it.
func Finish(st agentstate.State, t agentstate.Turn, r Result, start func(*agentstate.State, string) error) error {
	t.Ended = time.Now()
	t.PromptTokens, t.CachedTokens, t.OutputTokens = r.Prompt, r.Cached, r.Output
	t.Cost, t.Steps = r.Cost, r.Steps
	t.Status = agentstate.Done
	switch {
	case r.Stopped:
		t.Status = agentstate.Stopped
	case r.Error != "":
		t.Status, t.Error = agentstate.Failed, r.Error
	}
	releaseTurn, err := agentstate.LockTurn(st.Session)
	if err != nil {
		return err
	}
	defer releaseTurn()
	if st.Parent != "" {
		if err := events.Push(st.Parent, Event(st, t)); err != nil {
			return err
		}
	}
	if err := agentstate.SaveTurn(st.Session, t); err != nil {
		return err
	}
	if t.Status == agentstate.Stopped {
		return nil
	}
	_, evs := events.SplitReload(core.Poll(st.Session))
	if !events.Wakes(evs) {
		events.Requeue(st.Session, evs)
		return nil
	}
	if err := start(&st, events.Format(evs)); err != nil {
		events.Requeue(st.Session, evs)
		return err
	}
	return nil
}

// Event tells the parent session that an agent's turn ended, with its final
// answer, as codex delivers FINAL_ANSWER.
func Event(st agentstate.State, t agentstate.Turn) events.Event {
	from, to := st.Path, agentstate.PathOf(st.Parent)
	what := "finished"
	if t.Status == agentstate.Failed {
		what = "failed"
	} else if t.Status == agentstate.Stopped {
		what = "stopped"
	}
	detail := tui.FormatDuration(t.Duration())
	if n := t.PromptTokens + t.OutputTokens; n > 0 {
		detail += ", " + tui.FormatTokens(n) + " tokens"
	}
	body := fmt.Sprintf("Turn %d %s (%s).", t.N, what, detail)
	if t.Error != "" {
		body += " Error: " + t.Error
	}
	if msg := session.LastAssistant(st.Session); msg != "" {
		if len(msg) > FinalAnswerMax {
			msg = msg[:FinalAnswerMax] + fmt.Sprintf("\n[cut: atto agent report @%s has all of it]", st.Session)
		}
		body += "\n\n" + msg
	}
	return events.Event{Source: "agent", Text: agentstate.Envelope(agentstate.FinalAnswer, from, to, body),
		Title: fmt.Sprintf("◆ agent %s %s after %s", from, what, tui.FormatDuration(t.Duration()))}
}

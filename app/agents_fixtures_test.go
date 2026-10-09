package app

import (
	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/daemon"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/session"
)

// Old transcript fixtures are translated to the wire inventory so rendering
// tests exercise the same mapping as real clients.
type centerSnapshot struct {
	workers []daemon.Worker
	saved   []session.Summary
	agents  []centerAgent
}
type centerAgent struct {
	state agentstate.State
	turn  agentstate.Turn
}

func fixtureRows(snap centerSnapshot) []server.ThreadSummary {
	rows := []server.ThreadSummary{}
	for _, s := range snap.saved {
		row := server.ThreadSummary{ID: s.ID, Name: s.Name, Preview: s.Preview, LastMessage: s.LastMessage, Cwd: s.Cwd, Model: s.Model, Branch: s.Branch, Updated: s.Updated, Archived: s.Archived, External: s.External}
		if s.IsAgent() {
			row.Agent = &server.ThreadAgent{ParentThreadID: s.AgentOf, Lifecycle: agentstate.Open}
		}
		if m := s.Agent; m != nil {
			row.Agent = &server.ThreadAgent{ParentThreadID: m.Parent(), RootThreadID: m.RootSessionID, Name: m.Name, Path: m.Path, Role: m.Role, Origin: m.Origin, Project: m.Project, SpawnedBy: m.SpawnedBy, Lifecycle: agentstate.Open}
		}
		if row.Agent != nil && row.Archived {
			row.Agent.Lifecycle = agentstate.Closed
		}
		rows = append(rows, row)
	}
	for _, w := range snap.workers {
		row := server.ThreadSummary{ID: w.Session, Name: w.Name, Cwd: w.Cwd, Updated: w.Started, Loaded: true, Busy: w.Busy, OpenPrompt: w.State == "waiting"}
		found := false
		for i := range rows {
			if rows[i].ID == w.Session {
				rows[i].Loaded, rows[i].Busy, rows[i].OpenPrompt = true, row.Busy, row.OpenPrompt
				if row.Name != "" {
					rows[i].Name = row.Name
				}
				found = true
			}
		}
		if !found {
			rows = append(rows, row)
		}
	}
	for _, a := range snap.agents {
		st := a.state
		index := -1
		for i := range rows {
			if rows[i].ID == st.Session {
				index = i
			}
		}
		if index < 0 {
			rows = append(rows, server.ThreadSummary{ID: st.Session, Cwd: st.Cwd, Updated: st.Created})
			index = len(rows) - 1
		}
		row := &rows[index]
		row.Preview, row.Model = st.Task, st.Model
		if st.Branch != "" {
			row.Branch = st.Branch
		}
		lifecycle := st.Lifecycle
		if lifecycle == "" {
			lifecycle = agentstate.Open
		}
		if row.Archived {
			lifecycle = agentstate.Closed
		}
		row.Agent = &server.ThreadAgent{ParentThreadID: st.Parent, RootThreadID: st.Root, Name: st.Name, Path: st.Path, Role: st.Preset, Origin: st.Origin, Project: st.Project, SpawnedBy: st.SpawnedBy, Lifecycle: lifecycle, LastTurn: a.turn, WorktreeBranch: st.Branch}
	}
	return rows
}
func (c *agentCenter) applyFixture(snap centerSnapshot) { c.apply(fixtureRows(snap)) }

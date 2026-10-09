package server

import (
	"slices"
	"time"

	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/session"
)

// ThreadSummary is the inventory shared by session pickers and command centers.
// Listing does not attach to, load, or start any session.
type ThreadSummary struct {
	ID          string       `json:"threadId"`
	Name        string       `json:"name"`
	Preview     string       `json:"preview"`
	LastMessage string       `json:"lastMessage"`
	Cwd         string       `json:"cwd"`
	Model       string       `json:"model"`
	Branch      string       `json:"branch"`
	Updated     time.Time    `json:"updatedAt"`
	Messages    int          `json:"messages"`
	Loaded      bool         `json:"loaded"`
	Busy        bool         `json:"busy"`
	Archived    bool         `json:"archived"`
	External    bool         `json:"external"`
	OpenPrompt  bool         `json:"openPrompt"`
	GoalWaiting bool         `json:"goalWaiting"`
	Agent       *ThreadAgent `json:"agent,omitempty"`
}

type ThreadAgent struct {
	ParentThreadID string               `json:"parentThreadId"`
	RootThreadID   string               `json:"rootThreadId"`
	Depth          int                  `json:"depth"`
	Path           string               `json:"path"`
	Name           string               `json:"name"`
	Role           string               `json:"role"`
	Origin         string               `json:"origin"`
	Project        string               `json:"project"`
	SpawnedBy      *session.SpawnedBy   `json:"spawnedBy,omitempty"`
	Lifecycle      agentstate.Lifecycle `json:"lifecycle"`
	LastTurn       agentstate.Turn      `json:"lastTurn"`
	DurationMS     int64                `json:"durationMs"`
	WorktreeBranch string               `json:"worktreeBranch"`
}

func (s *Server) listThreads(p threadParams) (any, error) {
	listFn := session.List
	if p.IncludeAgents {
		listFn = session.ListAll
	}
	list, err := listFn(p.Cwd, p.Archived)
	if err != nil {
		return nil, err
	}
	if p.IncludeArchived && !p.Archived {
		archived, err := listFn(p.Cwd, true)
		if err != nil {
			return nil, err
		}
		list = append(list, archived...)
	}
	states := map[string]agentstate.State{}
	if p.IncludeAgents {
		for _, st := range agentstate.AllWithClosed() {
			states[st.Session] = st
			if st.Lifecycle == agentstate.Closed && !p.IncludeClosedAgents {
				continue
			}
			if p.Cwd != "" && !session.SameDir(st.Cwd, p.Cwd) {
				continue
			}
			if slices.ContainsFunc(list, func(x session.Summary) bool { return x.ID == st.Session }) {
				continue
			}
			if st.Lifecycle == agentstate.Closed && !p.IncludeArchived && !p.Archived {
				continue
			}
			if p.Archived && st.Lifecycle != agentstate.Closed {
				continue
			}
			list = append(list, session.Summary{ID: st.Session, Name: st.Name, Cwd: st.Cwd, Preview: st.Task, Model: st.Model, Updated: st.Created, Archived: st.Lifecycle == agentstate.Closed})
		}
		// Recorded parents, even before their first prompt, anchor the forest.
		for i := 0; i < len(list); i++ {
			parent := list[i].AgentOf
			if st, ok := states[list[i].ID]; ok {
				parent = st.Parent
			}
			if parent == "" || slices.ContainsFunc(list, func(x session.Summary) bool { return x.ID == parent }) {
				continue
			}
			if path, e := session.Find(parent); e == nil {
				if x, e := session.Summarize(path); e == nil {
					x.Archived = isArchivedPath(path)
					list = append(list, x)
				}
			}
		}
	}
	out := []map[string]any{}
	byID := map[string]map[string]any{}
	for _, x := range list {
		st, recorded := states[x.ID]
		if (recorded && st.Lifecycle == agentstate.Closed || x.IsAgent() && x.Archived) && !p.IncludeClosedAgents {
			continue
		}
		row := map[string]any{"threadId": x.ID, "name": x.Name, "preview": x.Preview, "lastMessage": x.LastMessage, "cwd": x.Cwd, "updatedAt": x.Updated, "messages": x.Messages, "loaded": false, "busy": false, "archived": x.Archived, "external": x.External, "branch": x.Branch, "model": x.Model, "openPrompt": false, "goalWaiting": false}
		var meta *ThreadAgent
		if x.Agent == nil && x.AgentOf != "" {
			meta = &ThreadAgent{ParentThreadID: x.AgentOf, RootThreadID: x.AgentOf, Depth: 1, Path: "/root/" + x.ID, Name: x.Name, Origin: session.OriginAgent, Lifecycle: agentstate.Open}
		}
		if m := x.Agent; m != nil {
			meta = &ThreadAgent{ParentThreadID: m.Parent(), RootThreadID: m.RootSessionID, Depth: m.Depth, Path: m.Path, Name: m.Name, Role: m.Role, Origin: m.Origin, Project: m.Project, SpawnedBy: m.SpawnedBy, Lifecycle: agentstate.Open}
			if x.Archived {
				meta.Lifecycle = agentstate.Closed
			}
		}
		if recorded {
			turn := st.Latest()
			meta = &ThreadAgent{ParentThreadID: st.Parent, RootThreadID: st.Root, Depth: st.Depth, Path: st.Path, Name: st.Name, Role: st.Preset, Origin: st.Origin, Project: st.Project, SpawnedBy: st.SpawnedBy, Lifecycle: st.Lifecycle, LastTurn: turn, DurationMS: turn.Duration().Milliseconds(), WorktreeBranch: st.Branch}
			if meta.Lifecycle == "" {
				meta.Lifecycle = agentstate.Open
			}
			row["name"], row["preview"], row["model"] = st.Name, st.Task, st.Model
			if st.Branch != "" {
				row["branch"] = st.Branch
			}
		}
		if g, _ := goal.Load(x.ID); g != nil {
			row["goalWaiting"] = g.Status == goal.Paused || g.Status == goal.Blocked || g.Status == goal.UsageLimited
		}
		if meta != nil {
			row["agent"] = meta
		}
		out = append(out, row)
		byID[x.ID] = row
	}
	s.mu.Lock()
	live := make([]*thread, 0, len(s.threads))
	for _, t := range s.threads {
		live = append(live, t)
	}
	s.mu.Unlock()
	if !p.Archived {
		for _, t := range live {
			var row map[string]any
			if err := t.call(func() error {
				if p.Cwd != "" && !session.SameDir(p.Cwd, t.cwd) {
					return nil
				}
				if st, ok := states[t.id]; ok && st.Lifecycle == agentstate.Closed && !p.IncludeClosedAgents {
					return nil
				}
				if !p.IncludeAgents && t.sess.IsAgent() {
					return nil
				}
				row = map[string]any{"threadId": t.id, "name": t.name, "cwd": t.cwd, "updatedAt": t.lastActive, "loaded": true, "busy": t.turns.Busy || t.shell != nil, "openPrompt": t.prompt != nil, "goalWaiting": t.goal.Goal != nil && (t.goal.Held() || t.goal.Goal.Status == goal.Paused || t.goal.Goal.Status == goal.Blocked || t.goal.Goal.Status == goal.UsageLimited)}
				return nil
			}); err != nil || row == nil {
				continue
			}
			if saved := byID[t.id]; saved != nil {
				for _, key := range []string{"name", "loaded", "busy", "openPrompt", "goalWaiting"} {
					saved[key] = row[key]
				}
			} else {
				out = append(out, row)
			}
		}
	}
	slices.SortStableFunc(out, func(a, b map[string]any) int {
		x, _ := a["updatedAt"].(time.Time)
		y, _ := b["updatedAt"].(time.Time)
		return y.Compare(x)
	})
	return map[string]any{"threads": out}, nil
}

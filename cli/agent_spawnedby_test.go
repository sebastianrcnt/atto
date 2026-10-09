package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

// A model session with a model, an effort and two user turns behind it.
func modelSession(t *testing.T, model, effort string) string {
	t.Helper()
	w := session.New(t.TempDir())
	w.Append(session.Entry{Type: session.TypeModel, Provider: "fake", Model: model})
	w.Append(session.Entry{Type: session.TypeEffort, Effort: effort})
	for _, text := range []string{"first", "second"} {
		w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: text}})
		w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: "ok"}})
	}
	w.Close()
	return w.ID
}

func TestSpawnedByRecordsTheModelSessionAndToolCall(t *testing.T) {
	agentServer(t, func(int, string) string { return textAnswer("done") })
	t.Chdir(t.TempDir())
	enableAgents(t, "")
	parent := modelSession(t, "small", "low")
	t.Setenv("ATTO_SESSION_ID", parent)
	t.Setenv(config.EnvToolCallID, "call_77")
	// The environment does not say which model: only the session's own record does.
	t.Setenv("ATTO_MODEL", "lies/lies")
	if _, err := runAgent(t, "spawn", "helper", "work"); err != nil {
		t.Fatal(err)
	}
	st, err := agentstate.LoadChild(parent, "helper")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { jobs.KillAll(parent); jobs.KillAll(st.Session) })
	by := st.SpawnedBy
	if by == nil || by.Origin != session.SpawnModel || by.Session == nil || *by.Session != parent || by.Model != "fake/small" || by.Effort != "low" || by.Turn != 2 || by.ToolCallID != "call_77" || by.Cwd == "" {
		t.Fatalf("spawnedBy: %+v", by)
	}
	// The header carries it too.
	path, _ := session.Find(st.Session)
	if h, _, err := session.Load(path); err != nil || h.Agent == nil || h.Agent.SpawnedBy == nil || h.Agent.SpawnedBy.ToolCallID != "call_77" || h.Agent.SpawnedBy.Origin != session.SpawnModel {
		t.Fatalf("header: %+v %v", h.Agent, err)
	}
	if _, err := runAgent(t, "wait", "helper", "-timeout", "30s"); err != nil {
		t.Fatal(err)
	}
	// list has a compact column, report -json the whole object.
	list, err := runAgent(t, "list")
	if err != nil || !strings.Contains(list, "BY") || !strings.Contains(list, "small·low t2") {
		t.Fatalf("list: %q %v", list, err)
	}
	out, err := runAgent(t, "report", "helper", "-json")
	var r struct {
		SpawnedBy struct {
			Session    string `json:"session"`
			Model      string `json:"model"`
			Effort     string `json:"effort"`
			Turn       int    `json:"turn"`
			ToolCallID string `json:"toolCallId"`
			Origin     string `json:"origin"`
		} `json:"spawnedBy"`
	}
	if err != nil || json.Unmarshal([]byte(out), &r) != nil || r.SpawnedBy.Session != parent || r.SpawnedBy.Model != "fake/small" || r.SpawnedBy.Effort != "low" || r.SpawnedBy.Turn != 2 || r.SpawnedBy.ToolCallID != "call_77" || r.SpawnedBy.Origin != "model" {
		t.Fatalf("report -json: %q %v", out, err)
	}
	if text, _ := runAgent(t, "report", "helper"); !strings.Contains(text, "started by small·low t2") {
		t.Fatalf("report: %q", text)
	}
}

// From an agent, the turn is the agent's own turn counter.
func TestSpawnedByTurnOfAnAgent(t *testing.T) {
	agentServer(t, func(int, string) string { return textAnswer("done") })
	t.Chdir(t.TempDir())
	enableAgents(t, "")
	parent := savedIDAgent(t, "p0", "mid")
	parent.Turns = 4
	if err := agentstate.Save(parent); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATTO_SESSION_ID", parent.Session)
	if _, err := runAgent(t, "spawn", "leaf", "work"); err != nil {
		t.Fatal(err)
	}
	leaf, err := agentstate.LoadChild(parent.Session, "leaf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { jobs.KillAll(parent.Session); jobs.KillAll(leaf.Session) })
	if leaf.SpawnedBy == nil || leaf.SpawnedBy.Turn != 4 || leaf.SpawnedBy.ToolCallID != "" {
		t.Fatalf("spawnedBy: %+v", leaf.SpawnedBy)
	}
}

// An outside orchestrator that names a session records that, and an
// orchestrator with none records only "outside".
func TestSpawnedByFromOutsideAndWithExplicitSession(t *testing.T) {
	agentServer(t, func(int, string) string { return textAnswer("done") })
	t.Chdir(t.TempDir())
	enableAgents(t, "")
	t.Setenv(config.EnvToolCallID, "call_from_a_stale_shell") // not a model shell: ignored
	root, _ := spawnExternalAgent(t, "o")
	if root.SpawnedBy == nil || root.SpawnedBy.Origin != session.SpawnOutside || root.SpawnedBy.ToolCallID != "" || root.SpawnedBy.Session != nil {
		t.Fatalf("outside: %+v", root.SpawnedBy)
	}
	parent := modelSession(t, "m", "high")
	if _, err := runAgent(t, "spawn", "x", "work", "-session", parent); err != nil {
		t.Fatal(err)
	}
	x, err := agentstate.LoadChild(parent, "x")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { jobs.KillAll(parent); jobs.KillAll(x.Session) })
	if by := x.SpawnedBy; by == nil || by.Origin != session.SpawnExplicitSession || by.Session == nil || *by.Session != parent || by.Model != "fake/m" || by.Effort != "high" || by.Turn != 2 || by.ToolCallID != "" {
		t.Fatalf("explicit session: %+v", by)
	}
	list, _ := runAgent(t, "list", "-session", parent)
	if !strings.Contains(list, "m·high t2 (-session)") {
		t.Fatalf("list: %q", list)
	}
	if outside, _ := runAgent(t, "list"); !strings.Contains(outside, "outside") {
		t.Fatalf("outside list: %q", outside)
	}
}

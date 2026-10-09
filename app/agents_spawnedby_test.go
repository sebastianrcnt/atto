package app

import (
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

// The details of an agent say who started it, as tracking and not proof.
func TestCenterDetailShowsWhoStartedTheAgent(t *testing.T) {
	c := &agentCenter{flat: true}
	parent := "root"
	c.apply(centerSnapshot{
		saved: []session.Summary{{ID: "root", Cwd: "/work"}, {ID: "tests", Name: "tests", AgentOf: "root", Cwd: "/work"}},
		agents: []centerAgent{{state: agentstate.State{Session: "tests", Parent: "root", Name: "tests", Task: "Run suite", Model: "fake/m",
			SpawnedBy: &session.SpawnedBy{Session: &parent, Model: "openai/gpt-6.1-sol", Effort: "high", Turn: 3, ToolCallID: "call_9", Origin: session.SpawnModel}},
			turn: agentstate.Turn{Status: agentstate.Done}}},
	})
	var detail []string
	for _, it := range c.shown() {
		if it.id == "tests" {
			detail = c.renderDetail(it, 60)
		}
	}
	text := tui.StripEscapes(strings.Join(detail, "\n"))
	if !strings.Contains(text, "Started by: sol·high t3 · call call_9") {
		t.Fatalf("details:\n%s", text)
	}
}

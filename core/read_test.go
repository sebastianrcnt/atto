package core

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

func TestSavedRestoresOnlyAfterCompaction(t *testing.T) {
	setup(t)
	w := session.New(t.TempDir())
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "old"}})
	w.Append(session.Entry{Type: session.TypeCompaction, Replacement: []provider.Message{{Role: "user", Content: "notes"}}})
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: "answer"}, Usage: &provider.Usage{PromptTokens: 20, CompletionTokens: 5}})
	w.Close()
	saved, file, err := OpenDisplay(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if len(saved.Entries) != 3 || len(saved.Branch()) != 2 || file.Leaf() != session.Leaf(saved.Entries) {
		t.Fatal("display, agent and writer paths differ")
	}
	light, err := Read(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(light.Entries, saved.Branch()) {
		t.Fatal("context-only read differs")
	}
	old := agent.New(config.ModelRef{}, "", t.TempDir())
	old.Restore(saved.Entries)
	got := agent.New(config.ModelRef{}, "", old.Cwd)
	got.Restore(saved.Branch())
	if !reflect.DeepEqual(got.Messages(), old.Messages()) || got.LastUsage != old.LastUsage || got.ContextTokens() != old.ContextTokens() {
		t.Fatal("agent restore differs")
	}
}

func TestReadSessionWideSnapshots(t *testing.T) {
	setup(t)
	w := session.New(t.TempDir())
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "root"}})
	root := w.Leaf()
	w.Append(session.Entry{Type: session.TypeModel, Provider: "a", Model: "one"})
	w.Append(session.Entry{Type: session.TypeEffort, Effort: "high"})
	w.Append(session.Entry{Type: session.TypeContext, LongContext: true})
	w.Append(session.Entry{Type: session.TypeName, Name: "named on an abandoned branch"})
	g, err := goal.New("finish")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	w.Append(session.Entry{Type: session.TypeGoal, Goal: raw})
	w.Branch(root)
	w.Append(session.Entry{Type: session.TypeCompaction, Replacement: []provider.Message{{Role: "user", Content: "notes"}}})
	w.Close()
	_, entries, err := session.Load(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := Read(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Model != "a/one" || saved.Effort != "high" || !saved.LongContext || saved.Name != "named on an abandoned branch" {
		t.Fatal("choices must remain session-wide")
	}
	old := GoalDriver{Session: "old"}
	got := GoalDriver{Session: "got"}
	if old.Restore(entries) != got.Restore(saved.Snapshots()) || old.Goal == nil || got.Goal == nil {
		t.Fatal("goal restore differs")
	}
	// Each restore saves the live goal with its own current timestamp.
	got.Goal.Updated = old.Goal.Updated
	if !reflect.DeepEqual(old.Goal, got.Goal) {
		t.Fatal("goal state differs")
	}
}

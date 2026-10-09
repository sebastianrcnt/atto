package agentturn

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

func setup(t *testing.T) {
	t.Helper()
	t.Setenv(config.EnvDir, t.TempDir())
	if err := agentstate.WriteMarker("test", ""); err != nil {
		t.Fatal(err)
	}
}

func agentWithAnswer(t *testing.T, parent, name, answer string) agentstate.State {
	t.Helper()
	w := session.NewAgent("/w", parent)
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: answer}})
	w.Close()
	st := agentstate.State{Name: name, Parent: parent, Session: w.ID, Created: time.Now()}
	if parent == "" {
		st.Origin = session.OriginExternal
	}
	if err := agentstate.Save(st); err != nil {
		t.Fatal(err)
	}
	st, _ = agentstate.Load(w.ID)
	return st
}

func TestPrepareNumbersTheTurnAndNamesTheJobOwner(t *testing.T) {
	setup(t)
	child := agentWithAnswer(t, "parent1", "kid", "x")
	root := agentWithAnswer(t, "", "top", "x")
	for _, c := range []struct {
		st    agentstate.State
		owner string
	}{{child, "parent1"}, {root, root.Session}} {
		got, err := Prepare(c.st, "do it")
		if err != nil || got.Turns != 1 || got.Prompt != "do it" || got.Job != 0 || got.JobOwner != c.owner {
			t.Fatalf("%+v %v", got, err)
		}
		again, _ := Prepare(got, "more")
		if again.Turns != 2 || again.Prompt != "more" {
			t.Fatalf("second turn %+v", again)
		}
	}
}

func TestFinishRecordsTheTurnAndTellsOnlyAParent(t *testing.T) {
	setup(t)
	child := agentWithAnswer(t, "parent1", "kid", strings.Repeat("a", FinalAnswerMax+10))
	child.Turns = 1
	turn := agentstate.Turn{N: 1, Queued: time.Now(), Started: time.Now().Add(-time.Second)}
	never := func(*agentstate.State, string) error { t.Fatal("a successor without waiting work"); return nil }
	if err := Finish(child, turn, Result{Prompt: 100, Cached: 40, Output: 7, Cost: 0.5, Steps: 3}, never); err != nil {
		t.Fatal(err)
	}
	got, ok := agentstate.LoadTurn(child.Session)
	if !ok || got.Status != agentstate.Done || got.PromptTokens != 100 || got.CachedTokens != 40 || got.OutputTokens != 7 || got.Cost != 0.5 || got.Steps != 3 || got.Ended.IsZero() {
		t.Fatalf("turn %+v", got)
	}
	evs := events.Drain("parent1")
	if len(evs) != 1 || !strings.Contains(evs[0].Text, "Message Type: FINAL_ANSWER\nFrom: /root/kid\nTo: /root\n") || !strings.Contains(evs[0].Text, "[cut: atto agent report @"+child.Session+" has all of it]") {
		t.Fatalf("final answer: %+v", evs)
	}
	if len(evs[0].Text) > FinalAnswerMax+500 {
		t.Fatalf("answer not capped: %d", len(evs[0].Text))
	}

	// A root has no recipient.
	root := agentWithAnswer(t, "", "top", "done")
	root.Turns = 1
	if err := Finish(root, agentstate.Turn{N: 1, Started: time.Now()}, Result{Error: "boom"}, never); err != nil {
		t.Fatal(err)
	}
	if got, _ := agentstate.LoadTurn(root.Session); got.Status != agentstate.Failed || got.Error != "boom" {
		t.Fatalf("root turn %+v", got)
	}
	if evs := events.Drain(root.Session); len(evs) != 0 {
		t.Fatalf("a root told itself: %+v", evs)
	}
}

func TestFinishStoppedAndSuccessor(t *testing.T) {
	setup(t)
	kid := agentWithAnswer(t, "parent1", "kid", "ok")
	kid.Turns = 1
	// Work that arrived as the turn ended gets a successor.
	if err := events.Push(kid.Session, events.Event{Source: "agent", Text: agentstate.Envelope(agentstate.NewTask, "/root", "/root/kid", "another")}); err != nil {
		t.Fatal(err)
	}
	var started string
	err := Finish(kid, agentstate.Turn{N: 1, Started: time.Now()}, Result{}, func(st *agentstate.State, text string) error { started = text; return nil })
	if err != nil || !strings.Contains(started, "another") {
		t.Fatalf("successor: %q %v", started, err)
	}
	// A stopped turn tells the parent but starts nothing, and leaves the inbox alone.
	events.Drain("parent1")
	if err := events.Push(kid.Session, events.Event{Source: "agent", Text: agentstate.Envelope(agentstate.NewTask, "/root", "/root/kid", "later")}); err != nil {
		t.Fatal(err)
	}
	err = Finish(kid, agentstate.Turn{N: 1, Started: time.Now()}, Result{Stopped: true}, func(*agentstate.State, string) error { return errors.New("no successor after a stop") })
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := agentstate.LoadTurn(kid.Session); got.Status != agentstate.Stopped {
		t.Fatalf("turn %+v", got)
	}
	if evs := events.Drain("parent1"); len(evs) != 1 || !strings.Contains(evs[0].Text, "Turn 1 stopped") {
		t.Fatalf("parent: %+v", evs)
	}
	if evs := events.Drain(kid.Session); len(evs) != 1 {
		t.Fatalf("the waiting task was taken: %+v", evs)
	}
}

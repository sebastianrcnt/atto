package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/provider/providertest"
	"github.com/sebastianrcnt/atto/session"
)

// harness is a server with the scripted model, a thread and a client in
// the same process following its events.
type harness struct {
	t  *testing.T
	s  *Server
	m  *providertest.Model
	c  *Client
	id string
	ev chan Notification
}

func newHarness(t *testing.T, script ...providertest.Reply) *harness {
	t.Helper()
	s, m := testServer(t, script...)
	h := &harness{t: t, s: s, m: m, ev: make(chan Notification, 10000)}
	h.c = h.connect()
	var th ThreadInfo
	if err := h.c.Call(context.Background(), "thread/start", map[string]any{}, &th); err != nil {
		t.Fatal(err)
	}
	h.id = th.ID
	return h
}

// connect is another client, its events going to h.ev when it is the
// first.
func (h *harness) connect() *Client {
	c := Connect(context.Background(), h.s)
	h.t.Cleanup(func() { c.Close() })
	if err := c.Call(context.Background(), "initialize", map[string]any{"protocolVersions": []int{2}, "capabilities": map[string]bool{"interactive": true}}, nil); err != nil {
		h.t.Fatal(err)
	}
	if h.c == nil {
		go func() {
			for n := range c.Events() {
				h.ev <- n
			}
		}()
	} else {
		go func() {
			for range c.Events() {
			}
		}()
	}
	return c
}

func (h *harness) call(method string, params map[string]any) map[string]any {
	h.t.Helper()
	out, err := h.try(h.c, method, params)
	if err != nil {
		h.t.Fatalf("%s: %v", method, err)
	}
	return out
}

func (h *harness) try(c *Client, method string, params map[string]any) (map[string]any, error) {
	if params == nil {
		params = map[string]any{}
	}
	params["threadId"] = h.id
	var out map[string]any
	err := c.Call(context.Background(), method, params, &out)
	return out, err
}

// wait returns the params of the next notification method for which ok
// holds (nil ok: any).
func (h *harness) wait(method string, ok func(map[string]any) bool) map[string]any {
	h.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case n := <-h.ev:
			if n.Method != method {
				continue
			}
			var p map[string]any
			json.Unmarshal(n.Params, &p)
			if ok == nil || ok(p) {
				return p
			}
		case <-deadline:
			h.t.Fatalf("no %s", method)
		}
	}
}

// idle waits for the turn running to end and none to follow.
func (h *harness) completed() map[string]any {
	h.t.Helper()
	return h.wait("turn/completed", nil)
}

func itemOf(p map[string]any) map[string]any {
	it, _ := p["item"].(map[string]any)
	return it
}

// Enter steers a running turn, Tab queues, and a queued message is taken
// back by its ID; the steer reaches the model in the same turn.
func TestSteerQueueAndTakeBack(t *testing.T) {
	gate := make(chan struct{})
	h := newHarness(t, providertest.Reply{Text: "first answer", Gate: gate}, providertest.Reply{Text: "steered answer"})
	r := h.call("input/submit", map[string]any{"input": "start"})
	if r["status"] != StatusStarted || r["inputId"] == "" {
		t.Fatalf("submit: %v", r)
	}
	h.m.Started(5 * time.Second)
	if r = h.call("input/submit", map[string]any{"input": "also this"}); r["status"] != StatusSteered {
		t.Fatalf("steer: %v", r)
	}
	q := h.call("input/submit", map[string]any{"input": "queued-x7", "intent": "queue"})
	if q["status"] != StatusQueued {
		t.Fatalf("queue: %v", q)
	}
	p := h.wait("turn/pending", func(p map[string]any) bool {
		items, _ := p["pending"].(map[string]any)["items"].([]any)
		return len(items) == 2
	})
	_ = p
	back := h.call("turn/unsteer", map[string]any{"inputId": q["inputId"]})
	if back["text"] != "queued-x7" {
		t.Fatalf("takeback: %v", back)
	}
	close(gate)
	if c := h.completed(); c["status"] != "completed" {
		t.Fatalf("turn: %v", c)
	}
	reqs := h.m.Requests()
	if len(reqs) != 2 || !strings.Contains(reqs[1], "also this") || strings.Contains(strings.Join(reqs, ""), "queued-x7") {
		t.Fatalf("requests: %d %v", len(reqs), reqs)
	}
}

// Ctrl+Enter interrupts the running turn and sends the draft as the next
// one, after the steers the turn had not taken.
func TestReplaceSendsNow(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	h := newHarness(t, providertest.Reply{Text: "never", Gate: gate}, providertest.Reply{Text: "ok"})
	h.call("input/submit", map[string]any{"input": "slow"})
	h.m.Started(5 * time.Second)
	h.call("input/submit", map[string]any{"input": "steer one"})
	if r := h.call("input/submit", map[string]any{"input": "now", "intent": "replace"}); r["status"] != StatusQueued {
		t.Fatalf("replace: %v", r)
	}
	if c := h.completed(); c["status"] != "interrupted" {
		t.Fatalf("first turn: %v", c)
	}
	h.wait("item/started", func(p map[string]any) bool { return itemOf(p)["text"] == "steer one\n\nnow" })
	if c := h.completed(); c["status"] != "completed" {
		t.Fatalf("second turn: %v", c)
	}
}

// A turn the model never answered gives its message back to the client
// that sent it; queued messages pause until resumed.
func TestFailedTurnRecoversAndPausesQueue(t *testing.T) {
	gate := make(chan struct{})
	h := newHarness(t, providertest.Reply{Status: 404, Gate: gate}, providertest.Reply{Text: "ok"})
	h.call("input/submit", map[string]any{"input": "doomed"})
	h.m.Started(5 * time.Second)
	h.call("input/submit", map[string]any{"input": "next", "intent": "queue"})
	close(gate)
	rec := h.wait("input/recovered", nil)
	if rec["text"] != "doomed" || rec["ifEmpty"] != true || rec["clientId"] == "" {
		t.Fatalf("recovered: %v", rec)
	}
	h.wait("turn/pending", func(p map[string]any) bool { return p["pending"].(map[string]any)["paused"] == true })
	h.call("queue/resume", nil)
	h.wait("item/started", func(p map[string]any) bool { return itemOf(p)["text"] == "next" })
}

// While a client has a picker open, inbox events wait; they start a turn
// once it closes. A client that goes away releases its gate.
func TestEventsWaitForOpenPicker(t *testing.T) {
	h := newHarness(t, providertest.Reply{Text: "seen"})
	h.call("client/gate", map[string]any{"open": true})
	if err := events.Push(h.id, events.Event{Source: "job", Title: "job 1 exited", Text: "done"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1200 * time.Millisecond) // two inbox ticks
	if n := len(h.m.Requests()); n != 0 {
		t.Fatalf("%d requests while a picker was open", n)
	}
	h.call("client/gate", map[string]any{"open": false})
	h.wait("event", nil)
	h.completed()
}

// A prompt goes to every client; the first answer wins and a late one is
// refused as stale.
func TestPromptFirstAnswerWins(t *testing.T) {
	h := newHarness(t)
	other := h.connect()
	th := h.s.threads[h.id]
	answered := make(chan int, 2)
	th.do(func() {
		th.ask(&openPrompt{wire: Prompt{Kind: PromptSelect, Title: "Pick", Options: []PromptOption{{Label: "a"}, {Label: "b"}}},
			choose: func(i int) { answered <- i }, cancel: func() { answered <- -1 }})
	})
	open := h.wait("prompt/open", nil)
	id := open["prompt"].(map[string]any)["id"].(string)
	one := 1
	if _, err := h.try(other, "prompt/answer", map[string]any{"id": id, "index": one}); err != nil {
		t.Fatal(err)
	}
	_, err := h.try(h.c, "prompt/answer", map[string]any{"id": id, "index": 0})
	var re *RPCError
	if !errors.As(err, &re) || re.Data == nil || re.Data.Reason != ReasonStalePrompt {
		t.Fatalf("late answer: %v", err)
	}
	if got := <-answered; got != 1 {
		t.Fatalf("answered %d", got)
	}
	if closed := h.wait("prompt/closed", nil); closed["how"] != "answered" || closed["by"] == "" {
		t.Fatalf("closed: %v", closed)
	}
}

// A "!" command run during a turn joins the context only when the turn
// ends, and its item says so until then.
func TestUserShellWaitsForRun(t *testing.T) {
	gate := make(chan struct{})
	h := newHarness(t, providertest.Reply{Text: "ok", Gate: gate}, providertest.Reply{Text: "after"})
	h.call("input/submit", map[string]any{"input": "work"})
	h.m.Started(5 * time.Second)
	h.call("input/submit", map[string]any{"input": "!echo shelled"})
	done := h.wait("item/completed", func(p map[string]any) bool { return itemOf(p)["shell"] == true })
	if itemOf(done)["contextPending"] != true {
		t.Fatalf("shell during turn: %v", itemOf(done))
	}
	close(gate)
	// After the turn ends (turn/completed comes first).
	h.wait("item/updated", func(p map[string]any) bool {
		return itemOf(p)["shell"] == true && itemOf(p)["contextPending"] == nil
	})
	h.call("input/submit", map[string]any{"input": "next"})
	h.completed()
	if reqs := h.m.Requests(); !strings.Contains(reqs[len(reqs)-1], "shelled") {
		t.Fatal("the command's output never reached the model")
	}
}

// With retention 0 a thread its last client leaves idle closes at once:
// SessionEnd runs and the lease is let go.
func TestDetachRetiresIdleThread(t *testing.T) {
	h := newHarness(t, providertest.Reply{Text: "ok"})
	h.s.Retire = true
	h.call("input/submit", map[string]any{"input": "hello"})
	h.completed()
	r := h.call("thread/detach", map[string]any{"reason": "clear"})
	if r["closed"] != true || h.s.Loaded(h.id) {
		t.Fatalf("detach: %v", r)
	}
	path, err := session.Find(h.id)
	if err != nil {
		t.Fatal(err)
	}
	if _, locked := session.LockedBy(path); locked {
		t.Fatal("lease kept after retiring")
	}
}

// A goal set over the protocol starts working at once; pausing it stops
// the turns after the current step.
func TestGoalRunsAndPauses(t *testing.T) {
	h := newHarness(t, providertest.Reply{Text: "working on it", Words: 3, Delay: 20 * time.Millisecond})
	h.call("goal/set", map[string]any{"input": "ship the feature"})
	h.wait("item/started", func(p map[string]any) bool { return itemOf(p)["type"] == ItemGoal })
	r := h.call("goal/pause", nil)
	if g, _ := r["goal"].(map[string]any); g["status"] != "paused" {
		t.Fatalf("pause: %v", r)
	}
	h.completed()
	n := len(h.m.Requests())
	time.Sleep(700 * time.Millisecond)
	if len(h.m.Requests()) != n {
		t.Fatal("a paused goal kept starting turns")
	}
}

// A detached busy thread goes on, and retires once its turn is over.
func TestDetachedTurnGoesOn(t *testing.T) {
	gate := make(chan struct{})
	h := newHarness(t, providertest.Reply{Text: "late", Gate: gate})
	h.s.Retire = true
	h.call("input/submit", map[string]any{"input": "hello"})
	h.m.Started(5 * time.Second)
	if r := h.call("thread/detach", nil); r["closed"] == true {
		t.Fatal("a busy thread closed on detach")
	}
	close(gate)
	h.wait("thread/closed", nil)
	path, _ := session.Find(h.id)
	_, entries, err := session.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		found = found || (e.Message != nil && e.Message.Content == "late")
	}
	if !found {
		t.Fatal("the detached turn's answer was not saved")
	}
}

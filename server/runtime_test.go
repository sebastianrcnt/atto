package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/goal"
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

func TestInterruptModesSettlePendingInput(t *testing.T) {
	for _, mode := range []string{"cancel", "sendPending"} {
		t.Run(mode, func(t *testing.T) {
			gate := make(chan struct{})
			defer close(gate)
			h := newHarness(t, providertest.Reply{Text: "waiting", Gate: gate}, providertest.Reply{Text: "next"})
			h.call("input/submit", map[string]any{"input": "work"})
			if h.m.Started(5*time.Second) != 1 {
				t.Fatal("model did not start")
			}
			h.call("input/submit", map[string]any{"input": "pending"})
			h.call("turn/interrupt", map[string]any{"mode": mode})
			if p := h.completed(); p["status"] != "interrupted" {
				t.Fatalf("turn: %v", p)
			}
			if mode == "cancel" {
				if p := h.wait("input/recovered", nil); p["text"] != "pending" || p["clientId"] == "" {
					t.Fatalf("recovery: %v", p)
				}
				if len(h.m.Requests()) != 1 {
					t.Fatal("Ctrl+C sent a pending steer")
				}
			} else {
				h.wait("item/started", func(p map[string]any) bool { return itemOf(p)["text"] == "pending" })
				h.completed()
				if len(h.m.Requests()) != 2 {
					t.Fatal("Esc did not start the pending steer")
				}
			}
		})
	}
}

func TestRuntimeTreeForkLabelsAndSummary(t *testing.T) {
	h := newHarness(t, providertest.Reply{Text: "answer one"}, providertest.Reply{Text: "answer two"}, providertest.Reply{Text: "branch notes"}, providertest.Reply{Text: "new branch"})
	for _, text := range []string{"one", "two"} {
		h.call("turn/start", map[string]any{"input": text})
		h.completed()
	}
	var tree struct {
		Entries []session.Entry `json:"entries"`
		Leaf    string          `json:"leaf"`
	}
	if err := h.c.Call(context.Background(), "thread/tree", map[string]any{"threadId": h.id}, &tree); err != nil {
		t.Fatal(err)
	}
	users := UserMessages(session.Active(tree.Entries))
	if len(users) != 2 || tree.Leaf == "" {
		t.Fatalf("tree: %+v", tree)
	}
	h.call("thread/setLabel", map[string]any{"entryId": users[1].ID, "label": "checkpoint"})
	fork := h.call("thread/fork", map[string]any{"entryId": users[1].ID})
	if fork["input"] != "two" {
		t.Fatalf("fork: %v", fork)
	}
	_, entries, err := session.Load(fork["path"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if got := UserMessages(session.Active(entries)); len(got) != 1 || got[0].Message.Content != "one" {
		t.Fatalf("fork entries: %+v", got)
	}
	h.call("thread/navigate", map[string]any{"entryId": users[1].ID, "summary": map[string]any{"mode": "custom", "instructions": "keep decisions"}})
	h.wait("thread/branchChanged", nil)
	if rec := h.wait("input/recovered", nil); rec["text"] != "two" {
		t.Fatalf("navigation recovery: %v", rec)
	}
	h.call("turn/start", map[string]any{"input": "after move"})
	h.completed()
	requests := h.m.Requests()
	if len(requests) != 4 || !strings.Contains(requests[2], "keep decisions") || !strings.Contains(requests[3], "branch notes") {
		t.Fatalf("summary requests: %v", requests)
	}
	if err := h.c.Call(context.Background(), "thread/tree", map[string]any{"threadId": h.id}, &tree); err != nil {
		t.Fatal(err)
	}
	label, oldBranch := false, false
	for _, e := range tree.Entries {
		label = label || e.Type == session.TypeLabel && e.Label == "checkpoint"
		oldBranch = oldBranch || e.Message != nil && e.Message.Content == "answer two"
	}
	if !label || !oldBranch {
		t.Fatal("tree lost labels or abandoned branch")
	}
}

func TestUnattendedPromptsAreNotAutoAnswered(t *testing.T) {
	h := newHarness(t)
	th, _ := h.s.thread(h.id)
	answers := make(chan string, 3)
	for _, title := range []string{"first", "second"} {
		q := title
		_ = th.call(func() error {
			th.ask(&openPrompt{wire: Prompt{Kind: PromptInput, Title: q}, submit: func(text string) { answers <- text }, cancel: func() { answers <- "cancelled" }})
			return nil
		})
	}
	first := h.wait("prompt/open", nil)["prompt"].(map[string]any)["id"].(string)
	h.c.Close()
	time.Sleep(30 * time.Millisecond)
	select {
	case a := <-answers:
		t.Fatalf("unattended prompt auto-answered: %s", a)
	default:
	}
	c := h.connect()
	var snapshot ThreadInfo
	if err := c.Call(context.Background(), "thread/attach", map[string]any{"threadId": h.id}, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Prompt == nil || snapshot.Prompt.ID != first {
		t.Fatalf("prompt not preserved: %+v", snapshot.Prompt)
	}
	if err := c.Call(context.Background(), "prompt/answer", map[string]any{"threadId": h.id, "id": first, "text": "answered first"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := <-answers; got != "answered first" {
		t.Fatalf("answer: %s", got)
	}
	if err := c.Call(context.Background(), "thread/read", map[string]any{"threadId": h.id}, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Prompt == nil || snapshot.Prompt.Title != "second" {
		t.Fatalf("queued prompt: %+v", snapshot.Prompt)
	}
	if err := c.Call(context.Background(), "prompt/answer", map[string]any{"threadId": h.id, "id": snapshot.Prompt.ID, "text": "answered second"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := <-answers; got != "answered second" {
		t.Fatalf("answer: %s", got)
	}
}

func TestCommandsShellExclusionAndTimer(t *testing.T) {
	h := newHarness(t, providertest.Reply{Text: "done"})
	catalog := h.call("commands/list", nil)
	if len(catalog["commands"].([]any)) < 20 {
		t.Fatalf("catalog: %v", catalog)
	}
	h.call("commands/run", map[string]any{"name": "name", "args": "runtime name"})
	h.call("commands/run", map[string]any{"name": "context", "args": "long"})
	state := h.call("thread/read", nil)
	if state["name"] != "runtime name" || state["longContext"] != true {
		t.Fatalf("commands: %v", state)
	}
	h.call("shell/start", map[string]any{"command": "echo secret-shell-output", "exclude": true})
	h.wait("item/completed", func(p map[string]any) bool { return itemOf(p)["shell"] == true })
	h.call("turn/start", map[string]any{"input": "hello"})
	h.completed()
	if strings.Contains(h.m.Requests()[0], "secret-shell-output") {
		t.Fatal("excluded shell reached model")
	}
	h.call("thread/reload", nil)
	h.wait("thread/reloaded", nil)
	ctx := h.call("thread/context", map[string]any{"view": "system"})
	if ctx["systemPrompt"] == "" {
		t.Fatal("system context missing")
	}
	debug := h.call("thread/debugRequest", nil)
	if debug["request"] == "" {
		t.Fatal("debug request missing")
	}
	h.call("timer/create", map[string]any{"when": "1ms", "message": "timer wake"})
	h.wait("event", func(p map[string]any) bool { return p["source"] == "timer" })
	h.completed()
	if len(h.m.Requests()) != 2 || !strings.Contains(h.m.Requests()[1], "timer wake") {
		t.Fatalf("timer requests: %v", h.m.Requests())
	}
}

func TestExtensionPromptAndStepEndUseRuntime(t *testing.T) {
	h := newHarness(t, providertest.Reply{Text: "extension answer", Prompt: 20, Completion: 4})
	path := filepath.Join(config.Dir(), "extensions", "ask.ts")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`export default (atto:any) => {
  atto.on("user_prompt", async (_e:any,ctx:any) => {
    const answer=await ctx.ui.confirm("Continue?");
    return answer ? {} : {block:true,reason:"declined"};
  });
  atto.on("step_end", (e:any,ctx:any) => ctx.ui.notify("step-end:" + e.outputTokens));
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h.call("thread/reload", nil)
	h.wait("thread/reloaded", nil)
	h.call("turn/start", map[string]any{"input": "go"})
	open := h.wait("prompt/open", nil)["prompt"].(map[string]any)
	if open["origin"] != "extension" || open["confirm"] != true {
		t.Fatalf("extension prompt: %v", open)
	}
	if len(h.m.Requests()) != 0 {
		t.Fatal("model ran before extension approval")
	}
	h.call("prompt/answer", map[string]any{"id": open["id"], "index": 0})
	h.wait("extension/notify", func(p map[string]any) bool { return strings.HasPrefix(p["message"].(string), "step-end:") })
	if reqs := h.m.Requests(); len(reqs) != 1 {
		t.Fatalf("model requests: %d", len(reqs))
	}
}

func TestGoalRetryCanBeInterrupted(t *testing.T) {
	h := newHarness(t)
	th, _ := h.s.thread(h.id)
	g, err := goal.New("retry work")
	if err != nil {
		t.Fatal(err)
	}
	_ = th.call(func() error {
		th.goal.Set(g)
		th.goal.BeginTurn()
		th.goal.EndTurn(errors.New("temporary network failure"))
		th.continueGoal()
		return nil
	})
	h.wait("goal/retry", nil)
	h.call("turn/interrupt", map[string]any{"mode": "cancel"})
	state := h.call("goal/read", nil)["goal"].(map[string]any)
	if state["status"] != "paused" || len(h.m.Requests()) != 0 {
		t.Fatalf("retry interrupt: %v", state)
	}
	_ = th.call(func() error {
		if th.retryTimer != nil {
			t.Error("retry timer survived interrupt")
		}
		return nil
	})
}

func TestRetirementKeepsTimersAndPromptsAndRechecksAttachments(t *testing.T) {
	h := newHarness(t)
	h.s.Retire = true
	h.call("timer/create", map[string]any{"when": "1h", "message": "future work"})
	if result := h.call("thread/detach", nil); result["closed"] == true {
		t.Fatal("timer-bearing thread retired")
	}
	h.call("thread/attach", nil)
	th, _ := h.s.thread(h.id)
	// A retention callback already queued must not close a newly attached
	// client, even if its timer observed an earlier unattended state.
	if result := h.s.closeThread(th, closeMode{retire: true, reason: "exit"}); result.Closed {
		t.Fatal("stale retention closed attached thread")
	}
	_ = th.call(func() error {
		for _, timer := range events.Timers(h.id) {
			_ = events.CancelTimer(h.id, timer.ID)
		}
		th.setCounts(0, 0)
		th.ask(&openPrompt{wire: Prompt{Kind: PromptInput, Title: "waiting"}, submit: func(string) {}, cancel: func() {}})
		return nil
	})
	if result := h.call("thread/detach", nil); result["closed"] == true {
		t.Fatal("unanswered prompt retired")
	}
}

func TestShutdownRejectsNewThreads(t *testing.T) {
	s, _ := testServer(t)
	s.Close()
	c := Connect(context.Background(), s)
	defer c.Close()
	if err := c.Call(context.Background(), "thread/start", nil, nil); err == nil {
		t.Fatal("closed runtime opened a session")
	}
}

func TestQuietExitWaitsForNextExplicitTurn(t *testing.T) {
	h := newHarness(t, providertest.Reply{Text: "first response"}, providertest.Reply{Text: "event received"})
	if err := events.Push(h.id, events.Event{Source: "job", Title: "quiet exit", Text: "quiet-job-marker", Quiet: true}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond)
	if len(h.m.Requests()) != 0 {
		t.Fatal("quiet exit woke an idle runtime")
	}
	h.call("turn/start", map[string]any{"input": "next explicit turn"})
	h.completed()
	reqs := h.m.Requests()
	if len(reqs) != 2 || !strings.Contains(reqs[1], "quiet-job-marker") {
		t.Fatalf("quiet event not delivered in explicit turn: %v", reqs)
	}
}

func TestCancelledSteersRecoverToTheirSendingClients(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	h := newHarness(t, providertest.Reply{Text: "waiting", Gate: gate})
	other := h.connect()
	var first, second struct {
		ClientID string `json:"clientId"`
	}
	if err := h.c.Call(context.Background(), "initialize", nil, &first); err != nil {
		t.Fatal(err)
	}
	if err := other.Call(context.Background(), "initialize", nil, &second); err != nil {
		t.Fatal(err)
	}
	h.call("turn/start", map[string]any{"input": "work"})
	h.m.Started(5 * time.Second)
	h.call("turn/steer", map[string]any{"input": "same"})
	if _, err := h.try(other, "turn/steer", map[string]any{"input": "same"}); err != nil {
		t.Fatal(err)
	}
	h.call("turn/interrupt", map[string]any{"mode": "cancel"})
	h.completed()
	seen := map[string]bool{}
	for range 2 {
		p := h.wait("input/recovered", nil)
		if p["text"] != "same" {
			t.Fatalf("mixed client drafts: %v", p)
		}
		seen[p["clientId"].(string)] = true
	}
	if !seen[first.ClientID] || !seen[second.ClientID] || first.ClientID == second.ClientID {
		t.Fatalf("recovery origins: %v", seen)
	}
}

// A user shell has its own lifetime, not the model turn's. Its final
// output and successful completion must still reach clients and context.
func TestUserShellOutlivesRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell gate")
	}
	gate := make(chan struct{})
	h := newHarness(t, providertest.Reply{Text: "ok", Gate: gate}, providertest.Reply{Text: "after"})
	h.call("turn/start", map[string]any{"input": "work"})
	h.m.Started(5 * time.Second)
	path := filepath.Join(t.TempDir(), "release")
	command := fmt.Sprintf("while [ ! -f %q ]; do sleep 0.05; done; echo late-shell-output", path)
	h.call("shell/start", map[string]any{"command": command})
	close(gate)
	h.completed()
	state := h.call("thread/read", nil)
	shellIndex, assistantIndex := -1, -1
	for i, v := range state["items"].([]any) {
		it := v.(map[string]any)
		if it["type"] == ItemAgent {
			assistantIndex = i
		}
		if it["shell"] == true {
			shellIndex = i
			if it["status"] != "inProgress" {
				t.Fatalf("shell closed with turn: %v", it)
			}
		}
	}
	if shellIndex < 0 || assistantIndex < 0 || shellIndex >= assistantIndex {
		t.Fatalf("snapshot reordered items: shell at %d, later assistant at %d", shellIndex, assistantIndex)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	done := h.wait("item/completed", func(p map[string]any) bool { return itemOf(p)["shell"] == true })
	it := itemOf(done)
	if it["status"] != "completed" || !strings.Contains(it["output"].(string), "late-shell-output") {
		t.Fatalf("shell result: %v", it)
	}
	h.call("turn/start", map[string]any{"input": "next"})
	h.completed()
	if reqs := h.m.Requests(); !strings.Contains(reqs[len(reqs)-1], "late-shell-output") {
		t.Fatal("late shell result never reached the model")
	}
}

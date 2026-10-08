package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/tui"
	"path/filepath"
)

// remotePrompt starts /remote and follows its events from now on.
func remotePromptSetup(t *testing.T) (*App, remoteClient, <-chan rmsg) {
	t.Helper()
	a := remoteApp(t, newRemoteModel(t))
	send(t, a, "first") // a session file with entries, for /tree and /resume
	a.ui.Do(func() { a.cmdRemote("on") })
	c := a.remoteClient(t)
	eid, _ := c.must("thread/read", nil)["eventId"].(float64)
	return a, c, c.events(eid)
}

func promptOf(m rmsg) map[string]any { p, _ := m.Params["prompt"].(map[string]any); return p }

func optionLabels(p map[string]any) []string {
	var out []string
	for _, o := range p["options"].([]any) {
		out = append(out, o.(map[string]any)["label"].(string))
	}
	return out
}

// Every kind of prompt the terminal opens shows on the phone, and Esc
// from the phone closes it in the terminal.
func TestRemotePromptKinds(t *testing.T) {
	a, c, ev := remotePromptSetup(t)
	cases := []struct {
		name, kind, title string
		open              func()
		option            string
	}{
		{"model", "select", "Select model", func() { a.cmdModel("") }, "m2"},
		{"effort", "select", "Reasoning effort", func() { a.setModel(mustRef(t, a, "t/m2")); a.cmdEffort("") }, "high"},
		{"summary instructions", "input", "Custom summarization instructions", func() { a.askSummaryInstructions("x") }, ""},
		{"summarize branch", "select", "Summarize branch?", func() { a.askSummary("x") }, summaryPlain},
		{"tree", "select", "Session tree", func() { a.cmdTree("") }, "user: first"},
		{"fork", "select", "Fork from a message", func() { a.cmdFork("") }, "first"},
		{"resume", "select", "Resume a session", func() { a.cmdSessions("") }, "first"},
		{"exit", "select", "A task is still running", func() { a.exitMenu() }, "2. Run in background"},
		{"login", "select", "Select authentication method", func() { a.cmdLogin("") }, loginAPIKey},
	}
	for _, tc := range cases {
		a.ui.Do(tc.open)
		settle(a)
		read := c.must("thread/read", nil)
		pCurrent, _ := read["prompt"].(map[string]any)
		current := pCurrent["id"]
		m, _ := until(t, ev, "prompt/open", func(m rmsg) bool {
			return promptOf(m)["id"] == current && strings.Contains(promptOf(m)["title"].(string), tc.title)
		})
		p := promptOf(m)
		if p["kind"] != tc.kind {
			t.Fatalf("%s prompt %v", tc.name, p)
		}
		if tc.option != "" && !strings.Contains(strings.Join(optionLabels(p), "\n"), tc.option) {
			t.Fatalf("%s options %v", tc.name, p)
		}
		c.must("prompt/answer", map[string]any{"id": p["id"], "cancel": true})
		until(t, ev, "prompt/closed", func(m rmsg) bool { return m.Params["id"] == p["id"] })
		settle(a)
		a.ui.Do(func() { a.closeModal() })
		settle(a)
	}
	// Execution prompts are created by the runtime, not by the terminal.
	a.ui.Do(func() { a.rpcErr("client/gate", map[string]any{"open": true}) })
	settle(a)
	goalCommand(a, "ship it")
	goalCommand(a, "pause")
	goalCommand(a, "different objective")
	p := promptOf(first(t, ev, "prompt/open"))
	if p["origin"] != "goal" || !strings.Contains(p["title"].(string), "Replace goal?") {
		t.Fatalf("goal prompt %v", p)
	}
	c.must("prompt/answer", map[string]any{"id": p["id"], "cancel": true})
	settle(a)
	// Real extension select/confirm/input prompts can all be answered here.
	writeTestFile(t, filepath.Join(config.ExtensionsDir(), "demo.ts"), dialogExtension)
	typeLine(a, "/reload")
	settle(a)
	typeLine(a, "/demo now")
	for _, q := range []struct {
		title string
		ans   map[string]any
	}{{"Pick one", map[string]any{"index": 1}}, {"Sure?", map[string]any{"index": 0}}, {"Name?", map[string]any{"text": "Ada"}}} {
		m, _ := until(t, ev, "prompt/open", func(m rmsg) bool { return strings.Contains(promptOf(m)["title"].(string), q.title) })
		p := promptOf(m)
		q.ans["id"] = p["id"]
		c.must("prompt/answer", q.ans)
	}
	within(t, a, "extension answers", func() bool { return strings.Contains(bodyText(a), "picked green true Ada true") })
}

func first(t *testing.T, ev <-chan rmsg, method string) rmsg {
	t.Helper()
	m, _ := until(t, ev, method, nil)
	return m
}

func mustRef(t *testing.T, a *App, id string) config.ModelRef {
	r, ok := a.models.Find("", id)
	if !ok {
		t.Fatalf("no model %s", id)
	}
	return r
}

func TestRemotePromptAnswers(t *testing.T) {
	a, c, ev := remotePromptSetup(t)

	// Picked on the phone: applied in the terminal, which closes the picker.
	a.ui.Do(func() { a.cmdModel("") })
	p := promptOf(first(t, ev, "prompt/open"))
	labels := optionLabels(p)
	if p["filterable"] != true || labels[int(p["selected"].(float64))] != "m" {
		t.Fatalf("model picker %v", p)
	}
	i := strings.Index(strings.Join(labels, "\n")+"\n", "m2\n")
	idx := strings.Count((strings.Join(labels, "\n") + "\n")[:i], "\n")
	c.must("prompt/answer", map[string]any{"id": p["id"], "index": idx})
	if m := first(t, ev, "prompt/closed"); m.Params["id"] != p["id"] || m.Params["how"] != "answered" || m.Params["by"] == "" {
		t.Fatalf("closed %v", m.Params)
	}
	settle(a)
	var model string
	a.ui.Do(func() { model = a.model().Model.ID })
	if model != "m2" || modalOpen(a) {
		t.Fatalf("model %s, modal open %v", model, modalOpen(a))
	}

	// Opened from the phone, answered in the terminal.
	c.must("turn/start", map[string]any{"input": "/effort"})
	p = promptOf(first(t, ev, "prompt/open"))
	if p["title"] != "Reasoning effort" {
		t.Fatalf("effort prompt %v", p)
	}
	a.ui.Do(func() { a.modal.HandleInput("\x1b[B"); a.modal.HandleInput("\r") })
	if m := first(t, ev, "prompt/closed"); m.Params["id"] != p["id"] || m.Params["how"] != "answered" || m.Params["by"] != a.conn.id {
		t.Fatalf("closed in the terminal %v", m.Params)
	}
	// Too late: the first answer won.
	if m := c.call("prompt/answer", map[string]any{"id": p["id"], "index": 0}); m.Error == nil || !strings.Contains(m.Error.Message, "not open") {
		t.Fatalf("stale answer: %+v", m.Error)
	}
	if m := c.call("prompt/answer", map[string]any{"id": "nope", "cancel": true}); m.Error == nil {
		t.Fatal("unknown prompt id accepted")
	}

	// Esc in the terminal closes it on the phone.
	a.ui.Do(func() { a.cmdEffort("") })
	p = promptOf(first(t, ev, "prompt/open"))
	a.ui.Do(func() { a.modal.HandleInput("\x1b") })
	if m := first(t, ev, "prompt/closed"); m.Params["how"] != "cancelled" || m.Params["by"] != a.conn.id {
		t.Fatalf("esc in the terminal %v", m.Params)
	}

	// Answers that do not fit are refused, and the prompt stays open.
	a.ui.Do(func() { a.cmdEffort("") })
	p = promptOf(first(t, ev, "prompt/open"))
	for _, bad := range []map[string]any{
		{"id": p["id"], "index": 99},
		{"id": p["id"], "index": -1},
		{"id": p["id"], "text": "low"},
		{"id": p["id"]},
	} {
		if m := c.call("prompt/answer", bad); m.Error == nil {
			t.Fatalf("answer %v accepted", bad)
		}
	}
	if !modalOpen(a) {
		t.Fatal("a refused answer closed the picker")
	}

	// A phone that (re)connects now finds it in thread/read.
	read := c.must("thread/read", nil)
	if rp, _ := read["prompt"].(map[string]any); rp == nil || rp["id"] != p["id"] || len(rp["options"].([]any)) != len(p["options"].([]any)) {
		t.Fatalf("snapshot prompt %v", read["prompt"])
	}

	// Another modal over it: the first is closed for the phone.
	a.ui.Do(func() { a.cmdModel("") })
	if m := first(t, ev, "prompt/closed"); m.Params["id"] != p["id"] || m.Params["how"] != "closed" {
		t.Fatalf("replaced %v", m.Params)
	}
	p = promptOf(first(t, ev, "prompt/open"))
	a.ui.Do(func() { a.modal.HandleInput("\x1b") })
	first(t, ev, "prompt/closed")
	if read := c.must("thread/read", nil); read["prompt"] != nil {
		t.Fatalf("closed prompt still in the snapshot: %v", read["prompt"])
	}

	// An input: the goal's objective, edited on the phone.
	goalCommand(a, "old aim")
	goalCommand(a, "pause")
	c.must("turn/start", map[string]any{"input": "/goal edit"})
	p = promptOf(first(t, ev, "prompt/open"))
	if p["kind"] != "input" || p["text"] != "old aim" || p["placeholder"] != "Type a goal objective" {
		t.Fatalf("goal edit %v", p)
	}
	if m := c.call("prompt/answer", map[string]any{"id": p["id"], "index": 0}); m.Error == nil {
		t.Fatal("index for an input accepted")
	}
	c.must("prompt/answer", map[string]any{"id": p["id"], "text": "new\naim"})
	first(t, ev, "prompt/closed")
	settle(a)
	var obj string
	a.ui.Do(func() { obj = a.theGoal().Objective })
	if obj != "new aim" {
		t.Fatalf("objective %q", obj)
	}

	// Prompts are tracked with /remote off too: turning it on later, the
	// open one is in the first snapshot.
	a.ui.Do(func() {
		a.stopRemote()
		a.cmdEffort("")
		a.cmdRemote("on")
	})
	c = a.remoteClient(t)
	if rp, _ := c.must("thread/read", nil)["prompt"].(map[string]any); rp == nil || !strings.HasPrefix(rp["title"].(string), "Reasoning effort") {
		t.Fatalf("prompt opened before /remote: %v", rp)
	}
}

// Long pickers are capped.
func TestRemotePromptCap(t *testing.T) {
	a, _, ev := remotePromptSetup(t)
	a.ui.Do(func() {
		l := &tui.SelectList{Title: "Many", Filterable: true}
		for i := range 250 {
			l.Items = append(l.Items, tui.SelectItem{Label: fmt.Sprint("item ", i)})
		}
		l.OnCancel = a.closeModal
		a.openModal(l)
	})
	p := promptOf(first(t, ev, "prompt/open"))
	if n := len(p["options"].([]any)); n != maxPromptOptions || p["total"] != float64(250) || p["note"] == "" {
		t.Fatalf("%d options, total %v, note %v", n, p["total"], p["note"])
	}
}

// The goal reaches the phone as the status line shows it.
func TestRemoteGoal(t *testing.T) {
	a, c, ev := remotePromptSetup(t)
	if read := c.must("thread/read", nil); read["goal"] != nil {
		t.Fatalf("unexpected goal %v", read)
	}
	a.ui.Do(func() { a.rpcErr("client/gate", map[string]any{"open": true}) })
	settle(a)
	goalCommand(a, "port the parser")
	goalCommand(a, "pause")
	g := *a.theGoal()
	g.TokensUsed = 12500
	g.Seconds = 840
	fixtureGoal(t, a, &g)
	check := func(st goal.Status, want string) {
		g := *a.theGoal()
		g.Status = st
		fixtureGoal(t, a, &g)
		if st == goal.Active {
			goalCommand(a, "resume")
		}
		read := c.must("thread/read", nil)
		wire := read["goal"].(map[string]any)
		status := footer(a, 200)
		if wire["indicator"] != want || !strings.Contains(status, want) || wire["statusLabel"] != st.Label() {
			t.Fatalf("status %v footer %s", wire, status)
		}
	}
	read := c.must("thread/read", nil)
	wire := read["goal"].(map[string]any)
	if wire["objective"] != "port the parser" || wire["tokens"] != "12.5K" || wire["elapsed"] != "14m" || wire["summary"] != "Objective: port the parser Time: 14m." {
		t.Fatalf("goal %v", wire)
	}
	check(goal.Active, "Pursuing goal (14m)")
	for _, st := range []goal.Status{goal.Blocked, goal.UsageLimited, goal.Complete} {
		g.Status = st
		check(st, g.Indicator(g.Seconds, false))
	}
	read = c.must("thread/read", nil)
	eid, _ := read["eventId"].(float64)
	ev = c.events(eid)
	c.must("turn/start", map[string]any{"input": "/goal clear"})
	until(t, ev, "goal/updated", func(m rmsg) bool { return m.Params["goal"] == nil })
	if c.must("thread/read", nil)["goal"] != nil {
		t.Fatal("goal not cleared")
	}
}

// Restarting a link while a picker is open must not wrap its callbacks a
// second time or enqueue a second question. The same runtime object survives.
func TestRemoteRestartKeepsPickerAnswer(t *testing.T) {
	a, c, ev := remotePromptSetup(t)
	a.ui.Do(func() { a.cmdModel("") })
	p := promptOf(first(t, ev, "prompt/open"))
	a.ui.Do(func() { a.stopRemote(); a.startRemote(0) })
	settle(a)
	c = a.remoteClient(t)
	current := c.must("thread/read", nil)["prompt"].(map[string]any)
	if current["id"] != p["id"] {
		t.Fatalf("picker replaced: %v, was %v", current, p)
	}
	index := -1
	for i, label := range optionLabels(p) {
		if label == "m2" {
			index = i
		}
	}
	if index < 0 {
		t.Fatal("no second model")
	}
	c.must("prompt/answer", map[string]any{"id": p["id"], "index": index})
	within(t, a, "picker answer after link restart", func() bool { return a.model().Model.ID == "m2" && a.modal == nil })
	if read := c.must("thread/read", nil); read["prompt"] != nil {
		t.Fatalf("duplicate picker %v", read["prompt"])
	}
}

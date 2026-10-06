package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/extensions"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/mcp"
	"github.com/sebastianrcnt/atto/tui"
)

// remotePrompt starts /remote and follows its events from now on.
func remotePromptSetup(t *testing.T) (*App, remoteClient, <-chan rmsg) {
	t.Helper()
	a := remoteApp(t, newRemoteModel(t))
	runTurn(t, a, "first") // a session file with entries, for /tree and /resume
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

func modalOpen(a *App) bool {
	open := false
	a.ui.Do(func() { open = a.modal != nil })
	return open
}

// Every kind of prompt the terminal opens shows on the phone, and Esc
// from the phone closes it in the terminal.
func TestRemotePromptKinds(t *testing.T) {
	a, c, ev := remotePromptSetup(t)
	pausedGoal := func() { a.goal.Set(&goal.Goal{Objective: "ship it", Status: goal.Paused}) }
	cases := []struct {
		name, kind, title string
		open              func()
		option            string // one of the options, for a select
	}{
		{"model picker", "select", "Select model", func() { a.cmdModel("") }, "m2"},
		{"effort", "select", "Reasoning effort", func() { a.setModel(mustRef(t, a, "t/m2")); a.cmdEffort("") }, "high"},
		{"replace goal", "select", "Replace goal?", func() { pausedGoal(); a.cmdGoal("something else") }, "Replace current goal"},
		{"resume goal", "select", "Resume paused goal?", func() { pausedGoal(); a.promptResumeGoal() }, "Resume goal"},
		{"goal edit", "input", "Edit goal", func() { pausedGoal(); a.cmdGoal("edit") }, ""},
		{"summary instructions", "input", "Custom summarization instructions", func() { a.askSummaryInstructions("x") }, ""},
		{"summarize branch", "select", "Summarize branch?", func() { a.askSummary("x") }, summaryPlain},
		{"tree", "select", "Session tree", func() { a.cmdTree("") }, "user: first"},
		{"fork", "select", "Fork from a message", func() { a.cmdFork("") }, "first"},
		{"resume", "select", "Resume a session", func() { a.cmdResume("") }, "first"},
		{"exit menu", "select", "A task is still running", func() { a.exitMenu() }, "2. Run in background"},
		{"mcp approval", "select", "MCP server srv", func() { a.askMCPApproval(mcp.Info{Name: "srv", Target: "srv --stdio"}) }, mcpAllowAll},
		{"login method", "select", "Select authentication method", func() { a.cmdLogin("") }, loginAPIKey},
	}
	for _, tc := range cases {
		var seq int
		a.ui.Do(func() { seq = a.promptSeq; tc.open() })
		// (skipping what an earlier cancel went back to)
		m, _ := until(t, ev, "prompt/open", func(m rmsg) bool { return promptOf(m)["id"] == fmt.Sprintf("p%d", seq+1) })
		p := promptOf(m)
		if p["kind"] != tc.kind || !strings.Contains(p["title"].(string), tc.title) {
			t.Fatalf("%s: prompt %v", tc.name, p)
		}
		if tc.option != "" && !strings.Contains(strings.Join(optionLabels(p), "\n"), tc.option) {
			t.Fatalf("%s: options %v", tc.name, optionLabels(p))
		}
		id := p["id"].(string)
		if r := c.call("prompt/answer", map[string]any{"id": id, "cancel": true}); r.Error != nil {
			t.Fatalf("%s: %s", tc.name, r.Error.Message)
		}
		m, _ = until(t, ev, "prompt/closed", func(m rmsg) bool { return m.Params["id"] == id })
		if m.Params["how"] != "cancelled" || m.Params["by"] != "remote" {
			t.Fatalf("%s: closed %v", tc.name, m.Params)
		}
		a.ui.Do(func() {
			for a.modal != nil { // what a cancel went back to
				a.dismissModal()
			}
			a.goal.Set(nil)
		})
	}

	// An extension's select, confirm and input.
	h := newTUIHost(a)
	got := make(chan any, 3)
	for _, q := range []extensions.Question{
		{Kind: "select", Title: "Pick a color", Options: []string{"red", "blue"}},
		{Kind: "confirm", Title: "Proceed?"},
		{Kind: "input", Title: "Your name"},
	} {
		h.Ask("demo", q, func(v any) { got <- v })
		m, _ := until(t, ev, "prompt/open", func(m rmsg) bool { return strings.HasPrefix(promptOf(m)["title"].(string), q.Title) })
		p := promptOf(m)
		if !strings.Contains(p["title"].(string), "(demo)") {
			t.Fatalf("extension %s: %v", q.Kind, p)
		}
		switch q.Kind {
		case "select":
			c.must("prompt/answer", map[string]any{"id": p["id"], "index": 1})
		case "confirm":
			if strings.Join(optionLabels(p), ",") != "Yes,No" {
				t.Fatalf("confirm options %v", p)
			}
			c.must("prompt/answer", map[string]any{"id": p["id"], "index": 0})
		default:
			c.must("prompt/answer", map[string]any{"id": p["id"], "text": "Ada"})
		}
		if v := <-got; fmt.Sprint(v) != map[string]string{"select": "blue", "confirm": "true", "input": "Ada"}[q.Kind] {
			t.Fatalf("extension %s answered %v", q.Kind, v)
		}
		until(t, ev, "prompt/closed", func(m rmsg) bool { return m.Params["id"] == p["id"] })
	}
	if modalOpen(a) {
		t.Fatal("a modal is still open")
	}
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
	if m := first(t, ev, "prompt/closed"); m.Params["id"] != p["id"] || m.Params["how"] != "answered" || m.Params["by"] != "remote" {
		t.Fatalf("closed %v", m.Params)
	}
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
	if m := first(t, ev, "prompt/closed"); m.Params["id"] != p["id"] || m.Params["how"] != "answered" || m.Params["by"] != "terminal" {
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
	if m := first(t, ev, "prompt/closed"); m.Params["how"] != "cancelled" || m.Params["by"] != "terminal" {
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
	a.ui.Do(func() { a.goal.Set(&goal.Goal{Objective: "old aim", Status: goal.Paused}) })
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
	var obj string
	a.ui.Do(func() { obj = a.goal.Goal.Objective })
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
		t.Fatalf("no goal yet: %v", read["goal"])
	}
	a.ui.Do(func() {
		a.goal.Set(&goal.Goal{Objective: "port the parser", Status: goal.Paused, Budget: 50000, TokensUsed: 12500, Seconds: 840})
	})
	g, _ := first(t, ev, "goal/updated").Params["goal"].(map[string]any)
	var status string
	a.ui.Do(func() { status = plainLines(a.renderStatus(200)) })
	if g["indicator"] != "Goal paused (/goal resume)" || !strings.Contains(status, g["indicator"].(string)) {
		t.Fatalf("indicator %v, status line %q", g["indicator"], status)
	}
	if g["objective"] != "port the parser" || g["status"] != "paused" || g["statusLabel"] != "paused" || g["tokens"] != "12.5K / 50K" ||
		g["elapsed"] != "14m" || g["summary"] != "Objective: port the parser Time: 14m. Tokens: 12.5K/50K." {
		t.Fatalf("goal %v", g)
	}

	// Active: the status line's budget wording, on the phone too.
	a.ui.Do(func() { gg := a.goal.Goal; gg.Status = goal.Active; a.goal.Set(gg) })
	g, _ = first(t, ev, "goal/updated").Params["goal"].(map[string]any)
	a.ui.Do(func() { status = plainLines(a.renderStatus(200)) })
	if g["indicator"] != "Pursuing goal (12.5K / 50K)" || g["statusLabel"] != "active" || !strings.Contains(status, "Pursuing goal (12.5K / 50K)") {
		t.Fatalf("active goal %v, status line %q", g, status)
	}
	if read := c.must("thread/read", nil); read["goal"].(map[string]any)["indicator"] != g["indicator"] {
		t.Fatalf("snapshot goal %v", read["goal"])
	}
	// Each status as the terminal words it.
	for _, st := range []goal.Status{goal.Blocked, goal.UsageLimited, goal.BudgetLimited, goal.Complete} {
		var want string
		a.ui.Do(func() {
			gg := a.goal.Goal
			gg.Status = st
			a.goal.Set(gg)
			want = gg.Indicator(a.goal.Elapsed(), a.goal.Held())
			status = plainLines(a.renderStatus(200))
		})
		g, _ = first(t, ev, "goal/updated").Params["goal"].(map[string]any)
		if g["indicator"] != want || !strings.Contains(status, want) || g["statusLabel"] != st.Label() {
			t.Fatalf("%s: %v (want %q, status line %q)", st, g, want, status)
		}
	}

	// Cleared from the phone: null.
	c.must("turn/start", map[string]any{"input": "/goal clear"})
	if m := first(t, ev, "goal/updated"); m.Params["goal"] != nil {
		t.Fatalf("cleared: %v", m.Params)
	}
	if read := c.must("thread/read", nil); read["goal"] != nil {
		t.Fatalf("snapshot after clear %v", read["goal"])
	}
}

package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/ai"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/provider/providertest"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/tui"
	"github.com/sebastianrcnt/atto/ui"
)

func applyStatusFixture(a *App) {
	m := a.model()
	d := ui.StatusData{Model: a.models.DisplayName(m), ContextTokens: a.ctxTokens, ContextWindow: m.Model.ContextWindow,
		CompactLimit: a.info.AutoCompactLimit, CompactCap: a.info.AutoCompactCap, Long: a.info.LongContext,
		Fresh: a.usage.fresh(), Output: a.usage.output, CacheWrite: a.usage.cacheWrite,
		LastInput: a.usage.last.PromptTokens, LastCached: a.usage.last.CachedTokens,
		Path: core.ShortPath(a.cwd), Branch: a.gitBranch, Jobs: a.jobCount, Timers: a.timerCount,
		Custom: a.statusCmd, CustomLines: a.statusLines}
	if len(m.Model.Levels()) > 0 {
		d.Effort = a.effort()
	}
	if priced(m.Model) || a.usage.cost > 0 {
		d.Cost = fmt.Sprintf("$%.3f", a.usage.cost) + a.surcharge(m)
		if m.Provider.Subscription {
			d.Cost = "≈" + d.Cost
		}
	}
	if a.info.Goal != nil {
		d.Goal = tui.StripEscapes(a.goalIndicator())
		a.info.Goal.Indicator = d.Goal
	}
	trees := ui.StatusTrees(d)
	snapshot := &ui.Snapshot{Version: 1}
	for index, id := range ui.StatusIDs {
		snapshot.Instances = append(snapshot.Instances, ui.Instance{Site: ui.Status, ID: "atto/" + id, Rev: int64(index + 1), Tree: trees[id]})
	}
	a.view.Info.UI = snapshot
	a.applyUI(snapshot)
}

// Reference files are generated only by the frozen main renderer, never from
// renderUIStatus. JSON preserves exact SGR bytes and trailing padding; text is
// the same screenshot with SGR stripped for review.
func mainStatusGolden(t *testing.T, name string, width int, want []string) {
	t.Helper()
	path := filepath.Join("testdata", fmt.Sprintf("status-main-%s-%d.json", name, width))
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		b, _ := json.MarshalIndent(want, "", "  ")
		if err := os.WriteFile(path, append(b, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(strings.TrimSuffix(path, ".json")+".txt", []byte(plainLines(want)+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved []string
	if err = json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(saved, want) {
		t.Fatalf("main reference changed: %s\n%q\nwant %q", path, want, saved)
	}
}

func TestStatusMainParity(t *testing.T) {
	oldRSS := rssBytes.Load()
	rssBytes.Store(31 << 20)
	defer rssBytes.Store(oldRSS)
	for _, state := range []string{"idle", "busy", "jobs", "goal", "all", "custom", "custom-all", "custom-empty", "warning", "no-usage", "output-only", "max-effort", "long-context", "priced", "compact-cap", "timers", "goal-paused", "goal-held", "custom-long"} {
		t.Run(state, func(t *testing.T) {
			a := statusApp(t, nil)
			setTestModel(a, config.ModelRef{Model: config.Model{ID: "m", Name: "Orca Local", ContextWindow: 262000, Efforts: []string{"off", "low", "medium", "high", "max"}}})
			a.cwd = "/work/scratchpad/uiv/w"
			a.info.Effort = "low"
			a.ctxTokens = 2300
			a.usage = usageStats{}
			a.usage.add(provider.Usage{PromptTokens: 70000, CachedTokens: 67900, CompletionTokens: 107})
			if state == "busy" || state == "all" || state == "custom-all" {
				a.busy = true
				a.activity = "Thinking"
			}
			if state == "jobs" || state == "all" || state == "custom-all" {
				a.jobCount = 1
			}
			if state == "goal" || state == "all" || state == "custom-all" {
				a.info.Goal = &server.GoalInfo{Seconds: 10, Goal: &goal.Goal{Objective: "ship it", Status: goal.Active}}
			}
			if strings.HasPrefix(state, "custom") {
				a.statusCmd = true
				if state != "custom-empty" {
					a.statusLines = []string{"custom status line", tui.FG(2, "second line")}
				}
			}
			switch state {
			case "warning":
				a.ctxTokens = 240000
			case "no-usage":
				a.usage = usageStats{}
			case "output-only":
				a.usage = usageStats{output: 107}
			case "max-effort":
				a.info.Effort = "max"
			case "long-context":
				a.info.LongContext = true
				a.ctxTokens = 21000
			case "priced":
				ref := a.model()
				ref.Provider.Subscription = true
				ref.Model.Cost = &ai.ModelCost{Output: 1}
				setTestModel(a, ref)
				a.usage.cost = .123
				a.usage.cacheWrite = 2000
			case "compact-cap":
				a.info.AutoCompactCap = 100000
				a.info.AutoCompactLimit = 90000
				a.ctxTokens = 81000
			case "timers":
				a.timerCount = 2
				a.jobCount = 3
			case "goal-paused":
				a.info.Goal = &server.GoalInfo{Goal: &goal.Goal{Status: goal.Paused}}
			case "goal-held":
				a.info.Goal = &server.GoalInfo{Held: true, Goal: &goal.Goal{Status: goal.Active}}
			case "custom-long":
				a.statusLines = []string{strings.Repeat("custom ", 40), "second"}
			}
			// Capture BEFORE enabling UI so the reference cannot accidentally route
			// through the new renderer. Store every styled byte, not just cell text.
			wants := map[int][]string{}
			for _, width := range []int{40, 80, 100, 120, 160} {
				want := a.renderMainStatus(width)
				mainStatusGolden(t, state, width, want)
				wants[width] = want
			}
			applyStatusFixture(a)
			for _, width := range []int{40, 80, 100, 120, 160} {
				got, want := a.renderStatus(width), wants[width]
				if !slices.Equal(got, want) {
					t.Fatalf("width %d UI != main\nUI: %q\nmain: %q", width, got, want)
				}
			}
		})
	}
}

func TestStatusGoalClockIsLocalAndRSSIsNotWorkerHeap(t *testing.T) {
	oldRSS := rssBytes.Load()
	defer rssBytes.Store(oldRSS)
	a := statusApp(t, nil)
	a.busy = true
	a.info.Goal = &server.GoalInfo{Seconds: 0, TurnStartedAt: time.Now().Add(-10 * time.Second).UnixMilli(), Goal: &goal.Goal{Status: goal.Active}, Indicator: "Pursuing goal (0s)"}
	applyStatusFixture(a)
	// Simulate a shared drawing from turn start, followed by a quiet running turn.
	m := ui.Match{Site: ui.Status, ID: "atto/goal"}
	old := ui.Text(ui.TextProps{Text: "Pursuing goal (0s)", Color: ui.Accent})
	for i := range a.view.Info.UI.Instances {
		if a.view.Info.UI.Instances[i].ID == m.ID {
			a.view.Info.UI.Instances[i].Tree = &old
			a.view.Info.UI.Instances[i].Rev += 100
		}
	}
	a.info.Goal.Indicator = "Pursuing goal (0s)"
	a.applyUI(a.view.Info.UI)
	rssBytes.Store(31 << 20)
	out := plainLines(a.renderStatus(100))
	if !strings.Contains(out, "Pursuing goal (10s)") || !strings.Contains(out, "31MB") {
		t.Fatal(out)
	}
}

func TestLivePortableStatusMatchesMain(t *testing.T) {
	gate := make(chan struct{})
	a, _ := liveApp(t, providertest.Reply{Text: "answer", Prompt: 70000, Cached: 67900, Completion: 107}, providertest.Reply{Text: "next", Gate: gate})
	defer close(gate)
	oldRSS := rssBytes.Load()
	rssBytes.Store(31 << 20)
	defer rssBytes.Store(oldRSS)
	check := func(label string) {
		within(t, a, label, func() bool {
			for _, width := range []int{40, 80, 100, 120, 160} {
				if !slices.Equal(a.renderStatus(width), a.renderMainStatus(width)) {
					return false
				}
			}
			return true
		})
	}
	check("initial idle native/UI parity")
	typeLine(a, "hello")
	waitIdle(t, a)
	check("usage and cache native/UI parity")
	typeLine(a, "again")
	within(t, a, "busy", func() bool { return a.busy })
	check("busy native/UI parity without duplicate activity")
	j, err := jobs.StartWorkerTurn(a.threadID, a.cwd, "sleep 60", os.Getpid(), jobs.Control{File: filepath.Join(t.TempDir(), "stop"), Content: "stop"}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = jobs.Kill(a.threadID, j.ID) })
	within(t, a, "running job status", func() bool { return a.jobCount == 1 })
	check("busy plus job native/UI parity")
	goalCommand(a, "ship it")
	check("busy plus job plus active goal native/UI parity")
	goalCommand(a, "pause")
	check("paused goal native/UI parity")
	if err := config.UpdateSettings(map[string]any{"statusLine": config.StatusLine{Command: "echo custom-status", RefreshInterval: 1}}); err != nil {
		t.Fatal(err)
	}
	a.ui.Do(func() { a.statusCmd = true; a.statusLines = []string{"custom-status"} })
	check("worker custom status native/UI parity")
}

func TestPortableStatusRejectsInvalidTreesAndHandlesRemovedModel(t *testing.T) {
	a := statusApp(t, nil)
	invalid := ui.Text(ui.TextProps{Text: "unvalidated content"})
	invalid.Props["notAProp"] = true
	snap := &ui.Snapshot{Version: 1, Instances: []ui.Instance{{Site: ui.Status, ID: "atto/model", Rev: 1, Tree: &invalid}}}
	a.view.Info.UI = snap
	a.applyUI(snap)
	for width := 1; width <= 40; width++ {
		got := a.renderStatus(width)
		if strings.Contains(plainLines(got), "unvalidated content") {
			t.Fatal("bypassed frontend validation")
		}
		for _, line := range got {
			if tui.VisibleWidth(line) > width {
				t.Fatal("removed model caused overflow")
			}
		}
	}
}

func TestAdditionalGoStatusIsNotDiscarded(t *testing.T) {
	a := statusApp(t, nil)
	applyStatusFixture(a)
	n := ui.Text(ui.TextProps{Text: "Additional Go status"})
	a.view.Info.UI.Instances = append(a.view.Info.UI.Instances, ui.Instance{Site: ui.Status, ID: "atto/diagnostics", Rev: 100, Tree: &n})
	a.applyUI(a.view.Info.UI)
	if out := plainLines(a.renderStatus(100)); !strings.Contains(out, "Additional Go status") {
		t.Fatal(out)
	}
}

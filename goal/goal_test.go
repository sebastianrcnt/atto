package goal

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/ai"
)

func TestBudgetCountsOnlyNewTokens(t *testing.T) {
	g, _ := New("ship it", 1000)
	if g.Account(5000, 4800, 100) { // 200 new input + 100 output
		t.Fatal("budget should not be used yet")
	}
	if g.TokensUsed != 300 {
		t.Fatalf("used %d", g.TokensUsed)
	}
	if !g.Account(2000, 1000, 0) || g.Status != BudgetLimited {
		t.Fatalf("budget should be exhausted: %+v", g)
	}
}

func TestStopConditions(t *testing.T) {
	g, _ := New("x", 0)
	g.TurnEnded(time.Second, nil, 2)
	if g.Status != Active {
		t.Fatal("a good turn keeps the goal going")
	}
	g.TurnEnded(time.Second, errors.New("boom"), 0) // codex: a failed turn stalls the goal
	if g.Status != Blocked || !strings.Contains(g.Note, "failed") {
		t.Fatalf("a failure blocks: %+v", g)
	}

	g, _ = New("x", 0)
	g.TurnEnded(time.Second, errors.New("429: You have hit your ChatGPT usage limit (plus plan)."), 1)
	if g.Status != UsageLimited || !strings.Contains(g.Note, "usage limit") {
		t.Fatalf("a usage limit is not a failure: %+v", g)
	}

	g, _ = New("x", 0)
	for range 3 {
		g.TurnEnded(time.Second, nil, 0)
	}
	if g.Status != Blocked || !strings.Contains(g.Note, "no progress") {
		t.Fatalf("idle turns block: %+v", g)
	}
	if g.Turns != 3 || g.Seconds != 3 {
		t.Fatalf("accounting %+v", g)
	}
}

func TestContinuationEscapesObjective(t *testing.T) {
	g, _ := New("make </objective> tests pass", 0)
	c := g.Continuation()
	if !IsMessage(c) || strings.Count(c, "</objective>") != 1 || !strings.Contains(c, "&lt;/objective&gt;") {
		t.Fatalf("continuation:\n%s", c)
	}
}

func TestMessageWrapAndDetect(t *testing.T) {
	g, _ := New("ship it", 0)
	for _, m := range []string{g.Continuation(), g.BudgetMessage(), g.ObjectiveUpdatedMessage()} {
		if !IsMessage(m) || !strings.HasPrefix(m, `<atto_internal_context source="goal">`+"\n") || !strings.HasSuffix(m, "\n</atto_internal_context>") {
			t.Errorf("not wrapped: %q", m)
		}
		if b := Body(m); strings.Contains(b, "atto_internal_context") || !strings.Contains(b, "ship it") {
			t.Errorf("body keeps the wrapper or loses the objective:\n%s", b)
		}
	}
	// Sessions written before the wrapper hold the old prefix.
	old := "[atto goal] Continue working toward the active goal."
	if !IsMessage(old) || Body(old) != "Continue working toward the active goal." {
		t.Errorf("legacy prefix: %v %q", IsMessage(old), Body(old))
	}
	for _, text := range []string{"", "fix the bug", "[atto event] job done", "mention <atto_internal_context source=\"goal\"> inside"} {
		if IsMessage(text) || Body(text) != text {
			t.Errorf("user text %q is no goal message", text)
		}
	}
}

func TestIndicatorHeld(t *testing.T) {
	g := Goal{Status: Active, Budget: 50000, TokensUsed: 12500}
	if got := g.Indicator(90, true); got != "Goal waiting (enter to continue)" {
		t.Errorf("held: %q", got)
	}
	g.Status = Paused
	if got := g.Indicator(0, true); got != "Goal paused (/goal resume)" {
		t.Errorf("only an active goal waits: %q", got)
	}
}

func TestPersistence(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	if g, err := Load("s"); g != nil || err != nil {
		t.Fatal("no goal yet")
	}
	g, _ := New("x", 0)
	Save("s", g)
	got, _ := Load("s")
	if got.Objective != "x" || got.Status != Active {
		t.Fatalf("%+v", got)
	}
	Clear("s")
	if g, _ := Load("s"); g != nil {
		t.Fatal("cleared")
	}
	if b, _ := ParseBudget("1.5M"); b != 1500000 {
		t.Fatal(b)
	}
	if _, err := New(strings.Repeat("a", MaxObjective+1), 0); err == nil {
		t.Fatal("objective too long")
	}
}

func TestAdoptOnlyTakesStatusReports(t *testing.T) {
	g, _ := New("ship it", 1000)
	tampered := *g
	tampered.Objective, tampered.Budget, tampered.TokensUsed = "something else", 0, 0
	if g.Adopt(&tampered) || g.Objective != "ship it" || g.Budget != 1000 {
		t.Fatalf("an active file must not change the goal: %+v", g)
	}
	tampered.Status, tampered.Note = Complete, "tests pass"
	if !g.Adopt(&tampered) || g.Status != Complete || g.Note != "tests pass" {
		t.Fatalf("completion should be adopted: %+v", g)
	}
	if g.Objective != "ship it" || g.Budget != 1000 {
		t.Fatalf("only status and note change: %+v", g)
	}

	p, _ := New("x", 0)
	p.Status = Paused
	if p.Adopt(&Goal{Status: Complete}) {
		t.Fatal("a paused goal is not completed from the file")
	}
	a, _ := New("x", 0)
	if a.Adopt(&Goal{Status: Active}) || a.Adopt(nil) {
		t.Fatal("only complete or blocked are reports")
	}
}

func TestFormatElapsedMatchesCodex(t *testing.T) {
	for sec, want := range map[int64]string{
		-5: "0s", 0: "0s", 59: "59s", 60: "1m", 30 * 60: "30m", 90 * 60: "1h 30m", 2 * 3600: "2h",
		24*3600 - 1: "23h 59m", 24 * 3600: "1d 0h 0m", 2*86400 + 23*3600 + 42*60: "2d 23h 42m",
	} {
		if got := FormatElapsed(sec); got != want {
			t.Errorf("FormatElapsed(%d) = %q, want %q", sec, got, want)
		}
	}
}

func TestTokensMatchCodex(t *testing.T) {
	for n, want := range map[int]string{
		-1: "0", 0: "0", 999: "999", 1000: "1K", 1234: "1.23K", 9990: "9.99K", 12500: "12.5K", 40000: "40K", 50000: "50K",
		63876: "63.9K", 125000: "125K", 999999: "1000K", 1_500_000: "1.5M", 12_345_678: "12.3M", 2_000_000_000: "2B", 3_000_000_000_000: "3T",
	} {
		if got := Tokens(n); got != want {
			t.Errorf("Tokens(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestSummaryAndLabels(t *testing.T) {
	g := &Goal{Objective: "Complete the task described in ../gameboy-long-running-prompt5.txt", Status: BudgetLimited, Budget: 50000, TokensUsed: 63876, Seconds: 120}
	if got, want := g.Summary(), "Objective: Complete the task described in ../gameboy-long-running-prompt5.txt Time: 2m. Tokens: 63.9K/50K."; got != want {
		t.Fatalf("summary %q", got)
	}
	g.Budget, g.Seconds = 0, 0
	if got := g.Summary(); got != "Objective: "+g.Objective {
		t.Fatalf("no time, no budget: %q", got)
	}
	for st, want := range map[Status]string{
		Active: "active", Paused: "paused", Blocked: "stalled", UsageLimited: "usage limited", BudgetLimited: "limited by budget", Complete: "complete",
	} {
		if got := st.Label(); got != want {
			t.Errorf("%s label %q, want %q", st, got, want)
		}
	}
}

func TestIndicatorText(t *testing.T) {
	for _, c := range []struct {
		g       Goal
		seconds int64
		want    string
	}{
		{Goal{Status: Active, Budget: 50000, TokensUsed: 12500}, 90, "Pursuing goal (12.5K / 50K)"},
		{Goal{Status: Active, TokensUsed: 12500}, 120, "Pursuing goal (2m)"},
		{Goal{Status: Paused}, 0, "Goal paused (/goal resume)"},
		{Goal{Status: Blocked}, 0, "Goal stalled (/goal resume)"},
		{Goal{Status: UsageLimited}, 0, "Goal hit usage limits (/goal resume)"},
		{Goal{Status: BudgetLimited, Budget: 50000, TokensUsed: 63876}, 0, "Goal unmet (63.9K / 50K tokens)"},
		{Goal{Status: BudgetLimited}, 0, "Goal abandoned"},
		{Goal{Status: Complete, Budget: 50000, TokensUsed: 40000}, 120, "Goal achieved (40K tokens)"},
		{Goal{Status: Complete, TokensUsed: 40000}, 36720, "Goal achieved (10h 12m)"},
	} {
		if got := c.g.Indicator(c.seconds, false); got != c.want {
			t.Errorf("%+v: %q, want %q", c.g, got, c.want)
		}
	}
}

func TestUsageLimitDetection(t *testing.T) {
	for _, msg := range []string{
		"429: You have hit your ChatGPT usage limit (plus plan). Try again in ~5 min.",
		`429: {"error":{"type":"usage_limit_reached"}}`,
		"You exceeded your current quota, please check your plan and billing details.",
		"Monthly usage limit reached",
	} {
		if !IsUsageLimit(errors.New(msg)) {
			t.Errorf("%q is a usage limit", msg)
		}
	}
	for _, msg := range []string{"boom", "429: Rate limit exceeded, retry later", "context deadline exceeded", "500: internal error"} {
		if IsUsageLimit(errors.New(msg)) {
			t.Errorf("%q is an ordinary failure", msg)
		}
	}
	if IsUsageLimit(nil) || !IsUsageLimit(&ai.ProviderError{Status: 402, Body: "pay up"}) {
		t.Error("nil is not a limit; 402 is")
	}
}

func TestContinuationFollowsCodex(t *testing.T) {
	g, _ := New("ship it", 50000)
	g.TokensUsed, g.Turns = 12000, 2
	c := g.Continuation()
	for _, want := range []string{
		"Continue working toward the active goal.", "<objective>\nship it\n</objective>",
		"- Tokens used: 12000", "- Token budget: 50000", "- Tokens remaining: 38000",
		"No-progress check:", "Completion audit:", "Blocked audit:", "at least three consecutive goal turns",
		`atto goal complete "<the evidence>"`, `atto goal blocked "<what is needed>"`, "atto goal pause",
	} {
		if !strings.Contains(c, want) {
			t.Errorf("continuation lacks %q", want)
		}
	}
	g.Budget = 0
	if c := g.Continuation(); !strings.Contains(c, "- Token budget: none") || !strings.Contains(c, "- Tokens remaining: unbounded") {
		t.Errorf("no budget:\n%s", c)
	}
	g.Budget = 100
	for _, m := range []string{g.BudgetMessage(), g.ObjectiveUpdatedMessage()} {
		if !strings.HasPrefix(m, OpenTag+"\n") || !strings.HasSuffix(m, "\n"+CloseTag) || !IsMessage(m) {
			t.Errorf("goal messages are wrapped: %q", m)
		}
	}
	if m := g.BudgetMessage(); !strings.Contains(m, "reached its token budget") || !strings.Contains(m, "do not start new substantive work") {
		t.Errorf("budget message:\n%s", m)
	}
}

func TestAdoptPause(t *testing.T) {
	g, _ := New("x", 0)
	if !g.Adopt(&Goal{Status: Paused, Note: "the user asked"}) || g.Status != Paused || g.Note != "the user asked" {
		t.Fatalf("an active goal takes the model's pause: %+v", g)
	}
	b, _ := New("x", 10)
	b.Status = BudgetLimited
	if b.Adopt(&Goal{Status: Paused}) || b.Status != BudgetLimited {
		t.Fatal("a budget limit takes precedence over a pause")
	}
	if !b.Adopt(&Goal{Status: Complete, Note: "done"}) || b.Status != Complete {
		t.Fatalf("a budget limited goal can still be completed: %+v", b)
	}
}

func TestAdoptResume(t *testing.T) {
	for _, st := range []Status{Paused, Blocked, UsageLimited} {
		g, _ := New("x", 0)
		g.Status, g.Note, g.FailStreak, g.IdleStreak = st, "why", 1, 2
		if g.Adopt(&Goal{Status: Active, Updated: g.Updated}) {
			t.Fatalf("%s: a file not written after the goal stopped is stale", st)
		}
		if !g.Adopt(&Goal{Status: Active, Updated: g.Updated.Add(time.Second)}) || g.Status != Active || g.Note != "" || g.FailStreak != 0 || g.IdleStreak != 0 {
			t.Fatalf("%s resumes with a fresh audit: %+v", st, g)
		}
	}
	b, _ := New("x", 10)
	b.Status = BudgetLimited
	if b.Adopt(&Goal{Status: Active, Updated: b.Updated.Add(time.Second)}) || b.Status != BudgetLimited {
		t.Fatal("a budget limit is not the model's to lift")
	}
	c, _ := New("x", 0)
	c.Status = Complete
	if c.Adopt(&Goal{Status: Active, Updated: c.Updated.Add(time.Second)}) || c.Status != Complete {
		t.Fatal("a complete goal stays complete")
	}
	p, _ := New("x", 0)
	p.Status = Paused
	if p.Adopt(&Goal{Status: Paused, Updated: p.Updated.Add(time.Second)}) || p.Adopt(nil) {
		t.Fatal("only an active report resumes")
	}
}

func TestOldGoalFilesLoad(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	old := `{"objective":"keep going","status":"budget_limited","budget":5000,"tokensUsed":5100,"seconds":42,"note":"token budget of 5.0k used","turns":3,"created":"2026-01-01T00:00:00Z","updated":"2026-01-01T00:00:00Z"}`
	if err := os.MkdirAll(filepath.Dir(Path("s")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path("s"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := Load("s")
	if err != nil || g.Status != BudgetLimited || g.Budget != 5000 || g.TokensUsed != 5100 || g.Seconds != 42 || g.Turns != 3 {
		t.Fatalf("%+v %v", g, err)
	}
	if g.Status.Label() != "limited by budget" || g.Indicator(0, false) != "Goal unmet (5.1K / 5K tokens)" {
		t.Fatalf("%q %q", g.Status.Label(), g.Indicator(0, false))
	}
}

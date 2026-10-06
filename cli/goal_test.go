package cli

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/goal"
)

func TestGoalCommandInsideAgent(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	t.Setenv("ATTO_SESSION_ID", "s1")
	t.Setenv(config.EnvAgent, "1")

	// As codex's create_goal: a model sets a goal (when the user asks)
	// but never replaces an unfinished one.
	if err := RunGoal([]string{"set", "-budget", "50k", "port the parser"}, io.Discard); err != nil {
		t.Fatalf("set inside an agent: %v", err)
	}
	if got, _ := goal.Load("s1"); got == nil || got.Objective != "port the parser" || got.Budget != 50_000 || got.Status != goal.Active {
		t.Fatalf("set: %+v", got)
	}
	if err := RunGoal([]string{"set", "rewrite everything"}, io.Discard); err == nil || !strings.Contains(err.Error(), "only the user can replace") {
		t.Fatalf("replace inside an agent: %v", err)
	}
	g, _ := goal.New("ship it", 0)
	_ = goal.Save("s2", g)
	if err := RunGoal([]string{"complete", "-session", "s2", "done"}, io.Discard); err == nil {
		t.Fatal("another session's goal must be off limits")
	}
	_ = goal.Save("s1", g)
	if err := RunGoal([]string{"complete", "tests pass"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got, _ := goal.Load("s1"); got.Status != goal.Complete || got.Note != "tests pass" {
		t.Fatalf("report not written: %+v", got)
	}
}

func TestGoalResumeReport(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	t.Setenv("ATTO_SESSION_ID", "s1")
	t.Setenv(config.EnvAgent, "1")
	if err := RunGoal([]string{"resume", "the user asked"}, io.Discard); err == nil || !strings.Contains(err.Error(), "no goal") {
		t.Fatalf("no goal: %v", err)
	}
	g, _ := goal.New("ship it", 0)
	_ = goal.Save("s1", g)
	if err := RunGoal([]string{"resume", "x"}, io.Discard); err == nil || !strings.Contains(err.Error(), "active, not paused") {
		t.Fatalf("an active goal does not resume: %v", err)
	}
	for _, st := range []goal.Status{goal.Paused, goal.Blocked, goal.UsageLimited} {
		g.Status, g.Note, g.FailStreak, g.IdleStreak = st, "why", 1, 2
		_ = goal.Save("s1", g)
		if err := RunGoal([]string{"resume"}, io.Discard); err == nil {
			t.Fatal("a reason is required")
		}
		var out strings.Builder
		if err := RunGoal([]string{"resume", "the user asked"}, &out); err != nil {
			t.Fatalf("%s: %v", st, err)
		}
		if got, _ := goal.Load("s1"); got.Status != goal.Active || got.Note != "" || got.FailStreak != 0 || got.IdleStreak != 0 || !strings.Contains(out.String(), "resumed") {
			t.Fatalf("%s: %+v %q", st, got, out.String())
		}
	}
	for _, st := range []goal.Status{goal.BudgetLimited, goal.Complete} {
		g.Status = st
		_ = goal.Save("s1", g)
		if err := RunGoal([]string{"resume", "x"}, io.Discard); err == nil {
			t.Fatalf("a %s goal does not resume", st)
		}
		if got, _ := goal.Load("s1"); got.Status != st {
			t.Fatalf("refusal changed the goal: %+v", got)
		}
	}
}

func TestGoalPauseReport(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	t.Setenv("ATTO_SESSION_ID", "s1")
	t.Setenv(config.EnvAgent, "1")
	g, _ := goal.New("ship it", 0)
	_ = goal.Save("s1", g)
	if err := RunGoal([]string{"pause"}, io.Discard); err == nil {
		t.Fatal("a reason is required")
	}
	var out strings.Builder
	if err := RunGoal([]string{"pause", "the user asked"}, &out); err != nil {
		t.Fatal(err)
	}
	if got, _ := goal.Load("s1"); got.Status != goal.Paused || got.Note != "the user asked" || !strings.Contains(out.String(), "paused") {
		t.Fatalf("%+v %q", got, out.String())
	}
	if err := RunGoal([]string{"pause", "again"}, io.Discard); err == nil || !strings.Contains(err.Error(), "paused, not active") {
		t.Fatalf("only an active goal pauses: %v", err)
	}
	g.Status = goal.BudgetLimited
	_ = goal.Save("s1", g)
	if err := RunGoal([]string{"pause", "x"}, io.Discard); err == nil {
		t.Fatal("a budget limit takes precedence over a pause")
	}
	if err := RunGoal([]string{"complete", "done"}, io.Discard); err != nil {
		t.Fatalf("a budget limited goal can still complete: %v", err)
	}
	var show strings.Builder
	if err := RunGoal([]string{"show"}, &show); err != nil || !strings.Contains(show.String(), "status: complete") {
		t.Fatalf("%v %q", err, show.String())
	}
}

// goalServer serves plain text answers, or the given status from the
// failFrom-th request on.
func goalServer(t *testing.T, failFrom, status int, body string) func() []string {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		n := len(bodies)
		mu.Unlock()
		if failFrom > 0 && n >= failFrom {
			w.WriteHeader(status)
			io.WriteString(w, body)
			return
		}
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"thinking about it\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	t.Setenv("ATTO_DIR", dir)
	models := `{"providers":{"fake":{"baseUrl":"` + srv.URL + `","models":[{"id":"m","contextWindow":10000}]}}}`
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(models), 0o644); err != nil {
		t.Fatal(err)
	}
	return func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), bodies...) }
}

// printGoal runs -p -goal with JSON output and returns the result.
func printGoal(t *testing.T, o PrintOptions) printResult {
	t.Helper()
	t.Chdir(t.TempDir())
	quiet(t)
	out, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	os.Stdout = out
	o.Format, o.NoSave = "json", true
	if err := RunPrint(o); !errors.Is(err, ErrPrintFailed) {
		t.Fatalf("a goal that did not complete fails the run: %v", err)
	}
	data, _ := os.ReadFile(out.Name())
	var res printResult
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatalf("%v: %s", err, data)
	}
	return res
}

func TestRunPrintGoalStalls(t *testing.T) {
	bodies := goalServer(t, 0, 0, "")
	res := printGoal(t, PrintOptions{Goal: "ship it"})
	if res.GoalStatus != "blocked" || !strings.Contains(res.GoalNote, "no progress") || !strings.Contains(res.Error, "goal stalled") {
		t.Fatalf("turns without work stall the goal: %+v", res)
	}
	b := bodies()
	if len(b) != 3 || !strings.Contains(b[1], "Continue working toward the active goal.") || !strings.Contains(b[1], "atto goal complete") {
		t.Fatalf("%d requests: %v", len(b), b)
	}
}

func TestRunPrintGoalUsageLimited(t *testing.T) {
	bodies := goalServer(t, 1, 429, `{"error":{"type":"usage_limit_reached","message":"You have hit your usage limit."}}`)
	res := printGoal(t, PrintOptions{Goal: "ship it", GoalBudget: "50k"})
	if res.GoalStatus != "usage_limited" || !strings.Contains(res.Error, "usage limit") {
		t.Fatalf("%+v", res)
	}
	if n := len(bodies()); n != 1 {
		t.Fatalf("a usage limit stops the loop: %d requests", n)
	}
}

// "atto goal status" is bare "atto goal": models reach for it.
func TestGoalStatusIsShow(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	t.Setenv("ATTO_SESSION_ID", "s1")
	t.Setenv(config.EnvAgent, "1")
	for _, args := range [][]string{nil, {"status"}} {
		var out strings.Builder
		if err := RunGoal(args, &out); err != nil || strings.TrimSpace(out.String()) != "no goal" {
			t.Fatalf("%v without a goal: %v %q", args, err, out.String())
		}
	}
	g, _ := goal.New("ship it", 100)
	g.TokensUsed = 30
	_ = goal.Save("s1", g)
	var bare, status strings.Builder
	if err := RunGoal(nil, &bare); err != nil {
		t.Fatal(err)
	}
	if err := RunGoal([]string{"status"}, &status); err != nil || status.String() != bare.String() || !strings.Contains(status.String(), "ship it") {
		t.Fatalf("status differs from bare: %v %q vs %q", err, status.String(), bare.String())
	}
	if !strings.Contains(goalUsage, "atto goal [status]") {
		t.Fatal("usage lists status")
	}
}

func TestGoalSetRefusesCommandWords(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	t.Setenv("ATTO_SESSION_ID", "s1")
	t.Setenv(config.EnvAgent, "1")
	for _, w := range []string{"help", "status", "budget"} {
		if err := RunGoal([]string{"set", w}, io.Discard); err == nil || !strings.Contains(err.Error(), "not an objective") {
			t.Fatalf("set %s: %v", w, err)
		}
	}
	if g, _ := goal.Load("s1"); g != nil {
		t.Fatalf("no goal from a command word: %+v", g)
	}
}

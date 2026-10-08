// Package goal keeps one persistent objective per session that drives work
// across turns, after codex-rs's goals: while a goal is active, every time
// the agent goes idle atto starts another turn with a continuation prompt,
// until the model reports the goal complete or blocked, or a stop condition
// trips.
//
// The goal lives in a file (~/.atto/goals/<session>.json) so the model can
// update it from its shell (`atto goal complete "<evidence>"`) and the
// front end picks the change up; snapshots also go into the session file
// so a resumed session gets its goal back.
package goal

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/ai"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/fsutil"
	"github.com/sebastianrcnt/atto/prompts"
)

type Status string

const (
	Active       Status = "active"
	Paused       Status = "paused"        // by the user, or after an interrupt
	Blocked      Status = "blocked"       // reported by the model, or a stop condition (codex: stalled)
	UsageLimited Status = "usage_limited" // the provider's usage limit stopped a turn
	Complete     Status = "complete"      // reported by the model

	// legacyBudgetLimited was the status of a goal whose token budget ran
	// out, before budgets were dropped; such a goal loads as paused.
	legacyBudgetLimited Status = "budget_limited"
)

// NoteInterrupted is the note of a goal paused by an interrupt (Esc).
const NoteInterrupted = "interrupted"

// LongContextNotice warns when a goal runs without the price-tier cap.
const LongContextNotice = "Long context is on; the price-tier cap is off for this goal (/context normal to restore it)."

// MaxObjective bounds the objective (codex: 4,000 characters).
const MaxObjective = 4000

// Stop conditions, as in codex: a turn that fails blocks the goal (a usage
// limit stops it as usage limited), and turns that make no progress block
// it after three in a row, so the loop cannot spin.
const (
	maxFailStreak = 1
	maxIdleStreak = 3
)

type Goal struct {
	Objective  string    `json:"objective"`
	Status     Status    `json:"status"`
	TokensUsed int       `json:"tokensUsed"`
	Seconds    int64     `json:"seconds"`
	Note       string    `json:"note,omitempty"` // completion evidence or block reason
	Turns      int       `json:"turns"`
	FailStreak int       `json:"failStreak,omitempty"`
	IdleStreak int       `json:"idleStreak,omitempty"`
	Created    time.Time `json:"created"`
	Updated    time.Time `json:"updated"`
}

// UnmarshalJSON reads a goal, taking one saved with a token budget (the
// "budget" field is ignored) that ran out as paused, so the user can resume
// it.
func (g *Goal) UnmarshalJSON(data []byte) error {
	type plain Goal
	if err := json.Unmarshal(data, (*plain)(g)); err != nil {
		return err
	}
	if g.Status == legacyBudgetLimited {
		g.Status, g.Note = Paused, ""
	}
	return nil
}

func Path(session string) string { return filepath.Join(config.Dir(), "goals", session+".json") }

// Load returns the session's goal, or nil if it has none.
func Load(session string) (*Goal, error) {
	data, err := os.ReadFile(Path(session))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var g Goal
	return &g, json.Unmarshal(data, &g)
}

// Save writes the goal atomically.
func Save(session string, g *Goal) error {
	g.Updated = time.Now()
	data, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	p := Path(session)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return fsutil.WriteAtomic(p, data, 0o644)
}

// Clear removes the session's goal.
func Clear(session string) error {
	err := os.Remove(Path(session))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// reserved are the words /goal and atto goal take as subcommands: a goal
// whose whole objective is one of them is a typo, not a task.
var reserved = map[string]bool{
	"help": true, "status": true, "show": true,
	"clear": true, "edit": true, "pause": true, "resume": true,
}

// Reserved reports whether text is just a subcommand word (any case).
func Reserved(text string) bool { return reserved[strings.ToLower(strings.TrimSpace(text))] }

// New creates an active goal.
func New(objective string) (*Goal, error) {
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return nil, fmt.Errorf("the goal needs an objective")
	}
	if Reserved(objective) {
		return nil, fmt.Errorf("%q is a goal command, not an objective", objective)
	}
	if len(objective) > MaxObjective {
		return nil, fmt.Errorf("objective is %d characters; keep it under %d (put details in a file and point to it)", len(objective), MaxObjective)
	}
	now := time.Now()
	return &Goal{Objective: objective, Status: Active, Created: now, Updated: now}, nil
}

// Account adds a model call's usage. Only new tokens count: cached input
// is re-sent prefix.
func (g *Goal) Account(input, cached, output int) {
	g.TokensUsed += max(0, input-cached) + output
}

// Adopt takes the model's status report from the goal file (atto goal
// complete|blocked|pause|resume). The front end keeps the goal in memory and
// accepts only that transition, so editing the file cannot change the
// objective or the usage. Returns true if the status changed.
//
// Complete, blocked and pause (only at the user's request) apply to an
// active goal. Resuming (also only at the user's request) makes a paused,
// stalled or usage limited goal active again, as the user's /goal resume
// does, with a fresh stall audit.
func (g *Goal) Adopt(file *Goal) bool {
	if file == nil {
		return false
	}
	switch g.Status {
	case Paused, Blocked, UsageLimited:
		// Only a report written after our last save: a file that still says
		// active from before the goal stopped is stale, not a resume.
		if file.Status != Active || !file.Updated.After(g.Updated) {
			return false
		}
		g.Status, g.Note, g.FailStreak, g.IdleStreak = Active, "", 0, 0
		return true
	case Active:
	default:
		return false
	}
	switch file.Status {
	case Complete, Blocked, Paused:
	default:
		return false
	}
	g.Status, g.Note = file.Status, file.Note
	return true
}

// TurnEnded records a finished turn and applies the stop conditions:
// failures and turns without tool calls. The turn's time is not added
// here: the driver accrues Seconds while the turn runs.
func (g *Goal) TurnEnded(failed error, toolCalls int) {
	g.Turns++
	if g.Status != Active {
		return
	}
	if failed != nil {
		if IsUsageLimit(failed) {
			g.Status, g.Note = UsageLimited, failed.Error()
			return
		}
		g.FailStreak++
		if g.FailStreak >= maxFailStreak {
			g.Status, g.Note = Blocked, fmt.Sprintf("%d turns in a row failed (last: %v)", g.FailStreak, failed)
		}
		return
	}
	g.FailStreak = 0
	if toolCalls == 0 {
		g.IdleStreak++
		if g.IdleStreak >= maxIdleStreak {
			g.Status, g.Note = Blocked, fmt.Sprintf("no progress: %d turns in a row without running anything", g.IdleStreak)
		}
		return
	}
	g.IdleStreak = 0
}

// Usage is "12.5K tokens · 14m".
func (g *Goal) Usage() string {
	s := Tokens(g.TokensUsed) + " tokens"
	if g.Seconds > 0 {
		s += " · " + FormatElapsed(g.Seconds)
	}
	return s
}

// Tokens is a token count as codex shows it (format_tokens_compact): 950,
// 1.23K, 12.5K, 125K, 63.9K, 1.5M.
func Tokens(n int) string {
	if n <= 0 {
		return "0"
	}
	if n < 1000 {
		return fmt.Sprint(n)
	}
	v, suffix := float64(n), ""
	switch {
	case n >= 1_000_000_000_000:
		v, suffix = v/1e12, "T"
	case n >= 1_000_000_000:
		v, suffix = v/1e9, "B"
	case n >= 1_000_000:
		v, suffix = v/1e6, "M"
	default:
		v, suffix = v/1e3, "K"
	}
	decimals := 0
	switch {
	case v < 10:
		decimals = 2
	case v < 100:
		decimals = 1
	}
	f := fmt.Sprintf("%.*f", decimals, v)
	if strings.Contains(f, ".") {
		f = strings.TrimSuffix(strings.TrimRight(f, "0"), ".")
	}
	return f + suffix
}

// FormatElapsed is a goal's time as codex shows it: 59s, 30m, 1h 30m, 2h,
// 1d 0h 0m.
func FormatElapsed(seconds int64) string {
	seconds = max(0, seconds)
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := seconds / 60
	if minutes < 60 {
		return fmt.Sprintf("%dm", minutes)
	}
	hours, rem := minutes/60, minutes%60
	if hours >= 24 {
		return fmt.Sprintf("%dd %dh %dm", hours/24, hours%24, rem)
	}
	if rem == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh %dm", hours, rem)
}

// Label is a status as codex words it.
func (s Status) Label() string {
	switch s {
	case Active:
		return "active"
	case Paused:
		return "paused"
	case Blocked:
		return "stalled"
	case UsageLimited:
		return "usage limited"
	case Complete:
		return "complete"
	}
	return string(s)
}

// Summary is codex's goal_usage_summary: "Objective: … Time: 2m." (time
// only once some is used).
func (g *Goal) Summary() string {
	parts := []string{"Objective: " + g.Objective}
	if g.Seconds > 0 {
		parts = append(parts, "Time: "+FormatElapsed(g.Seconds)+".")
	}
	return strings.Join(parts, " ")
}

// Indicator is the status indicator, worded as codex's footer: "Pursuing
// goal (14m)", "Goal paused (/goal resume)" and so on. seconds is
// the goal's time including the running turn; held is an active goal
// waiting for the user after their input.
func (g *Goal) Indicator(seconds int64, held bool) string {
	switch g.Status {
	case Active:
		if held {
			return "Goal waiting (enter to continue)"
		}
		return "Pursuing goal (" + FormatElapsed(seconds) + ")"
	case Paused:
		return "Goal paused (/goal resume)"
	case Blocked:
		return "Goal stalled (/goal resume)"
	case UsageLimited:
		return "Goal hit usage limits (/goal resume)"
	case Complete:
		return "Goal achieved (" + FormatElapsed(seconds) + ")"
	}
	return ""
}

// IsUsageLimit reports whether err is the provider saying the usage limit
// was reached (see ai.IsUsageLimit).
func IsUsageLimit(err error) bool { return ai.IsUsageLimit(err) }

// IsTransient reports whether a goal turn that failed with err is worth
// trying again: anything ai.IsPermanent does not rule out (an interrupt, a
// usage limit, a context overflow, auth, a rejected request, a policy
// refusal), as codex retries. The turn's own retries (agent) already ran.
func IsTransient(err error) bool { return err != nil && !ai.IsPermanent(err) }

// Goal messages are wrapped as internal context, after codex's
// <codex_internal_context>, so the model can tell them from the user's own
// words (see the system prompt's Goals paragraph).
const (
	OpenTag  = `<atto_internal_context source="goal">`
	CloseTag = `</atto_internal_context>`

	// legacyPrefix marked goal messages before the wrapper; sessions
	// written then still hold them.
	legacyPrefix = "[atto goal] "
)

// wrap frames a goal message as internal context.
func wrap(body string) string {
	return OpenTag + "\n" + strings.TrimSpace(body) + "\n" + CloseTag
}

// IsMessage reports whether text is a goal message: a user-role message
// atto inserted, not the user. The old "[atto goal] " prefix counts.
func IsMessage(text string) bool {
	return strings.HasPrefix(text, OpenTag) || strings.HasPrefix(text, legacyPrefix)
}

// Body is a goal message without its wrapper (or old prefix); text that
// is not a goal message comes back unchanged.
func Body(text string) string {
	if rest, ok := strings.CutPrefix(text, OpenTag); ok {
		rest = strings.TrimPrefix(rest, "\n")
		rest = strings.TrimSuffix(rest, CloseTag)
		return strings.TrimSpace(rest)
	}
	return strings.TrimPrefix(text, legacyPrefix)
}

// escape keeps the objective from closing the tag it is wrapped in.
func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// data is what the goal templates are filled from.
func (g *Goal) data() prompts.Goal {
	return prompts.Goal{
		Objective:   escape(g.Objective),
		Turns:       g.Turns,
		Label:       g.Status.Label(),
		Interrupted: g.Note == NoteInterrupted,
	}
}

// Continuation is the message that starts each goal turn, after codex's
// continuation template. The objective is re-sent every time, so the goal
// survives compaction, and is framed as user data rather than instructions
// that outrank everything else.
func (g *Goal) Continuation() string {
	return wrap(prompts.Render("goal_continuation", g.data()))
}

// ClearedMessage and PausedMessage tell the running
// turn what the user just did to the goal, so it stops goal work (and does
// not set a new goal) instead of finishing what the user cancelled.
func ClearedMessage() string { return wrap(prompts.Render("goal_cleared", nil)) }

func (g *Goal) PausedMessage() string { return wrap(prompts.Render("goal_paused", g.data())) }

// StateMessage is the note that goes with a message the user started a turn
// with while the goal is not running by itself: waiting for the user (held),
// paused, stalled or usage limited. It tells the model to answer
// the user instead of picking the goal work back up. Empty for a goal that
// is running or finished.
func (g *Goal) StateMessage(held bool) string {
	var name string
	switch g.Status {
	case Active:
		if !held {
			return ""
		}
		name = "goal_state_waiting"
	case Paused, Blocked, UsageLimited:
		name = "goal_state_paused"
	default:
		return ""
	}
	return wrap(prompts.Render(name, g.data()))
}

// SteerMessage is the note that goes with a message the user sends while an
// active goal's turn runs, which models otherwise take for a paused goal.
func SteerMessage() string { return wrap(prompts.Render("goal_state_running_steer", nil)) }

// SplitNote separates a goal note appended to a user message (see
// StateMessage, SteerMessage) from the message; text without one comes back
// unchanged.
func SplitNote(text string) (message, note string) {
	i := strings.LastIndex(text, "\n\n"+OpenTag)
	if i < 0 || !strings.HasSuffix(text, CloseTag) {
		return text, ""
	}
	return text[:i], text[i+2:]
}

// ObjectiveUpdatedMessage tells the model, mid-turn, that the user edited
// the objective (codex's objective_updated template).
func (g *Goal) ObjectiveUpdatedMessage() string {
	return wrap(prompts.Render("goal_objective_updated", g.data()))
}

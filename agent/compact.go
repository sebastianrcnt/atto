package agent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/ai"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/prompts"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

// AutoCompactLimit uses 90% of the window or first price-tier boundary,
// further capped so the largest possible response still fits.
func AutoCompactLimit(m config.Model) int {
	return compactLimit(m, m.Cost.ContextPriceBoundary())
}

func compactLimit(m config.Model, cap int) int {
	if m.ContextWindow <= 0 {
		return 0
	}
	limit := m.ContextWindow * 9 / 10
	if m.MaxTokens > 0 {
		limit = min(limit, m.ContextWindow-m.MaxTokens)
	}
	if cap > 0 && cap < m.ContextWindow {
		limit = min(limit, cap*9/10)
	}
	return max(limit, 0)
}

// SetCompaction applies persistent per-model caps. A missing cap uses prices.
func (a *Agent) SetCompaction(c *config.Compaction) {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	a.compactLimits = nil
	if c != nil {
		a.compactLimits = maps.Clone(c.Limits)
	}
}

func (a *Agent) SetLongContext(long bool) {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	a.longContext = long
}

func (a *Agent) LongContext() bool {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	return a.longContext
}

// Why a cap lowers the compaction trigger.
const (
	ReasonPriceTier = "price-tier" // the model costs more above Cap input tokens
	ReasonSetting   = "setting"    // settings.json compaction.limits
)

// CompactionLimit returns the trigger and the effective cap (zero: window).
func (a *Agent) CompactionLimit() (limit, cap int) {
	limit, cap, _ = a.compactionPlan()
	return limit, cap
}

// compactionPlan is CompactionLimit and where the cap comes from. A cap
// that does not lower the trigger below the window's own (the room for the
// answer already does) is none.
func (a *Agent) compactionPlan() (limit, cap int, reason string) {
	m, cap, reason := a.compactionCap()
	limit = compactLimit(m.Model, cap)
	if cap <= 0 || limit >= compactLimit(m.Model, 0) {
		cap, reason = 0, ""
	}
	return limit, cap, reason
}

// compactionCap keeps the selected input cap even when output room already
// lowers the trigger: a resumed conversation may still exceed that cap.
func (a *Agent) compactionCap() (m config.ModelRef, cap int, reason string) {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	m = a.model
	cap, reason = m.Model.Cost.ContextPriceBoundary(), ReasonPriceTier
	if n, ok := a.compactLimits[m.ProviderName+"/"+m.Model.ID]; ok && n >= 0 {
		cap, reason = n, ReasonSetting
	}
	if a.longContext || cap >= m.Model.ContextWindow {
		cap = 0
	}
	return m, cap, reason
}

// CompactNoteWords bounds the length of handoff notes.
const CompactNoteWords = 700

// SummaryPrefix introduces handoff notes in the compacted history.
var SummaryPrefix = prompts.Render("compact_prefix", nil) + "\n\n"

// keepUserTokens is how much recent user text survives compaction (codex
// keeps 20k tokens of user messages).
const keepUserTokens = 20000

// ErrCompaction identifies a failed attempt to write handoff notes.
var ErrCompaction = errors.New("compaction failed")

// Compact replaces the conversation with handoff notes written by the model.
func (a *Agent) Compact(ctx context.Context, emit func(any)) error {
	return a.compact(ctx, emit, false)
}

// compact asks the model for handoff notes. The request reuses the full
// existing prefix (system, tools, history) and appends the instruction at the
// end, so the prefix cache stays warm. The new history is the most recent
// user messages (up to keepUserTokens) followed by the notes. If the latest
// turn cannot fit, its trailing tool-call/result group stays after the notes
// and only the earlier prefix is summarized.
func (a *Agent) compact(ctx context.Context, emit func(any), auto bool) error {
	if len(a.messages) == 0 {
		return fmt.Errorf("%w: nothing to compact", ErrCompaction)
	}
	if a.Hooks != nil {
		emitHook(emit, "PreCompact", a.Hooks.PreCompact(ctx, auto))
	}
	start := time.Now()
	before := a.ContextTokens()
	_, fitCap, _ := a.compactionCap()
	var reason string
	var cap int
	if auto {
		_, cap, reason = a.compactionPlan()
	}
	emit(CompactStart{Auto: auto, Reason: reason, Cap: cap})

	prompt := provider.Message{Role: "user", Content: prompts.Render("compact", map[string]any{"Words": CompactNoteWords})}
	client, req := a.request(prompt)
	req.ToolChoice = "none"
	model, _ := a.Current()
	est := before + messageChars(prompt)/4
	var res provider.Result
	var err error
	var retained []provider.Message
	try, sent := 0, 0
	cut := ""
	for again := 0; ; again++ {
		for ; ; try++ {
			// The conversation is at its limit by now, and one large tool result
			// can take it past the point where the request plus a full-size
			// answer fits: give the notes the room that is left, and when that
			// is too little drop the oldest turns from the request (codex trims
			// history the same way). If even the last turn cannot fit, retain
			// its latest tool-call/result group outside the summary request.
			// The conversation itself is untouched until the notes succeed.
			need := compactRoom << try
			old := req.Messages
			dropped, kept := fitCompactionUnderCap(&req, model.Model, est, need, fitCap)
			for _, m := range old[1 : 1+dropped] {
				est -= messageChars(m) / 4
			}
			for _, m := range kept {
				est -= messageChars(m) / 4
			}
			retained = append(kept, retained...)
			if dropped > 0 {
				emit(CompactTrimmed{Messages: dropped})
			}
			sent++
			res, err = client.Stream(ctx, req, provider.Handler{
				OnText: func(s string) { emit(CompactDelta{s}) },
			})
			if err == nil || try >= 2 || !contextExceeded(err) || ctx.Err() != nil {
				break
			}
		}
		// Notes cut off (the answer ran out, or ended in a call) would
		// replace the conversation with half a handoff: write them again,
		// once, and else leave the conversation as it is.
		if cut = cutNotes(res); err != nil || cut == "" || again >= 1 {
			break
		}
		emit(CompactDelta{"\n\n(the notes were cut off: writing them again)\n\n"})
	}
	// For /debug: the compaction request(s) and the turn's request before,
	// to check that the compaction kept the server's prefix cache.
	ai.PinRecentRequests("compaction", sent+1)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCompaction, err)
	}
	if cut != "" {
		return fmt.Errorf("%w: the handoff notes were cut off (%s), twice; the conversation is unchanged: %w", ErrCompaction, cut, ai.ErrNotRetryable)
	}
	notes := strings.TrimSpace(res.Message.Content)
	if notes == "" {
		notes = "(no notes available)"
	}

	// Most recent user messages, newest first within the budget, kept in
	// chronological order. Earlier notes are not kept: the new notes fold
	// them in.
	var kept []provider.Message
	budget := keepUserTokens * 4
	for i := len(a.messages) - 1; i >= 0 && budget > 0; i-- {
		m := a.messages[i]
		if m.Role != "user" || strings.HasPrefix(m.Content, SummaryPrefix) {
			continue
		}
		// Like codex, kept messages carry text only; each image becomes a
		// note (the placeholder labels in the text still say what it was).
		if len(m.Images) > 0 {
			m = stripImages(m, ImagesCompacted)
		}
		if len(m.Content) > budget {
			m.Content = m.Content[len(m.Content)-budget:] + "\n[truncated]"
		}
		budget -= len(m.Content)
		kept = append([]provider.Message{m}, kept...)
	}
	replacement := append(kept, provider.Message{Role: "user", Content: SummaryPrefix + notes})
	replacement = append(replacement, retained...)

	a.messages = replacement
	a.LastUsage = provider.Usage{}
	a.sinceUsage = len(a.system)
	for _, m := range replacement {
		a.sinceUsage += messageChars(m)
	}
	after, elapsed := a.ContextTokens(), time.Since(start)
	if a.Record != nil {
		a.Record(session.Entry{Type: session.TypeCompaction, Replacement: replacement, Notes: notes, TokensBefore: before,
			TokensAfter: after, ElapsedMs: elapsed.Milliseconds(), Auto: auto, Reason: reason, Cap: cap, Finish: res.FinishReason})
	}
	emit(CompactEnd{Notes: notes, Before: before, After: after, Elapsed: elapsed})
	return nil
}

// compactRoom is the least room a compaction request keeps for its answer:
// the notes (CompactNoteWords) and the thinking before them.
const compactRoom = 8192

// cutNotes says how compaction notes were cut off, or "" when they are
// whole: the answer ended at its token limit or in a tool call, or its
// last line is a heading with nothing under it.
func cutNotes(res provider.Result) string {
	switch res.FinishReason {
	case "length":
		return "the answer reached its token limit"
	case "tool_calls":
		return "the model made a tool call"
	}
	notes := strings.TrimSpace(res.Message.Content)
	if i := strings.LastIndexByte(notes, '\n'); strings.HasPrefix(notes[i+1:], "#") {
		return "it ends with a heading"
	}
	return ""
}

// fitCompaction makes req, whose prompt is about est tokens, fit model's
// context window with need tokens left to answer: it lowers MaxTokens to
// the room left, and drops the oldest turns of the conversation (never
// the system prompt or the compaction prompt, and whole turns, so a tool
// call keeps its result) until need fits. If the last turn alone is too
// large, it cuts at its latest assistant tool call and returns that suffix
// to keep after the notes instead of summarizing it. The other return value
// counts messages dropped from the oldest turns.
func fitCompaction(req *provider.Request, m config.Model, est, need int) (int, []provider.Message) {
	return fitCompactionUnderCap(req, m, est, need, 0)
}

// fitCompactionUnderCap also keeps the input below cap, independently of
// output room. Drop oldest whole turns first for a price or settings cap;
// only then consider retaining an oversized latest tool group. Like window
// fitting, it leaves the newest indivisible turn even if it cannot fit.
func fitCompactionUnderCap(req *provider.Request, m config.Model, est, need, cap int) (int, []provider.Message) {
	window := m.ContextWindow
	if window <= 0 {
		return 0, nil
	}
	const margin = 256 // token estimates are rough; servers add a few of their own
	dropped := 0
	msgs := req.Messages // [system, conversation..., compaction prompt]
	var kept []provider.Message
	dropOldest := func() bool {
		end := 2
		for end < len(msgs)-1 && msgs[end].Role != "user" {
			end++
		}
		if end >= len(msgs)-1 { // one turn left: keep it
			return false
		}
		for _, d := range msgs[1:end] {
			est -= messageChars(d) / 4
		}
		dropped += end - 1
		msgs = append(msgs[:1:1], msgs[end:]...)
		return true
	}
	for cap > 0 && est+margin > cap && len(msgs) > 3 {
		if !dropOldest() {
			break
		}
	}
	fits := func(tokens, output int) bool {
		return window-tokens-margin >= output && (cap <= 0 || tokens+margin <= cap)
	}
	if !fits(est, need) {
		last := 1
		for i := 2; i < len(msgs)-1; i++ {
			if msgs[i].Role == "user" {
				last = i
			}
		}
		lastEst := est
		for _, m := range msgs[1:last] {
			lastEst -= messageChars(m) / 4
		}
		if !fits(lastEst, need) {
			// Dropping older turns cannot help. Summarize their history and
			// the current turn's prefix, keeping the latest call with all its
			// results. Never cut at a tool result itself.
			for i := len(msgs) - 2; i > last; i-- {
				if msgs[i].Role == "assistant" && len(msgs[i].ToolCalls) > 0 {
					tail := msgs[i : len(msgs)-1]
					tokens := 0
					for _, m := range tail {
						tokens += messageChars(m) / 4
					}
					// The cut must leave room for at least the minimum answer,
					// not just retain tools when the prefix itself cannot fit.
					if !fits(lastEst-tokens, 1024) {
						continue
					}
					kept = tail
					est -= tokens
					msgs = append(msgs[:i:i], msgs[len(msgs)-1])
					break
				}
			}
		}
	}
	for !fits(est, need) && len(msgs) > 3 {
		if !dropOldest() {
			break
		}
	}
	req.Messages = msgs
	if room := window - est - margin; req.MaxTokens <= 0 || req.MaxTokens > room {
		req.MaxTokens = max(room, 1024)
	}
	return dropped, kept
}

// contextExceeded reports whether err says the request didn't fit the
// model's context window.
func contextExceeded(err error) bool { return ai.IsContextOverflow(err) }

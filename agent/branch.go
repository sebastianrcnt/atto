package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/prompts"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

// Branch summaries, after pi: going back in the session tree can leave a
// summary of the branch being left, so the conversation on the new branch
// knows what was tried there. The summary is written by the current model
// the way compaction notes are: the request is the conversation as it
// stands (the branch being left, prefix cache warm) with the instruction
// appended, and it names the message where the branch starts so the model
// leaves out what both branches share.

// Events emitted during SummarizeBranch.
type (
	BranchSummaryStart struct{}
	BranchSummaryDelta struct{ Text string }
	BranchSummaryEnd   struct {
		Summary string
		Elapsed time.Duration
	}
)

// BranchSummaryWords bounds the length of a branch summary.
const BranchSummaryWords = 600

// BranchSummaryPrefix starts the message that carries a branch summary to
// the model on the new branch; the summary follows in <summary> tags.
var BranchSummaryPrefix = prompts.Render("branch_summary_prefix", nil) + "\n\n"

// BranchSummaryMessage is the user message a branch summary becomes in
// the model's context. It depends only on the summary, so replaying the
// entry gives the same bytes each time.
func BranchSummaryMessage(summary string) provider.Message {
	return provider.Message{Role: "user", Content: BranchSummaryPrefix + "<summary>\n" + summary + "\n</summary>"}
}

// SummarizeBranch asks the model for a summary of branch, the entries the
// leaf is moving away from (session.Abandoned), which must be the end of
// the conversation the agent holds. instructions, if not empty, are the
// user's own focus for it. Nothing is recorded or changed: the caller
// writes the summary entry (session.Writer.BranchSummary) and restores the
// new branch. A canceled ctx stops it with ctx.Err().
func (a *Agent) SummarizeBranch(ctx context.Context, branch []session.Entry, instructions string, emit func(any)) (string, error) {
	return a.SummarizeBranchFrom(ctx, branchStart(branch), instructions, emit)
}

// SummarizeBranchFrom accepts the bounded origin description of a disk-streamed
// branch; the model already owns the context to be summarized.
func (a *Agent) SummarizeBranchFrom(ctx context.Context, origin, instructions string, emit func(any)) (string, error) {
	if len(a.messages) == 0 {
		return "", fmt.Errorf("nothing to summarize")
	}
	start := time.Now()
	emit(BranchSummaryStart{})
	prompt := prompts.Render("branch_summary", map[string]any{
		"Start": origin, "Words": BranchSummaryWords, "Focus": strings.TrimSpace(instructions),
	})
	client, req := a.request(provider.Message{Role: "user", Content: prompt})
	req.ToolChoice = "none"
	res, err := client.Stream(ctx, req, provider.Handler{
		OnText: func(s string) { emit(BranchSummaryDelta{s}) },
	})
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("branch summary failed: %w", err)
	}
	summary := strings.TrimSpace(res.Message.Content)
	if summary == "" {
		summary = "(no summary available)"
	}
	emit(BranchSummaryEnd{Summary: summary, Elapsed: time.Since(start)})
	return summary, nil
}

// HasBranchContent reports whether a branch being left has anything to
// summarize: a message, compaction or earlier branch summary.
func HasBranchContent(branch []session.Entry) bool {
	for _, e := range branch {
		switch e.Type {
		case session.TypeMessage:
			if e.Message != nil {
				return true
			}
		case session.TypeCompaction, session.TypeBranchSummary:
			return true
		case session.TypeBashExecution:
			if e.Bash != nil && !e.Bash.Exclude {
				return true
			}
		}
	}
	return false
}

// branchStart names, for the model, the message where branch begins.
func branchStart(branch []session.Entry) string {
	quote := func(s string) string {
		s = strings.Join(strings.Fields(s), " ")
		if r := []rune(s); len(r) > 160 {
			s = string(r[:160]) + "…"
		}
		return fmt.Sprintf("%q", s)
	}
	for _, e := range branch {
		switch e.Type {
		case session.TypeCompaction:
			return "the latest handoff notes (and the work they describe since that point)"
		case session.TypeBranchSummary:
			return "the branch summary that begins " + quote(e.Summary)
		case session.TypeBashExecution:
			if e.Bash != nil && !e.Bash.Exclude {
				return "the user's shell command " + quote(e.Bash.Command)
			}
		case session.TypeMessage:
			m := e.Message
			if m == nil {
				continue
			}
			switch m.Role {
			case "user":
				return "the user message that begins " + quote(m.Content)
			case "assistant":
				if strings.TrimSpace(m.Content) != "" {
					return "your reply that begins " + quote(m.Content)
				}
				for _, tc := range m.ToolCalls {
					var args BashArgs
					_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
					return "your command " + quote(args.Command)
				}
			case "tool":
				return "the command output that begins " + quote(m.Content)
			}
		}
	}
	return "the last message"
}

// BranchOrigin identifies an entry that determines the summary's start, without
// retaining the other entries on an abandoned branch.
func BranchOrigin(e session.Entry) (string, bool) {
	origin := branchStart([]session.Entry{e})
	return origin, origin != "the last message"
}

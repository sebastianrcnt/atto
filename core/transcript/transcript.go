// Package transcript is a conversation as the front ends show it: a list
// of items (user messages, assistant text, reasoning, tool calls,
// compactions, events, hook messages...). A Builder makes the items from
// the agent's live events as a turn runs, or in one go from the active
// branch of a saved session (Replay), and both go through the same code,
// so a resumed conversation looks the way it did live. The TUI turns items
// into blocks, the server into JSON-RPC items and atto -p into text or
// JSON lines; each keeps its own presentation.
package transcript

import (
	"time"

	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/provider"
)

// Kind is what an item is.
type Kind string

const (
	User       Kind = "user"       // a message the user sent, with its images
	Assistant  Kind = "assistant"  // the model's answer text
	Reasoning  Kind = "reasoning"  // the model's thinking
	Tool       Kind = "tool"       // a shell command the model ran
	Compaction Kind = "compaction" // the context was replaced by handoff notes
	Event      Kind = "event"      // an [atto event] for the model: a job exited, a timer fired, a monitor matched
	Goal       Kind = "goal"       // a goal message for the model: a continuation, or what the user did to the goal
	Hook       Kind = "hook"       // something a hook said, or what it blocked
	Notice     Kind = "notice"     // a message from atto itself (live only: not in the session)
	GoalStatus Kind = "goalStatus" // the goal changed status (live only)

	// BranchSummary is the summary of a branch the user went back from
	// (/tree), which the model sees on the new branch.
	BranchSummary Kind = "branchSummary"

	// Shell is a command the user ran with "!" or "!!" in the prompt.
	Shell Kind = "shell"

	// ExtText is a block of text an extension showed (ctx.ui.showText):
	// display only, never sent to the model.
	ExtText Kind = "extText"
)

// Status is where an item stands. Messages are complete when they start;
// streamed items (assistant text, reasoning, tools, compactions) are in
// progress until their step ends.
type Status string

const (
	InProgress Status = "inProgress"
	Completed  Status = "completed"
	Failed     Status = "failed" // a tool that failed, a compaction that did not finish
)

// Item is one entry of a transcript. Fields are used according to Kind.
type Item struct {
	ID     string
	Kind   Kind
	Status Status
	// Text is the message (user, assistant, reasoning, event, goal), the
	// handoff notes (compaction), the summary (branchSummary) or the
	// message of a hook or notice.
	Text string
	// EntryID is the session entry of the assistant message an assistant
	// or reasoning item belongs to, known once the message is recorded
	// (Handler.Saved fires then; "n<k>" when nothing is recorded). Front
	// ends make the item's block ID of it with session.BlockID.
	EntryID string
	// Images are a user message's images, or those atto view attached to
	// a tool's result, without their bytes.
	Images []provider.Image
	// Duration is the thinking time (reasoning), the run time (tool) or
	// how long a compaction or branch summary took, to the millisecond.
	// Zero when unknown.
	Duration time.Duration

	// Tool. Output is the command's output as shown: streamed, tidied when
	// the command ends, and only the tail when long (Dropped counts the
	// bytes let go).
	// Pending is set while the model is still writing the call (live
	// only): Description and Command fill in as they stream, and the item
	// is the same one that runs and finishes once the call is complete.
	Pending     bool
	CallID      string
	Description string
	Command     string
	Timeout     time.Duration
	Started     time.Time // when it began running (live only)
	Output      string
	Dropped     int
	Result      *ToolResult // set when the command ended

	// Compaction: the context estimate before and after (After is zero for
	// sessions saved before it was recorded).
	Auto         bool
	TokensBefore int
	TokensAfter  int

	// Shell: Command and Output (as for a tool; Output is replaced by the
	// saved text, cut as the model sees it, when the command ends) and
	// Result (Canceled, ExitCode). Excluded commands ("!!") are not sent
	// to the model; FullOutput is the file with all of a long output.
	Excluded   bool
	Truncated  bool
	FullOutput string

	// ExtText: Text under Title, shown by extension Ext; Lang says how to
	// colour it and Preview how many lines show collapsed (0: default).
	Ext     string
	Title   string
	Lang    string
	Preview int

	// Hook
	HookEvent string // UserPromptSubmit, PreToolUse, Stop...
	Blocked   bool

	// GoalStatus: the goal as it was when its status changed.
	GoalState *goal.Goal
}

// ToolResult is how a command ended.
type ToolResult struct {
	ExitCode int
	TimedOut bool
	Canceled bool
	Err      string // the command could not run
	// Job is the background job the command became (still running when
	// the result came), and Background why (agent.BackgroundRequested...).
	Job        int
	Background string
	// Text is the result as the model received it (live: before any
	// PostToolUse hook added to it).
	Text string
}

// Failed reports whether the command did not succeed.
func (r ToolResult) Failed() bool {
	return r.ExitCode != 0 || r.Err != "" || r.Canceled || r.TimedOut
}

// clone copies it so that later changes to the original don't show.
func (it *Item) clone() Item {
	c := *it
	c.Images = append([]provider.Image(nil), it.Images...)
	if it.Result != nil {
		r := *it.Result
		c.Result = &r
	}
	if it.GoalState != nil {
		g := *it.GoalState
		c.GoalState = &g
	}
	return c
}

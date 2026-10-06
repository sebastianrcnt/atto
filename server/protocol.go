// Package server exposes atto's agent over JSON-RPC 2.0 so any frontend
// (CLI, web, mobile, editor) can drive it. The model follows codex
// app-server: threads (conversations, persisted as sessions) contain turns
// (one user input and everything it causes), which produce items (user
// messages, reasoning, assistant messages, command executions,
// compactions, events, goal messages, hook messages; see
// core/transcript). Items stream as started → delta* → completed
// notifications.
//
// Requests:
//
//	initialize     {protocolVersions?, clientInfo?, capabilities?}
//	               → {name, version, protocolVersion, serverInstanceId, eventId, settings}
//	               protocolVersion is the newest revision both speak; none in
//	               common is refused (error data reason unsupportedProtocol).
//	               Errors may carry data {reason, retryable?}.
//	               settings: what of settings.json clients follow
//	               ({toolGroups}: false shows every command on its own)
//	models/list                                    → {models: [{id, name, contextWindow, efforts, hasKey, images}]}
//	thread/start   {cwd?, model?, effort?}         → thread + context
//	thread/resume  {threadId}                      → thread + items + context
//	               context: what the thread loaded (AGENTS files, skills,
//	               hooks, configuration, model and effort and where they
//	               came from), as stream-json's init event has it
//	thread/read    {threadId}                      → thread + items
//	thread/list    {cwd?, archived?}               → {threads: [...]}
//	thread/setModel {threadId, model}              → thread
//	thread/setEffort {threadId, effort}            → thread
//	thread/compact {threadId}                      → {turnId}
//	thread/rollback {threadId, numTurns?}          → thread + items + {input}
//	turn/start     {threadId, input, images?}      → {turnId}
//	               images: [{mimeType, data}], data base64 (or a data: URL);
//	               PNG, JPEG, GIF or WebP, at most 10 of 10 MB each, for
//	               models that take images
//	turn/steer     {threadId, input}               → {}
//	turn/interrupt {threadId}                      → {}
//	turn/background {threadId}                     → {}  (Ctrl+B: the running command becomes a job)
//	turn/unsteer   {threadId, input, queued?}      → {}
//	               takes back a pending steer (queued: a queued follow-up,
//	               live sessions) not delivered yet; refused once it was
//	job/list       {threadId}                      → {jobs: [Job]}  (the session's background jobs)
//	job/output     {threadId, job, lines?}         → {output}  (the last lines, 200 by default)
//	job/stop       {threadId, job}                 → {job}
//	subagent/list  {threadId}                      → {subagents: [Subagent]}
//	subagent/read  {threadId, name}                → {subagent, message, items}
//	               a subagent's transcript (its own session), read only,
//	               and its last message
//
// Notifications (all carry threadId):
//
//	turn/started   {turnId, startedAt, verb?}  (startedAt: Unix ms; verb: the word the terminal shows for "Working")
//	item/started   {turnId, item}
//	item/delta     {turnId, itemId, delta}
//	item/updated   {turnId, item}  (a command the model is still writing, pending: its description and command so far; again when it starts running)
//	item/completed {turnId, item}
//	hook           {event, message, blocked}  (also a hook item during a turn)
//	event          {title}  (inbox event delivered to the thread)
//	thread/reloaded {context, changes, promptChanged, error?}  (atto reload run by the agent)
//	extension/notify {extension, message, level}  (ctx.ui.notify)
//	item/display   {itemId, blockId, display}  (what extensions show on a reasoning or agentMessage item changed; display null: shown as it is)
//	extension/ui   {ui}  (the extensions' status items or widgets changed; see ExtensionUI)
//	thread/usage   {usage, step, contextTokens}  (after each model response: the session's totals and that response's)
//	turn/pending   {pending}  (pending steers or queued follow-ups changed; see PendingInput)
//	turn/completed {turnId, status, error?, usage, contextTokens}
//
// thread/read and thread/resume carry what the status line shows: the
// model's name and whether it is priced or on a subscription, the usage
// totals (Usage), and while a turn runs the turn (TurnInfo), with the
// pending input.
//
// Extensions shape what the client shows, as data only (text and a few
// tokens such as lang: "diff"; never markup or code): a reasoning or
// agentMessage item carries blockId and, when an extension set any,
// display {statuses, ext, text} (ctx.ui.setBlockStatus, setBlockDisplay;
// text replaces the item's own, which stays the original); ctx.ui.showText
// adds an extText item {title, ext, text, lang, preview}; and thread/read
// and thread/resume carry extensionUi {status, widgets}. Item displays
// and extText items are saved in the session (block_display and ext_text
// entries), so a resumed thread shows them again; the model never sees
// them.
//
// Over HTTP, thread/start, thread/resume and thread/read results carry
// eventId: the items are as of that event (those still streaming
// included), so follow the thread from there (GET /events?lastEventId=).
// When the events after the one a client resumes from are not known any
// more (the server restarted, or the client was away for longer than the
// server keeps events), the stream starts with
//
//	events/reset   {eventId}  (no threadId: read the thread again)
//
// Live session (atto's /remote, see Live): the server has one thread, the
// TUI's session. initialize says {live: true, threadId}; thread/start is
// refused (send /clear); thread/rollback takes back the last turn as
// /tree does, idle only; turn/start and turn/steer both send the
// input as if typed in the terminal (a turn, a steer or a queued turn:
// {status, turnId}). More notifications:
//
//	thread/switched {threadId, previousThreadId}  (/clear, /resume or /tree in the terminal: thread/read again)
//	thread/updated  {thread}  (model, effort, name or busy changed)
//	goal/updated    {goal}  (the goal changed; null when cleared; see GoalInfo)
//	prompt/open     {prompt}  (the terminal opened a picker or an input; see Prompt)
//	prompt/closed   {id, how, by}  (how: answered, cancelled or closed; by: terminal or remote)
//
// and one more request:
//
//	prompt/answer  {threadId, id, index? | text? | cancel?}  → {}
//	               answers the open prompt as if in the terminal: index
//	               picks an option of a select, text submits an input,
//	               cancel is Esc. The first answer wins, from either side;
//	               a prompt that is no longer open is refused.
//
// thread/read's result carries the open prompt and the goal as well.
package server

import (
	"encoding/json"
	"fmt"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

// ProtocolVersion is the protocol revision this server speaks. Revision 2
// adds connections with client IDs and event cursors, input IDs, the
// session runtime's scheduling (queue, send-now, goals, user shell),
// server-owned prompts and commands; revision 1 clients keep working
// with the methods they know. A client lists the revisions it speaks in
// initialize's protocolVersions; a server that shares none refuses it
// with reason unsupportedProtocol.
const ProtocolVersion = 2

// MinProtocolVersion is the oldest revision still served.
const MinProtocolVersion = 1

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int        `json:"code"`
	Message string     `json:"message"`
	Data    *ErrorData `json:"data,omitempty"`
}

// RPCError is a JSON-RPC error a request failed with.
type RPCError = rpcError

// ErrorData says why a request failed, for clients to act on rather
// than parse the message.
type ErrorData struct {
	Reason    string `json:"reason"`
	Retryable bool   `json:"retryable,omitempty"`
	// CurrentRevision is the revision a stale request should have named.
	CurrentRevision int `json:"currentRevision,omitempty"`
}

// Error reasons.
const (
	ReasonBusy                = "busy"
	ReasonNoModel             = "noModel"
	ReasonReadOnly            = "readOnly"
	ReasonStalePrompt         = "stalePrompt"
	ReasonAlreadyCommitted    = "alreadyCommitted"
	ReasonRevisionConflict    = "revisionConflict"
	ReasonOwnedElsewhere      = "ownedByLegacyWriter"
	ReasonUnsupported         = "unsupportedCapability"
	ReasonUnsupportedProtocol = "unsupportedProtocol"
	ReasonNotFound            = "notFound"
)

// failure is a server error with a reason.
func failure(reason, format string, args ...any) *rpcError {
	return &rpcError{Code: codeServer, Message: fmt.Sprintf(format, args...), Data: &ErrorData{Reason: reason}}
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeServer         = -32000
)

// Item types.
const (
	ItemUser       = "userMessage"
	ItemReasoning  = "reasoning"
	ItemAgent      = "agentMessage"
	ItemCommand    = "commandExecution"
	ItemCompaction = "compaction"
	ItemEvent      = "event" // [atto event]: a job exited, a timer fired, a monitor matched
	ItemGoal       = "goal"  // atto_internal_context source="goal": a goal continuation or note for the model
	ItemHook       = "hook"  // a hook's message, or what it blocked
	ItemNotice     = "notice"
	ItemGoalStatus = "goalStatus"
	ItemExtText    = "extText" // text an extension showed (ctx.ui.showText): display only

	// ItemBranchSummary is a summary of a branch the session went back
	// from in atto's /tree; threads show it when they resume such a session.
	ItemBranchSummary = "branchSummary"
)

// Item is one unit of a turn's output: the protocol form of a
// transcript.Item.
type Item struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Text   string `json:"text,omitempty"`   // message, reasoning, notes, hook message
	Status string `json:"status,omitempty"` // inProgress, completed, failed

	// commandExecution
	Description string `json:"description,omitempty"`
	Command     string `json:"command,omitempty"`
	Output      string `json:"output,omitempty"`
	ExitCode    *int   `json:"exitCode,omitempty"`
	DurationMs  int64  `json:"durationMs,omitempty"`
	TimedOut    bool   `json:"timedOut,omitempty"`
	// Job is the background job the command became (no exitCode then),
	// and Background why: requested, timeout or user.
	Job        int    `json:"job,omitempty"`
	Background string `json:"background,omitempty"`
	// Pending: the model is still writing the command (description and
	// command are what has arrived).
	Pending bool `json:"pending,omitempty"`
	// Images are what atto view attached to the command's result.
	Images []ItemImage `json:"images,omitempty"`

	// compaction
	Auto         bool `json:"auto,omitempty"`
	TokensBefore int  `json:"tokensBefore,omitempty"`
	TokensAfter  int  `json:"tokensAfter,omitempty"`

	// hook
	HookEvent string `json:"hookEvent,omitempty"`
	Blocked   bool   `json:"blocked,omitempty"`

	// goalStatus: the new status; text is its note
	GoalStatus string `json:"goalStatus,omitempty"`

	// reasoning, agentMessage: BlockID names the block for extensions (set
	// once the response is saved), and Display is what they show on it.
	BlockID string        `json:"blockId,omitempty"`
	Display *BlockDisplay `json:"display,omitempty"`

	// extText: text (under title) that extension ext showed; lang says
	// how to colour it ("diff", or plain text) and preview how many lines
	// show while it is collapsed (0: the client's default).
	Title   string `json:"title,omitempty"`
	Ext     string `json:"ext,omitempty"`
	Lang    string `json:"lang,omitempty"`
	Preview int    `json:"preview,omitempty"`
}

// ItemImage describes an image attached to a command's result: the file
// it was read from and its size as sent.
type ItemImage struct {
	Name   string `json:"name,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

// BlockDisplay is what extensions show on a reasoning or agentMessage item
// (ctx.ui.setBlockStatus, ctx.ui.setBlockDisplay): display only, the model
// never sees it. Text, when set, is shown in place of the item's own text
// (which stays the original, for a client to offer); it is Markdown, as
// the item's is.
type BlockDisplay struct {
	Statuses []BlockStatus `json:"statuses,omitempty"` // short labels for the header, in order
	Ext      string        `json:"ext,omitempty"`      // the extension whose text is shown
	Text     string        `json:"text,omitempty"`
}

type BlockStatus struct {
	Ext  string `json:"ext"`
	Text string `json:"text"`
}

// ExtensionUI is what extensions show around the input: status line items
// (ctx.ui.setStatus) and widgets, lines above the input (ctx.ui.setWidget).
// Keys are "<extension>/<key>"; both lists are in the order first set.
type ExtensionUI struct {
	Status  []ExtensionStatus `json:"status"`
	Widgets []ExtensionWidget `json:"widgets"`
}

type ExtensionStatus struct {
	Key  string `json:"key"`
	Text string `json:"text"`
}

type ExtensionWidget struct {
	Key   string   `json:"key"`
	Lines []string `json:"lines"`
}

// ThreadInfo describes a thread to clients.
type ThreadInfo struct {
	ID            string   `json:"threadId"`
	Cwd           string   `json:"cwd"`
	Name          string   `json:"name,omitempty"`
	Model         string   `json:"model"`
	Effort        string   `json:"effort"`
	Efforts       []string `json:"efforts,omitempty"`
	ContextWindow int      `json:"contextWindow,omitempty"`
	ContextTokens int      `json:"contextTokens"`
	Busy          bool     `json:"busy"`
	TurnID        string   `json:"turnId,omitempty"`
	Items         []Item   `json:"items,omitempty"`
	// What the status line shows of the model (see SetModel): its name,
	// the context size auto-compaction starts at, and whether it has
	// prices (a cost) and is on a subscription (the cost only estimates).
	ModelName        string `json:"modelName,omitempty"`
	AutoCompactLimit int    `json:"autoCompactLimit,omitempty"`
	Priced           bool   `json:"priced,omitempty"`
	Subscription     bool   `json:"subscription,omitempty"`
	// Usage is the session's token totals; Turn the running turn, and
	// Pending the input it has not taken yet (nil when none).
	Usage   *Usage        `json:"usage,omitempty"`
	Turn    *TurnInfo     `json:"turn,omitempty"`
	Pending *PendingInput `json:"pending,omitempty"`
	// Context is set in thread/start and thread/resume results.
	Context *core.Loaded `json:"context,omitempty"`
	// EventID, in thread/read and thread/resume results over HTTP, is the
	// latest event published when the items were read: follow the thread
	// from there (GET /events?lastEventId=).
	EventID int64 `json:"eventId,omitempty"`
	// Live marks the TUI's own session served by /remote.
	Live bool `json:"live,omitempty"`
	// Prompt is the live session's open picker or input, and Goal its
	// goal (live sessions only).
	Prompt *Prompt   `json:"prompt,omitempty"`
	Goal   *GoalInfo `json:"goal,omitempty"`
	// ExtensionUI, in thread/read and thread/resume results, is what the
	// extensions show around the input (nil when nothing).
	ExtensionUI *ExtensionUI `json:"extensionUi,omitempty"`
}

// SetModel fills in what a thread's info says of its model m; models is
// the configured list, which says whether the name needs its provider.
func SetModel(info *ThreadInfo, m config.ModelRef, models config.ModelsFile) {
	if m.Model.ID != "" {
		info.Model = m.ProviderName + "/" + m.Model.ID
	}
	info.ModelName, info.Efforts, info.ContextWindow = models.DisplayName(m), m.Model.Levels(), m.Model.ContextWindow
	info.AutoCompactLimit = agent.AutoCompactLimit(m.Model)
	c := m.Model.Cost
	info.Priced = c != nil && (c.Input > 0 || c.Output > 0 || c.CacheRead > 0 || c.CacheWrite > 0)
	info.Subscription = m.Provider.Subscription
}

// Usage is token usage: a session's totals, or one model response's
// (thread/usage's step). Input includes the cached and written tokens.
type Usage struct {
	InputTokens       int     `json:"inputTokens"`
	CachedInputTokens int     `json:"cachedInputTokens"`
	CacheWriteTokens  int     `json:"cacheWriteTokens,omitempty"`
	OutputTokens      int     `json:"outputTokens"`
	Cost              float64 `json:"cost,omitempty"` // US dollars; 0 without prices
	// The latest response's input and how much of it was cached, for the
	// cache hit rate (totals only).
	LastInputTokens       int `json:"lastInputTokens,omitempty"`
	LastCachedInputTokens int `json:"lastCachedInputTokens,omitempty"`
}

// Add counts one response's usage in the totals.
func (u *Usage) Add(x provider.Usage) {
	u.InputTokens += x.PromptTokens
	u.CachedInputTokens += x.CachedTokens
	u.CacheWriteTokens += x.CacheWriteTokens
	u.OutputTokens += x.CompletionTokens
	u.Cost += x.Cost
	u.LastInputTokens, u.LastCachedInputTokens = x.PromptTokens, x.CachedTokens
}

// StepUsage is one response's usage in protocol form.
func StepUsage(x provider.Usage) Usage {
	return Usage{InputTokens: x.PromptTokens, CachedInputTokens: x.CachedTokens, CacheWriteTokens: x.CacheWriteTokens, OutputTokens: x.CompletionTokens, Cost: x.Cost}
}

// UsageOf totals the usage recorded in session entries.
func UsageOf(entries []session.Entry) Usage {
	var u Usage
	for _, e := range entries {
		if e.Usage != nil {
			u.Add(*e.Usage)
		}
	}
	return u
}

// TurnInfo is the running turn as the terminal's activity line shows it.
type TurnInfo struct {
	StartedAt int64 `json:"startedAt"` // Unix milliseconds
	// Verb is the word shown for "Working" this turn (settings.json's
	// spinnerVerbs; live sessions only).
	Verb string `json:"verb,omitempty"`
	// The turn's tokens so far: input the server had not cached, and
	// output.
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

// PendingInput is input sent while a turn runs that it has not taken
// yet: steers, which it takes after the running command (or when the
// model stops), and in a live session follow-ups queued for after it.
type PendingInput struct {
	Steers []string `json:"steers"`
	Queued []string `json:"queued,omitempty"`
}

// Job is a background job of the thread's session (package jobs).
type Job struct {
	ID       int    `json:"id"`
	Label    string `json:"label"` // its name, or its command's first line
	Kind     string `json:"kind"`  // job, monitor, job+notify, monitor+notify
	Command  string `json:"command"`
	Status   string `json:"status"` // starting, running, exited, killed, failed, lost
	ExitCode *int   `json:"exitCode,omitempty"`
	Error    string `json:"error,omitempty"`
	// Started is Unix milliseconds; RuntimeMs how long it ran, or has run.
	Started   int64 `json:"started"`
	RuntimeMs int64 `json:"runtimeMs"`
}

// Subagent is a subagent of the thread's session (package subagent, atto
// agent) with its latest turn.
type Subagent struct {
	Name     string `json:"name"`
	Preset   string `json:"preset"`
	Model    string `json:"model"`
	Effort   string `json:"effort,omitempty"`
	ThreadID string `json:"threadId"` // its own session
	Task     string `json:"task"`     // the first message
	Prompt   string `json:"prompt"`   // the latest turn's
	// The latest turn: its number and status (idle, queued, running, done,
	// failed or stopped), how long it ran, its error and its usage.
	Turn         int     `json:"turn"`
	Status       string  `json:"status"`
	DurationMs   int64   `json:"durationMs,omitempty"`
	Error        string  `json:"error,omitempty"`
	InputTokens  int     `json:"inputTokens,omitempty"`
	CachedTokens int     `json:"cachedInputTokens,omitempty"`
	OutputTokens int     `json:"outputTokens,omitempty"`
	Cost         float64 `json:"cost,omitempty"`
	Created      int64   `json:"created"` // Unix milliseconds
}

// Prompt kinds.
const (
	PromptSelect = "select"
	PromptInput  = "input"
)

// Prompt is a choice or a line of input the live session's terminal asks
// for (a picker, a confirmation, an extension's dialog), mirrored to the
// clients so they can answer it.
type Prompt struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"` // select or input
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`

	// select: the options, and the one selected at first. Filterable
	// pickers (/model, /resume) can be searched; the client filters.
	// Total is the number of options before they were capped, when it was.
	Options    []PromptOption `json:"options,omitempty"`
	Selected   int            `json:"selected"`
	Filterable bool           `json:"filterable,omitempty"`
	Total      int            `json:"total,omitempty"`
	Note       string         `json:"note,omitempty"`

	// input: the text so far and a placeholder.
	Text        string `json:"text,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
}

type PromptOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// PromptAnswer is a client's answer to a prompt: Index for a select,
// Text for an input, or Cancel.
type PromptAnswer struct {
	Index  *int
	Text   *string
	Cancel bool
}

// GoalInfo is the live session's goal as the terminal shows it.
type GoalInfo struct {
	Objective string `json:"objective"`
	Status    string `json:"status"`      // active, paused, blocked, usage_limited, complete
	Label     string `json:"statusLabel"` // the status as atto words it: "stalled", "usage limited"
	// Indicator is the status line's text ("Pursuing goal (14m)"),
	// Summary the goal's usage summary.
	Indicator string `json:"indicator"`
	Summary   string `json:"summary"`
	Note      string `json:"note,omitempty"`
	// Tokens is "12.5K"; Elapsed the time spent, with
	// the running turn ("14m").
	Tokens     string `json:"tokens"`
	TokensUsed int    `json:"tokensUsed"`
	Elapsed    string `json:"elapsed"`
	Seconds    int64  `json:"seconds"`
	// Held: an active goal waiting for the user to continue it ("/goal
	// resume"), after a turn that took their input.
	Held bool `json:"held,omitempty"`
}

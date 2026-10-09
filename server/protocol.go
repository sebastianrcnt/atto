// Package server exposes atto's agent over JSON-RPC 2.0 so any frontend
// (terminal, web, mobile, editor) can drive it. The model follows codex
// app-server: threads (conversations, persisted as sessions) contain turns
// (one user input and everything it causes), which produce items (user
// messages, reasoning, assistant messages, command executions,
// compactions, events, goal messages, hook messages; see
// core/transcript). Items stream as started → delta* → completed
// notifications.
//
// With the daemon available, interactive clients and the app-server
// frontend attach to per-session workers. thread/start starts a worker;
// thread/resume joins an existing one, never another writer. Transport EOF
// and frontend shutdown detach; thread/close explicitly stops the session
// (/close uses reason "close"). Unattended idle workers retire after one
// minute, but turns, jobs, timers, active unheld goals and prompts retain them.
// app-server -in-process and ATTO_NO_DAEMON retain the in-process runtime. A routing facade translates event cursors and client
// provenance; its protocol revision is distinct from daemon control revision 4.
// Worker crashes recover saved sessions, not durable in-flight inputs/promises.
//
// Transports: atto app-server --listen stdio:// (default) and
// unix:///absolute/path.sock use JSON lines; --listen ws://IP:PORT uses
// one JSON-RPC message per RFC 6455 text message. All use ServeConn and
// the same event hub and worker routing. Unix sockets are 0600 and removed
// on exit; WS beyond loopback requires the persistent bearer token and
// warns about TLS. HTTP Authorization and ?token= work; browser Origin is
// same-host/loopback or explicitly --allow-origin. WS reconnect hydrates a
// fresh thread snapshot, not an arbitrary cursor replay.
// See docs/protocol.md for the client-author reference and example clients.
//
// Protocol revision 3 is the only one served: snapshots are bounded
// post-compaction tails, with earlier pages read from disk. Unattached
// workers keep no completed display items; event IDs and snapshot fences
// are unchanged. Earlier pages are oldest-first and never advance eventId.
//
// Requests:
//
//	initialize     {protocolVersions, clientInfo?, capabilities?}
//	               → {name, version, protocolVersion, serverInstanceId, clientId?, eventId, settings}
//	               protocolVersions must include 3. A client that offers only
//	               other revisions, or none, fails with error data.reason
//	               unsupportedProtocol, naming the revision served.
//	initialized    notification: acknowledges the handshake
//	               clientInfo and capabilities accept the Codex handshake shape.
//	               Errors carry data {reason, retryable?} for clients to act on.
//	               settings: what of settings.json clients follow
//	               ({toolGroups}: false shows every command on its own)
//	models/list                                    → {models: [{id, name, contextWindow, efforts, hasKey, images}]}
//	thread/start   {cwd?, model?, effort?, deferStart?}         → thread + context
//	thread/resume  {threadId, deferStart?, limit?}                      → thread + items tail + context
//	               context: what the thread loaded (AGENTS files, skills,
//	               hooks, configuration, model and effort and where they
//	               came from), as stream-json's init event has it
//	thread/read    {threadId, limit?}              → thread + items tail
//	thread/items   {threadId, before, limit?}       → {items,hasMore,before}
//	               Snapshots are post-compaction tails (default 200) with
//	               hasMore and before; thread/items reads the earlier pages.
//	               Page cursors do not change event IDs.
//	thread/entry   {threadId,entryId,offline?}      → complete saved entry
//	thread/list    {cwd?, archived?, includeAgents?, includeClosedAgents?, includeArchived?}               → {threads: [...]}
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
//	job/list       {threadId}                      → {jobs: [Job]}  (the session's background jobs)
//	job/output     {threadId, job, lines?}         → {output}  (the last lines, 200 by default)
//	job/stop       {threadId, job}                 → {job}
//	agent/tree     {threadId}                      → {rootThreadId, agents: [Agent]}
//	agent/list     {threadId}                      → {agents: [Agent]}
//	agent/read     {threadId, name}                → {agent, message, items}
//	               an agent's transcript (its own session), read only,
//	               and its last message
//
// Every thread runs in a session runtime with the terminal's scheduling
// (see thread): steers, a queue, send-now, the goal, inbox events, user
// shell commands, prompts and commands. More requests:
//
//	input/submit   {threadId, input, images?, intent?}  → {inputId?, status, turnId?}
//	               the input as typed: intent auto (Enter: a turn, a steer,
//	               queued while something else runs, "/command", "!shell",
//	               empty: resume the queue or a held goal), queue (Tab),
//	               replace (Ctrl+Enter: interrupt and send now) or steer.
//	               status: started, steered, queued or done. images may
//	               name a file of the image store instead of data.
//	turn/unsteer   {threadId, inputId?}  → {inputId, text, images}  (the last when no ID)
//	turn/interrupt {threadId, mode?}  → {interrupted}  (mode cancel: steers come
//	               back instead of going out)
//	queue/resume   {threadId}
//	shell/start    {threadId, command, exclude?}; shell/interrupt {threadId}
//	thread/attach  {threadId,limit?}  → thread + items (the client follows it)
//	thread/detach  {threadId, reason?}  → {closed, stoppedJobs?, notices?}
//	               the thread goes on; one with retention 0 that is left
//	               idle closes (reason: clear, resume, exit). Jobs, timers, goals
//	               and prompts prevent retirement; worker retention defaults to 1m.
//	worker/state   {threadId}  → {id, session, name, state, cwd, clients, busy, version, pid}
//	               internal worker registry diagnostics; no attachment is added.
//	thread/close   {threadId, reason?}  → {closed, stoppedJobs?, notices?}
//	thread/read    {threadId, offline?}  (offline: from the file, not loading it)
//	thread/setModel, thread/setEffort  {..., saveDefault?}
//	thread/setContextMode {threadId, contextMode: normal|long}
//	thread/setName {threadId, name}; thread/setLabel {threadId, entryId, label}
//	thread/tree    {threadId,query?,offline?} → {entries,leaf}; query → {matches}
//	               entries are bounded previews; thread/entry reads full text.
//	thread/navigate {threadId, entryId, summary?: {mode: none|auto|custom, instructions?}}
//	thread/fork    {threadId, entryId}  → {threadId, path, input, images}
//	thread/archive {threadId,stop?} → {threadId,path}; close then archive
//	thread/unarchive {threadId} → {threadId,path}; restore transcript only
//	thread/delete {threadId,stop?} → {threadId,notices?}; permanent cleanup
//	thread/statusLine {threadId} → {configured,lines,refreshInterval?,truncated?}
//	thread/debug {threadId} → {heap,goroutines,memory}; runtime profiles
//	thread/files {threadId, query?, limit?} → {files:[{path,directory}],truncated}
//	item/image {threadId, itemId, index, preview?} → {mimeType,data} (stored transcript image)
//	item/output {threadId, itemId} → {output,truncated} (stored transcript output)
//	thread/context {threadId, view?: system}  → ContextInfo
//	thread/reload  {threadId}; thread/debugRequest {threadId} → {request}
//	thread/sessionStart {threadId} (releases deferStart after the TUI trust picker)
//	models/reload {threadId}; mcp/list {threadId} → {servers}
//	thread/debugRequests {threadId} → {sets} (recent and pinned request bodies)
//	thread/handoff {threadId}  ("Run in background" without a daemon)
//	goal/read, goal/set {input}, goal/edit {input}, goal/pause, goal/resume, goal/clear
//	auth/list {threadId} → {providers,stored} (status, never credentials)
//	auth/login {threadId, provider, oauth?, apiKey?} → {status}; auth/updated and native prompts drive OAuth
//	auth/logout {threadId, provider} → {removed}; auth/cancel {threadId} → {}
//	commands/list  {threadId}  → {commands: [CommandInfo]}; commands/run {threadId, name, args?}
//	client/gate    {threadId, open}  (a picker of the client is open: automatic work waits)
//	job/stopAll, timer/list, timer/create {when, message}, timer/cancel {id}
//
// and notifications: input/recovered {clientId, text, images, ifEmpty}
// (input given back to the client that sent it), turn/activity {activity},
// thread/status {jobs, timers}, thread/branchChanged (read the thread
// again), thread/closed {reason, handoff}, thread/handedOff {line?,
// finished?}, commands/changed, goal/retry {at}. turn/pending carries
// items with IDs and paused; goal/updated the goal's state. Prompts
// (prompt/open, prompt/answer, prompt/closed) are the runtime's own
// questions: extension dialogs, MCP approvals, goal confirmations; every
// client sees them and the first answer wins (a late one: reason
// stalePrompt). With no client attached they wait.
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
//	thread/usage   {usage, step, contextTokens}  (after each model response: the session's totals and that response's)
//	turn/pending   {pending}  (pending steers or queued follow-ups changed; see PendingInput)
//	turn/completed {turnId, status, error?, usage, contextTokens}
//
// thread/read and thread/resume carry what the status line shows: the
// model's name and whether it is priced or on a subscription, the usage
// totals (Usage), and while a turn runs the turn (TurnInfo), with the
// pending input.
//
// Portable UI trees travel through ui/render and uiDisplay/uiBlock items.
// Callbacks stay in the worker; replayed drawings are passive.
//
// thread/start, thread/resume and thread/read results carry eventId: the
// items are as of that event (those still streaming included), so follow
// the thread from there. A connection that falls behind the server is told
//
//	events/reset   {eventId, serverInstanceId}  (no threadId: read the thread again)
//
// and goes on from the newest event.
//
// Every transport goes through one event hub (hub.go): each notification
// carries eventId, its number in the hub. On a connection (stdio, a Unix
// socket, a WebSocket, or a client in the same process, see Client) the
// client gets a clientId in initialize's result and every notification
// from then on; requests are handled in the order sent. A client that reads a thread drops the
// notifications whose eventId is at most the read's (see ThreadView), so
// a snapshot taken while events arrive is applied exactly once. Event IDs
// start from 1 in each server; serverInstanceId tells runs apart.
//
// Items carry what a client needs to show them as the
// terminal does (see Item and TranscriptItem): entryId, the command's
// timeout, cancellation and error, the user's images, a user command's
// shell fields, the full goal state of a goalStatus. When a reasoning or
// agentMessage item that already completed is saved, item/updated
//
// Two more notifications:
//
//	thread/updated  {thread}  (model, effort, name or busy changed)
//	goal/updated    {goal}  (the goal changed; null when cleared; see GoalInfo)
//
// Every client gets the runtime's prompts (prompt/open,
// prompt/closed {id, how, by}) and answers them with
//
//	prompt/answer  {threadId, id, index? | text? | cancel?}  → {}
//	               index picks an option of a select, text submits an
//	               input, cancel is Esc. The first answer wins; a prompt
//	               that is no longer open is refused (reason stalePrompt).
//
// thread/read's result carries the open prompt and the goal as well. A
// client's own pickers can be registered with prompt/clientOpen
// {threadId, prompt: {requestId, kind, title, options?, ...}} → Prompt,
// and withdraws with prompt/clientClose {threadId, id: requestId}.
// Answers are arbitrated exactly like execution prompts; the owner gets
// prompt/clientAnswered {clientId, requestId, answer}. Owner detach removes
// its pickers without answering; execution prompts still wait for a client.
// ping is an ordering fence: its reply follows preceding requests on the
// same JSON-lines connection (the UI need not wait for runtime requests).
package server

import (
	"encoding/json"
	"fmt"
	"github.com/sebastianrcnt/atto/ui"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/ai"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

// ProtocolVersion is the protocol revision this server speaks, and the only
// one it serves: connections with client IDs and event cursors, input IDs,
// the session runtime's scheduling (queue, send-now, goals, user shell),
// server-owned prompts and commands, bounded tail snapshots and
// thread/items disk-backed earlier pages. A client lists the revisions it
// speaks in initialize's protocolVersions; a server that lacks its revision
// refuses it with reason unsupportedProtocol.
const ProtocolVersion = 3

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
	ReasonParse               = "parseError"
	ReasonInvalidRequest      = "invalidRequest"
	ReasonInvalidParams       = "invalidParams"
	ReasonMethodNotFound      = "methodNotFound"
	ReasonInternal            = "internalError"
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
	EventID int64  `json:"eventId,omitempty"`
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
	ItemUIBlock    = "uiBlock"

	// ItemBranchSummary is a summary of a branch the session went back
	// from in atto's /tree; threads show it when they resume such a session.
	ItemBranchSummary = "branchSummary"
)

// Item is one unit of a turn's output: the protocol form of a
// transcript.Item.
type UIDisplay struct {
	ActionsEnabled bool     `json:"actionsEnabled,omitempty"`
	Rev            int64    `json:"rev"`
	Tree           *ui.Node `json:"tree"`
}

type Item struct {
	ActionsEnabled bool       `json:"actionsEnabled,omitempty"` // uiBlock: live bindings, never persisted
	UITree         *ui.Node   `json:"tree,omitempty"`
	UIRev          int64      `json:"rev,omitempty"`
	UIID           string     `json:"uiId,omitempty"`
	UIDisplay      *UIDisplay `json:"uiDisplay,omitempty"`
	ID             string     `json:"id"`
	Type           string     `json:"type"`
	Text           string     `json:"text,omitempty"`   // message, reasoning, notes, hook message
	Status         string     `json:"status,omitempty"` // inProgress, completed, failed

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
	Pending   bool  `json:"pending,omitempty"`
	StartedMs int64 `json:"startedMs,omitempty"` // running command start, Unix milliseconds
	// Images are what atto view attached to the command's result.
	Images []ItemImage `json:"images,omitempty"`

	// compaction
	Auto         bool `json:"auto,omitempty"`
	TokensBefore int  `json:"tokensBefore,omitempty"`
	TokensAfter  int  `json:"tokensAfter,omitempty"`

	// event: one title per [atto event] in text, as the TUI shows them
	// (text stays the full message the model got)
	Titles []string `json:"titles,omitempty"`

	// hook
	HookEvent string `json:"hookEvent,omitempty"`
	Blocked   bool   `json:"blocked,omitempty"`

	// goalStatus: the new status; text is its note
	GoalStatus string `json:"goalStatus,omitempty"`

	// reasoning, agentMessage: BlockID names the block for extensions (set
	// once the response is saved). UI overlays are separate portable trees.
	BlockID string `json:"blockId,omitempty"`

	// UIBlock title and provider.
	Title string `json:"title,omitempty"`
	Ext   string `json:"ext,omitempty"`
	// what a client needs to show an item as the terminal
	// does (see TranscriptItem). EntryID is the session entry the item
	// belongs to, when recorded.
	EntryID   string `json:"entryId,omitempty"`
	CallID    string `json:"callId,omitempty"`
	TimeoutMs int64  `json:"timeoutMs,omitempty"`
	// commandExecution: bytes of output not kept, and how it ended.
	Dropped    int    `json:"dropped,omitempty"`
	Canceled   bool   `json:"canceled,omitempty"`
	Error      string `json:"error,omitempty"`
	ResultText string `json:"resultText,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Cap        int    `json:"cap,omitempty"`
	// Shell marks a command the user ran ("!cmd"); Excluded one kept
	// from the model ("!!"), Truncated an output cut for the model
	// (FullOutput has all of it), and ContextPending one that finished
	// during a run: the model gets it when the run ends.
	Shell          bool   `json:"shell,omitempty"`
	Excluded       bool   `json:"excluded,omitempty"`
	Truncated      bool   `json:"truncated,omitempty"`
	FullOutput     string `json:"fullOutput,omitempty"`
	ContextPending bool   `json:"contextPending,omitempty"`
	// goalStatus: the goal as it was.
	GoalState *goal.Goal `json:"goalState,omitempty"`
	// notice: "" (plain), "warning" or "error"; with Title, an info
	// notice: Title, and Text under it.
	Level string `json:"level,omitempty"`
	// userMessage: the client that sent it, and its input ID; the user
	// messages of one committed steer share SteerGroup.
	ClientID   string `json:"clientId,omitempty"`
	InputID    string `json:"inputId,omitempty"`
	SteerGroup string `json:"steerGroup,omitempty"`
	// notice of level "loaded": what the session loaded, or after a
	// reload (Reloaded) what changed and what that did to the prompt.
	Loaded   *core.Loaded  `json:"loaded,omitempty"`
	Reloaded bool          `json:"reloaded,omitempty"`
	Changes  []core.Change `json:"changes,omitempty"`
	Note     string        `json:"note,omitempty"`
}

// ItemImage describes an image attached to a command's result: the file
// it was read from and its size as sent.
type ItemImage struct {
	Name   string `json:"name,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	File   string `json:"file,omitempty"`
	MIME   string `json:"mimeType,omitempty"`
}

// ThreadInfo describes a thread to clients.
type ThreadInfo struct {
	UI            *ui.Snapshot `json:"ui,omitempty"`
	ID            string       `json:"threadId"`
	Cwd           string       `json:"cwd"`
	Name          string       `json:"name,omitempty"`
	Model         string       `json:"model"`
	Effort        string       `json:"effort"`
	Efforts       []string     `json:"efforts,omitempty"`
	ContextWindow int          `json:"contextWindow,omitempty"`
	ContextTokens int          `json:"contextTokens"`
	Busy          bool         `json:"busy"`
	TurnID        string       `json:"turnId,omitempty"`
	Items         []Item       `json:"items,omitempty"`
	HasMore       bool         `json:"hasMore,omitempty"`
	Paged         bool         `json:"-"`
	Before        string       `json:"before,omitempty"`
	// What the status line shows of the model (see SetModel): its name,
	// the context size auto-compaction starts at, and whether it has
	// prices (a cost) and is on a subscription (the cost only estimates).
	ModelName        string `json:"modelName,omitempty"`
	AutoCompactLimit int    `json:"autoCompactLimit,omitempty"`
	AutoCompactCap   int    `json:"autoCompactCap,omitempty"`
	Priced           bool   `json:"priced,omitempty"`
	Subscription     bool   `json:"subscription,omitempty"`
	// Usage is the session's token totals; Turn the running turn, and
	// Pending the input it has not taken yet (nil when none).
	Usage   *Usage        `json:"usage,omitempty"`
	Turn    *TurnInfo     `json:"turn,omitempty"`
	Pending *PendingInput `json:"pending,omitempty"`
	// Context is set in thread/start and thread/resume results.
	Context *core.Loaded `json:"context,omitempty"`
	// EventID, in thread/read and thread/resume results, is the latest
	// event published when the items were read: follow the thread from
	// there, dropping notifications up to it.
	EventID int64 `json:"eventId,omitempty"`
	// Prompt is the session's open picker or input, and Goal its goal.
	Prompt *Prompt   `json:"prompt,omitempty"`
	Goal   *GoalInfo `json:"goal,omitempty"`

	// RunKind is what runs (turn, compact, branchSummary) and
	// Activity what it does; Jobs and Timers count the session's running
	// jobs and pending timers. ReadOnly says why the session cannot be
	// written (another process runs it); Offline marks a snapshot read
	// from the file without loading the session. ServerInstance goes with
	// EventID: the cursor.
	RunKind        string    `json:"runKind,omitempty"`
	Activity       *Activity `json:"activity,omitempty"`
	Jobs           int       `json:"jobs,omitempty"`
	Timers         int       `json:"timers,omitempty"`
	ReadOnly       string    `json:"readOnly,omitempty"`
	Offline        bool      `json:"offline,omitempty"`
	SessionPath    string    `json:"sessionPath,omitempty"`
	LongContext    bool      `json:"longContext,omitempty"`
	ServerInstance string    `json:"serverInstanceId,omitempty"`
}

// Activity is what a run is doing, as the activity line shows it
// (turn/activity).
type Activity struct {
	Phase        string `json:"phase"` // Thinking, Working, Retrying, Compacting context...
	RunKind      string `json:"runKind"`
	StartedAt    int64  `json:"startedAt"`
	ToolsRunning int    `json:"toolsRunning"`
}

// Timer is a pending timer of the session (timer/list).
type Timer struct {
	ID       string `json:"id"`
	Due      int64  `json:"due"` // Unix milliseconds
	Message  string `json:"message"`
	Schedule string `json:"schedule,omitempty"` // recurring
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
	Last              *provider.Usage `json:"last,omitempty"`
	LastCost          *ai.ModelCost   `json:"lastCost,omitempty"`
	InputTokens       int             `json:"inputTokens"`
	CachedInputTokens int             `json:"cachedInputTokens"`
	CacheWriteTokens  int             `json:"cacheWriteTokens,omitempty"`
	OutputTokens      int             `json:"outputTokens"`
	Cost              float64         `json:"cost,omitempty"` // US dollars; 0 without prices
	// The latest response's input and how much of it was cached, for the
	// cache hit rate (totals only).
	LastInputTokens       int `json:"lastInputTokens,omitempty"`
	LastCachedInputTokens int `json:"lastCachedInputTokens,omitempty"`
}

// Add counts one response's usage in the totals.
func (u *Usage) Add(x provider.Usage) {
	last := x
	u.Last = &last
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
	// each pending input with its ID (turn/unsteer takes it),
	// and whether the queue is paused (after a failed turn).
	Items  []PendingItem `json:"items,omitempty"`
	Paused bool          `json:"paused,omitempty"`
}

// PendingItem is one pending input.
type PendingItem struct {
	ID       string      `json:"id"`
	Kind     string      `json:"kind"` // steer or queued
	Text     string      `json:"text"`
	ClientID string      `json:"clientId,omitempty"`
	Images   []ItemImage `json:"images,omitempty"`
}

// Job is a background job of the thread's session (package jobs).
type Job struct {
	ID         int    `json:"id"`
	Label      string `json:"label"` // its name, or its command's first line
	Kind       string `json:"kind"`  // job, monitor, job+notify, monitor+notify
	Command    string `json:"command"`
	Status     string `json:"status"` // starting, running, exited, killed, failed, lost
	ExitCode   *int   `json:"exitCode,omitempty"`
	Error      string `json:"error,omitempty"`
	ResultText string `json:"resultText,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Cap        int    `json:"cap,omitempty"`
	// Started is Unix milliseconds; RuntimeMs how long it ran, or has run.
	Started   int64 `json:"started"`
	RuntimeMs int64 `json:"runtimeMs"`
}

// Agent is an agent of the thread's session (package agentstate, atto
// agent) with its latest turn.
type Agent struct {
	Name           string `json:"name"`
	ParentThreadID string `json:"parentThreadId,omitempty"`
	Path           string `json:"path,omitempty"`
	Preset         string `json:"preset"`
	Model          string `json:"model"`
	Effort         string `json:"effort,omitempty"`
	ThreadID       string `json:"threadId"` // its own session
	// Where it is in its tree: the root's session, the edges below it (0
	// for a root), whether it was started from a shell ("external") or by
	// an agent, the project it belongs to, and whether it is open, closing
	// or closed. A root started from a shell has no parentThreadId.
	RootThreadID string `json:"rootThreadId,omitempty"`
	Depth        int    `json:"depth"`
	Origin       string `json:"origin,omitempty"`
	Project      string `json:"project,omitempty"`
	Lifecycle    string `json:"lifecycle,omitempty"`
	// The job running its latest turn: a job of JobOwner (the parent, or the
	// agent itself for a root) numbered Job.
	JobOwner string `json:"jobOwner,omitempty"`
	Job      int    `json:"job,omitempty"`
	// SpawnedBy: who started it; tracking, not proof.
	SpawnedBy *session.SpawnedBy `json:"spawnedBy,omitempty"`
	Task      string             `json:"task"`   // the first message
	Prompt    string             `json:"prompt"` // the latest turn's
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
	PromptCustom      = "custom"
	PromptSelect      = "select"
	PromptInput       = "input"
	PromptMultiSelect = "multiSelect"
)

// Prompt is a runtime-owned question (a confirmation, an extension dialog
// or a client picker registered with prompt/clientOpen). Every attached client can
// answer it; the first valid answer wins.
type Prompt struct {
	UIID      string `json:"uiId,omitempty"`
	ClientID  string `json:"clientId,omitempty"`  // owner of a front-end picker
	RequestID string `json:"requestId,omitempty"` // owner correlation token
	ID        string `json:"id"`
	Kind      string `json:"kind"` // select, multiSelect or input
	Title     string `json:"title"`
	Subtitle  string `json:"subtitle,omitempty"`

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

	// who asks (extension, mcp, goal), and a select that is a
	// yes/no confirmation.
	Origin  string `json:"origin,omitempty"`
	Confirm bool   `json:"confirm,omitempty"`
}

type PromptOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// PromptAnswer is a client's answer to a prompt: Index for a select,
// Indexes for multiSelect, Text for an input, or Cancel.
type PromptAnswer struct {
	Index   *int    `json:"index,omitempty"`
	Indexes *[]int  `json:"indexes,omitempty"`
	Text    *string `json:"text,omitempty"`
	Cancel  bool    `json:"cancel,omitempty"`
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
	// the goal itself, and when the running turn started (a
	// client adds the time since to Seconds).
	Goal          *goal.Goal `json:"state,omitempty"`
	TurnStartedAt int64      `json:"turnStartedAt,omitempty"`
}

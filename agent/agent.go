// Package agent runs the model ↔ tool loop. It knows nothing about the UI:
// progress is reported through an emit callback with the event types below.
package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/prompts"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/shell"
	"github.com/sebastianrcnt/atto/skills"
)

// HookOutcome is what hooks decided for one event.
type HookOutcome struct {
	Block      bool   // deny the tool call / reject the prompt / keep going (Stop)
	Reason     string // shown to the model (and user) when blocking
	Context    string // extra context to add for the model
	Stop       bool   // "continue": false — end the turn now
	StopReason string
	Notices    []string // messages for the user (hook errors, output)
}

// Hooks lets user-configured hooks observe and steer the loop. Implemented
// by package hooks; nil means no hooks.
type Hooks interface {
	UserPromptSubmit(ctx context.Context, prompt string) HookOutcome
	PreToolUse(ctx context.Context, args BashArgs) (BashArgs, HookOutcome)
	PostToolUse(ctx context.Context, args BashArgs, res BashResult, output string) HookOutcome
	Stop(ctx context.Context, active bool) HookOutcome
	PreCompact(ctx context.Context, auto bool) HookOutcome
}

// Extensions lets JavaScript extensions (package extensions) observe and
// steer the loop, closest to the shell: PreToolUse hooks run before
// ToolCall, and PostToolUse hooks see the output ToolResult returns.
// UserPrompt runs after the UserPromptSubmit hooks. TurnStart and TurnEnd
// only observe. Implemented by package extensions; nil means none.
type Extensions interface {
	UserPrompt(ctx context.Context, prompt string) HookOutcome
	ToolCall(ctx context.Context, args BashArgs) (BashArgs, HookOutcome)
	// ToolResult returns the output for the model, possibly rewritten.
	ToolResult(ctx context.Context, args BashArgs, res BashResult, output string) (string, HookOutcome)
	TurnStart(prompt string)
	TurnEnd(err error)
	// BlockEnd reports that an assistant text or reasoning block (kind is
	// session.BlockText or session.BlockReasoning) is complete and saved:
	// id is its stable block ID, text its text and model the "provider/id"
	// that wrote it. It only observes and must not wait for extensions.
	BlockEnd(kind, id, text, model string)
	// StepEnd reports a completed model response: its usage and timing,
	// and the "provider/id" that wrote it. It only observes.
	StepEnd(e StepEnd, model string)
}

// MCPServers names the MCP servers configured for a session, sorted. They
// are reached through the shell ("atto mcp ..."), so the model needs
// nothing but that one line in its prompt. Implemented by package mcp.
type MCPServers interface {
	PromptServers() []string
}

// ExtensionEvent prefixes the event of the notices extensions cause.
const ExtensionEvent = "extension "

// HookNotice reports something a hook did, for display.
type HookNotice struct {
	Event   string
	Message string
	Blocked bool
}

// ErrPromptBlocked is returned when a UserPromptSubmit hook rejects input.
var ErrPromptBlocked = errors.New("prompt blocked by hook")

// StopHookPrefix starts the message that carries a Stop hook's reason to
// keep working.
const StopHookPrefix = "[Stop hook] "

// MaxStopBlocks caps how often Stop hooks may keep one turn going. A hook
// that ignores stop_hook_active would otherwise loop forever.
const MaxStopBlocks = 8

// ErrStoppedByHook is returned when a hook asks to stop the turn.
var ErrStoppedByHook = errors.New("stopped by hook")

func emitHook(emit func(any), event string, o HookOutcome) {
	for _, n := range o.Notices {
		emit(HookNotice{Event: event, Message: n})
	}
	switch {
	case o.Stop:
		emit(HookNotice{Event: event, Message: "stopped the turn: " + o.StopReason, Blocked: true})
	case o.Block:
		emit(HookNotice{Event: event, Message: o.Reason, Blocked: true})
	}
}

// ErrMaxSteps is returned by Run when MaxSteps model calls were made.
var ErrMaxSteps = errors.New("stopped: reached the maximum number of steps")

// Events emitted during Run and Compact.
type (
	ReasoningDelta struct{ Text string }
	TextDelta      struct{ Text string }
	// ToolDraft fires while the model is still writing a tool call: when it
	// begins (empty Args) and as its arguments stream in, with what could
	// be read of them so far. Index is the call's position in the
	// response. Providers that send a call whole emit none. Every draft
	// ends with the ToolStart of its Index or a ToolDraftEnd.
	ToolDraft struct {
		Index int
		Args  BashArgs
	}
	// ToolDraftEnd fires when a drafted call will not run: it was
	// malformed, unknown or blocked (Err says why), or the response ended
	// early (Err is empty).
	ToolDraftEnd struct {
		Index int
		Err   string
	}
	// ToolStart fires when a bash command begins executing. Index is the
	// call's position in the response, which names its ToolDraft.
	ToolStart struct {
		ID      string
		Index   int
		Args    BashArgs
		Timeout time.Duration
	}
	ToolOutput struct {
		ID    string
		Chunk string
	}
	// ToolEnd fires when the command has ended. Text is the result as
	// the model receives it, before PostToolUse hooks add to it, and
	// Images what atto view attached to it.
	ToolEnd struct {
		ID     string
		Result BashResult
		Text   string
		Images []provider.Image
	}
	// MessageSaved fires when the model's response is complete and recorded,
	// before StepEnd (and on its own for a response cut short by an error).
	// EntryID names it: the session entry's ID, or when nothing is recorded
	// an ID that is unique for the agent. The text and reasoning items of the
	// response are the blocks session.BlockID(sessionID, EntryID, ...) names.
	MessageSaved struct{ EntryID string }
	// UserMessageSaved gives the input shown before the run its persisted entry ID.
	UserMessageSaved struct{ EntryID string }
	// StepEnd fires after each model response. Context is the estimated
	// context size afterwards. TTFT is the time from sending the request
	// to its first streamed output, Generation from there to the end of
	// the stream (both zero when nothing streamed).
	StepEnd struct {
		Usage      provider.Usage
		Context    int
		TTFT       time.Duration
		Generation time.Duration
	}
	// SteerCommitted fires when steering messages are added to the
	// conversation of the running turn.
	SteerCommitted struct {
		Texts    []string
		EntryIDs []string // persisted entries, one per text when recording
		User     []bool   // set by a front end that knows which steers the user sent
	}
	// CompactStart, CompactDelta and CompactEnd bracket a compaction.
	// A cap that lowered an automatic compaction's trigger is named by
	// Reason (ReasonPriceTier or ReasonSetting) and Cap, the token size.
	CompactStart struct {
		Auto   bool
		Reason string
		Cap    int
	}
	CompactDelta struct{ Text string }
	// CompactTrimmed: the compaction request left out the oldest Messages
	// to fit the request limits (the conversation keeps them until the
	// compaction replaces it).
	CompactTrimmed struct{ Messages int }
	CompactEnd     struct {
		Notes         string
		Before, After int // estimated context tokens
		Elapsed       time.Duration
	}
)

type Agent struct {
	NoGoals bool // frontend has no goal continuation driver; set before SetStart
	Cwd     string
	// Shell runs the model's one tool: bash on Unix, PowerShell on Windows.
	Shell shell.Shell

	// Model settings may change from the UI while a turn runs; they are
	// read once per request.
	cfgMu         sync.Mutex
	client        provider.Streamer
	model         config.ModelRef
	effort        string
	env           []string // extra environment for bash commands
	lastReq       []byte   // most recent request body, for inspection
	longContext   bool
	compactLimits map[string]int
	sessID        string // sent to providers that route by session

	// Record, if set, receives every change to the conversation, for
	// persistence. Called on the goroutine running Run/Compact.
	Record func(session.Entry)
	// EntryID, if set, returns the ID of the entry Record wrote last; it
	// names the blocks of the assistant message just recorded.
	EntryID func() string
	// lastEntry and blockSeq make block IDs when nothing is recorded. Used
	// on the goroutine running the turn.
	lastEntry string
	blockSeq  int

	// The system prompt and what it was built from (a snapshot taken at
	// session start and on Reload). srcMu guards writes, and reads from
	// other goroutines than the one running the turn.
	srcMu    sync.Mutex
	system   string
	start    time.Time
	sources  Sources
	messages []provider.Message
	// MaxSteps stops a turn after this many model calls (0: unlimited).
	MaxSteps int
	// Hooks, if set, run around prompts, tool calls, stops and compaction.
	Hooks Hooks
	// Extensions, if set, run around prompts, tool calls and turns, after
	// the hooks (see Extensions).
	Extensions Extensions
	// MCP, if set, names the configured MCP servers for the system prompt
	// (see MCPServers). They are read when the prompt is built (at session
	// start and on Reload), not per request, so the prompt only changes
	// when the configuration does.
	MCP MCPServers
	// Worker, if set, marks an agent working for another (atto agent): its
	// prompt says so and carries the preset's instructions. Set it before
	// SetStart (core.Bind).
	Worker *Worker

	// LastUsage is the most recent nonzero usage reported by the model.
	LastUsage provider.Usage
	// sinceUsage counts estimated characters appended after the last reported usage.
	sinceUsage int

	steerMu  sync.Mutex
	steers   []string
	boundary []func() string
	// inputNote goes with the next user message (SetInputNote).
	inputNote string
	// sent is the user message RunWithImages added last, as the user typed
	// it, so the same input sent again after a failed turn is not added twice.
	sent sentInput
	// SteerNote, if set, gives text that goes at the end of a steer when it
	// is committed ("" for none): context atto adds about the session's
	// state, for the model only (the goal is running). It runs on the turn's
	// goroutine, so it must be safe to call while the front end runs.
	SteerNote func(steer string) string
	atStop    atomic.Bool // the boundary running is the model's stop (AtStop)
	// stopReq: end the running turn at its next step boundary (StopAtBoundary).
	stopReq atomic.Bool

	// DiscardPartial makes an interrupted model call leave nothing behind,
	// instead of its streamed text (experimental: set when the run goes on
	// in another process, which repeats the call).
	DiscardPartial atomic.Bool

	// bg, while a command runs, moves it to the background (Background).
	bgMu sync.Mutex
	bg   chan struct{}
}

// Background moves the running shell command to the background, as if it
// had reached its timeout: it keeps running as a job and the model is
// told so. It reports whether a command was running that can move; the
// move itself may still fail (e.g. too many jobs), in which case the
// command runs on in the foreground. Safe to call from any goroutine.
func (a *Agent) Background() bool {
	a.bgMu.Lock()
	defer a.bgMu.Unlock()
	if a.bg == nil {
		return false
	}
	select {
	case a.bg <- struct{}{}:
	default: // already asked
	}
	return true
}

// Steer queues a message for the running turn. It is added to the
// conversation at the next step boundary: after the current tool calls
// finish, or when the model stops (in which case the turn continues).
// Safe to call from any goroutine.
func (a *Agent) Steer(text string) {
	a.steerMu.Lock()
	a.steers = append(a.steers, text)
	a.steerMu.Unlock()
}

// SetInputNote sets text that goes at the end of the next user message
// (RunWithImages), after hooks have seen it: context atto adds about the
// session's state, such as a goal that is not running. It applies once.
func (a *Agent) SetInputNote(note string) {
	a.steerMu.Lock()
	a.inputNote = note
	a.steerMu.Unlock()
}

func (a *Agent) takeInputNote() string {
	a.steerMu.Lock()
	defer a.steerMu.Unlock()
	n := a.inputNote
	a.inputNote = ""
	return n
}

// StopAtBoundary ends the running turn at its next step boundary (after the
// current tool calls, or when the model stops) as if the model had finished:
// it returns no error, and steers not yet committed stay for DrainSteers.
// Safe to call from any goroutine; a request no boundary reached before the
// turn ended is dropped when the next turn starts.
func (a *Agent) StopAtBoundary() { a.stopReq.Store(true) }

// Unsteer takes back the last steer equal to text if it has not been
// committed yet, and reports whether it did.
func (a *Agent) Unsteer(text string) bool {
	a.steerMu.Lock()
	defer a.steerMu.Unlock()
	for i, v := range slices.Backward(a.steers) {
		if v == text {
			a.steers = append(a.steers[:i:i], a.steers[i+1:]...)
			return true
		}
	}
	return false
}

// DrainSteers removes and returns steering messages not yet committed.
func (a *Agent) DrainSteers() []string {
	a.steerMu.Lock()
	defer a.steerMu.Unlock()
	s := a.steers
	a.steers = nil
	return s
}

// AtBoundary queues fn for the running turn's next step boundary: after
// the step's tool calls, or when the model stops. It runs on the turn's
// goroutine with no request in flight, so it may Reload or change Hooks,
// and before steers are committed: a non-empty result is added to the
// conversation like a steer, which keeps the turn going. Functions no
// boundary reached (the turn ended first, or it was a compaction) are
// left for TakeBoundary. Safe to call from any goroutine.
func (a *Agent) AtBoundary(fn func() string) {
	a.steerMu.Lock()
	a.boundary = append(a.boundary, fn)
	a.steerMu.Unlock()
}

// TakeBoundary removes and returns the functions queued by AtBoundary that
// have not run.
func (a *Agent) TakeBoundary() []func() string {
	a.steerMu.Lock()
	defer a.steerMu.Unlock()
	fns := a.boundary
	a.boundary = nil
	return fns
}

// AtStop reports, to a function queued with AtBoundary, that the boundary
// is the model having stopped: what it returns goes on the turn, which
// would otherwise end.
func (a *Agent) AtStop() bool { return a.atStop.Load() }

func (a *Agent) runBoundary() {
	for _, fn := range a.TakeBoundary() {
		if text := fn(); text != "" {
			a.Steer(text)
		}
	}
}

// commitSteers appends pending steers, each as a user message of its own so
// that what the user said keeps its boundaries (providers take consecutive
// user messages).
func (a *Agent) commitSteers(emit func(any)) bool {
	s := a.DrainSteers()
	if len(s) == 0 {
		return false
	}
	ids := make([]string, len(s))
	for i, text := range s {
		if a.SteerNote != nil {
			if n := a.SteerNote(text); n != "" {
				text += "\n\n" + n
			}
		}
		a.appendMessage(provider.Message{Role: "user", Content: text}, session.Entry{})
		if a.EntryID != nil {
			ids[i] = a.EntryID()
		}
	}
	emit(SteerCommitted{Texts: s, EntryIDs: ids})
	return true
}

func New(model config.ModelRef, effort, cwd string) *Agent {
	// A default ID so session-routed providers work even without a saved
	// session (e.g. atto -p); SetSession replaces it.
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	a := &Agent{Cwd: cwd, Shell: shell.Default(), effort: effort, sessID: hex.EncodeToString(id)}
	a.SetModel(model)
	a.SetStart(time.Now())
	return a
}

// SetStart rebuilds the system prompt for a session that started at t.
// The prompt embeds the session's start date rather than today's, so it
// stays byte-identical for the whole session (and across resumes) and the
// prefix cache survives midnight. Call only while no turn is running.
func (a *Agent) SetStart(t time.Time) {
	// Skills are listed once here and not rescanned, like AGENTS files, so
	// the prompt (and the prefix cache) holds for the whole session; only
	// Reload reads them again.
	src, prompt := a.scan(t)
	a.srcMu.Lock()
	a.start, a.sources, a.system = t, src, prompt
	a.srcMu.Unlock()
}

// Reload reads the AGENTS files and skills again and rebuilds the system
// prompt for the session's start date. The prompt is replaced only when its
// text changed, which is what Reload reports: an unchanged prompt keeps the
// prefix cache. Call it while no request is in flight: when no turn runs,
// or from a step boundary (AtBoundary).
func (a *Agent) Reload() (changed bool) {
	a.srcMu.Lock()
	start, old := a.start, a.system
	a.srcMu.Unlock()
	src, prompt := a.scan(start)
	a.srcMu.Lock()
	defer a.srcMu.Unlock()
	a.sources = src
	if prompt == old {
		return false
	}
	a.system = prompt
	a.sinceUsage = max(0, a.sinceUsage+len(prompt)-len(old))
	return true
}

// Worker describes an agent working for another one (atto agent).
type Worker struct {
	Name, Preset string
	Instructions string // its role's
	// ID is the agent's own session ID; Path where it is in its tree
	// (/root for a root, /root/tests under it). Parent and ParentID name the
	// agent that started it, both "" for an agent started from a shell.
	ID, Path, Parent, ParentID string
	// Worktree and Branch: the git worktree it works in, with -worktree.
	Worktree, Branch string
}

// workerPart is the prompt's paragraph about agents: for an agent, what
// it is; for any session (an agent too) that may start
// agents, how, with the roles. It has no trailing newline.
func workerPart(sub *Worker, presets []agentstate.Preset) string {
	var parts []string
	if sub != nil {
		parts = append(parts, prompts.Render("agent", prompts.Agent{Name: sub.Name, Preset: sub.Preset, Instructions: sub.Instructions, ID: sub.ID, Path: sub.Path, Parent: sub.Parent, ParentID: sub.ParentID, Worktree: sub.Worktree, Branch: sub.Branch}))
	}
	list := strings.TrimSuffix(agentstate.PromptList(presets), "\n")
	parts = append(parts, prompts.Render("agent_parent", map[string]any{"Presets": list}))
	return strings.Join(parts, "\n\n")
}

// Sources is what the system prompt was built from.
type Sources struct {
	Cwd          string
	Shell        string // the shell's path
	Start        time.Time
	Instructions []Instruction // in prompt order
	Skipped      []SkippedInstruction
	Skills       []skills.Skill
	SkillIssues  []skills.Issue
	SkillDirs    []string // searched, highest priority first
	// Sizes of the prompt and of its parts, in bytes.
	PromptBytes, InstructionBytes, SkillBytes int
}

// scan reads the files the system prompt is built from and builds it.
func (a *Agent) scan(start time.Time) (Sources, string) {
	dirs := skills.Dirs(config.SkillsDir(), projectRoot(a.Cwd))
	sk, issues := skills.LoadIssues(dirs)
	var disabled []string
	st, _ := config.LoadSettings()
	if st.Skills != nil {
		disabled = st.Skills.Disabled
	}
	sk, bissues := skills.WithBuiltin(sk, config.SkillsCacheDir(), disabled)
	issues = append(issues, bissues...)
	files, skipped := scanInstructions(a.Cwd)
	var mcp []string
	if a.MCP != nil {
		mcp = a.MCP.PromptServers()
	}
	presets, _ := agentstate.LoadPresets(agentstate.Dirs(a.Cwd, projectRoot(a.Cwd)))
	sub := workerPart(a.Worker, presets)
	prompt := buildPrompt(a.Cwd, a.Shell, start, sk, files, mcp, sub, a.NoGoals)
	var instr strings.Builder
	writeInstructions(&instr, files)
	return Sources{
		Cwd: a.Cwd, Shell: a.Shell.Path, Start: start,
		Instructions: describeInstructions(files), Skipped: skipped,
		Skills: sk, SkillIssues: issues, SkillDirs: dirs,
		PromptBytes: len(prompt), InstructionBytes: instr.Len(),
		SkillBytes: len(skills.FormatForPrompt(sk, a.Shell.ToolName())),
	}, prompt
}

// Sources returns what the system prompt in use was built from.
func (a *Agent) Sources() Sources {
	a.srcMu.Lock()
	defer a.srcMu.Unlock()
	return a.sources
}

// Skills returns the skills found when the session started (or on the
// last Reload), and warnings about skill files that were invalid or
// shadowed.
func (a *Agent) Skills() ([]skills.Skill, []string) {
	a.srcMu.Lock()
	defer a.srcMu.Unlock()
	var warns []string
	for _, is := range a.sources.SkillIssues {
		warns = append(warns, is.String())
	}
	return a.sources.Skills, warns
}

// SetSession sets the session ID (for provider routing headers) and extra
// environment variables for bash commands.
func (a *Agent) SetSession(id string, env []string) {
	a.cfgMu.Lock()
	a.sessID, a.env = id, env
	a.cfgMu.Unlock()
}

// SetModel switches the model; history is kept. Takes effect on the next
// request.
func (a *Agent) SetModel(m config.ModelRef) {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	a.model = m
	onRequest := func(b []byte) {
		a.cfgMu.Lock()
		a.lastReq = b
		a.cfgMu.Unlock()
	}
	a.client = &provider.Client{
		Model:     m.AIModel(),
		APIKey:    m.APIKey,
		KeyFunc:   m.KeyFunc,
		Headers:   m.RequestHeaders(),
		OnRequest: onRequest,
	}
	if lv := m.Model.Levels(); len(lv) > 0 && !slices.Contains(lv, a.effort) {
		a.effort = lv[len(lv)/2]
	}
}

// SetEffort changes the reasoning effort for the next request.
func (a *Agent) SetEffort(e string) {
	a.cfgMu.Lock()
	a.effort = e
	a.cfgMu.Unlock()
}

// LastRequest returns the body of the most recent request sent.
func (a *Agent) LastRequest() []byte {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	return a.lastReq
}

// Breakdown sizes the parts of the context, in characters. Call only while
// no turn is running.
type Breakdown struct {
	System, Tools, User, Images, Notes, Assistant, Reasoning, ToolCalls, ToolResults int
	Messages                                                                         int
}

func (b Breakdown) Total() int {
	return b.System + b.Tools + b.User + b.Images + b.Notes + b.Assistant + b.Reasoning + b.ToolCalls + b.ToolResults
}

func (a *Agent) Breakdown() Breakdown {
	b := Breakdown{System: len(a.SystemPrompt()), Messages: len(a.messages)}
	for _, t := range a.tools() {
		b.Tools += len(t.Function.Name) + len(t.Function.Description) + len(t.Function.Parameters)
	}
	for _, m := range a.messages {
		switch m.Role {
		case "user":
			if strings.HasPrefix(m.Content, SummaryPrefix) {
				b.Notes += len(m.Content)
			} else {
				b.User += len(m.Content)
			}
			b.Images += len(m.Images) * imageChars
		case "assistant":
			b.Assistant += len(m.Content)
			b.Reasoning += len(m.ReasoningContent)
			for _, tc := range m.ToolCalls {
				b.ToolCalls += len(tc.Function.Name) + len(tc.Function.Arguments)
			}
		case "tool":
			b.ToolResults += len(m.Content)
			b.Images += len(m.Images) * imageChars
		}
	}
	return b
}

// SystemPrompt returns the system prompt in use.
func (a *Agent) SystemPrompt() string {
	a.srcMu.Lock()
	defer a.srcMu.Unlock()
	return a.system
}

// Effort returns the effort in use.
func (a *Agent) Effort() string {
	_, e := a.Current()
	return e
}

// Current returns the model and effort in use.
func (a *Agent) Current() (config.ModelRef, string) {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	return a.model, a.effort
}

// Reset clears the conversation.
func (a *Agent) Reset() {
	a.DrainSteers()
	a.messages = nil
	a.LastUsage = provider.Usage{}
	a.sinceUsage = 0
}

// Restore rebuilds the conversation from session entries.
func (a *Agent) Restore(entries []session.Entry) {
	a.Reset()
	for _, e := range entries {
		switch e.Type {
		case session.TypeMessage:
			if e.Message == nil {
				continue
			}
			a.messages = append(a.messages, withImageData(*e.Message))
			if e.Usage != nil && (e.Usage.PromptTokens > 0 || e.Usage.CompletionTokens > 0) {
				a.LastUsage, a.sinceUsage = *e.Usage, 0
			} else {
				a.sinceUsage += messageChars(*e.Message)
			}
		case session.TypeBranchSummary:
			m := BranchSummaryMessage(e.Summary)
			a.messages = append(a.messages, m)
			a.sinceUsage += messageChars(m)
		case session.TypeBashExecution:
			if e.Bash != nil && !e.Bash.Exclude {
				m := BashExecutionMessage(*e.Bash)
				a.messages = append(a.messages, m)
				a.sinceUsage += messageChars(m)
			}
		case session.TypeCompaction:
			a.messages = nil
			for _, m := range e.Replacement {
				a.messages = append(a.messages, withImageData(m))
			}
			a.LastUsage = provider.Usage{}
			a.sinceUsage = len(a.system)
			for _, m := range a.messages {
				a.sinceUsage += messageChars(m)
			}
		}
	}
}

// withImageData loads the bytes of a restored message's images. An image
// whose file is gone stays without data and is sent as a short note.
func withImageData(m provider.Message) provider.Message {
	if len(m.Images) == 0 {
		return m
	}
	ims := make([]provider.Image, len(m.Images))
	for i, im := range m.Images {
		ims[i] = im
		if loaded, err := images.Load(im); err == nil {
			ims[i] = loaded
		}
	}
	m.Images = ims
	return m
}

// imageChars is the context an image is assumed to take, in characters:
// codex estimates 7373 bytes for an image resized to fit 2048px.
const imageChars = 7373

func messageChars(m provider.Message) int {
	n := estimateChars(m.Content) + estimateChars(m.ReasoningContent) + len(m.Images)*imageChars
	for _, tc := range m.ToolCalls {
		n += len(tc.Function.Name) + estimateChars(tc.Function.Arguments)
	}
	return n
}

// estimateChars keeps the usual four bytes per token for prose. Text with
// little whitespace and many digits or punctuation, or long unbroken runs
// (base64, minified code), gets the more conservative three bytes per token.
func estimateChars(s string) int {
	spaces, dense, run, longest := 0, 0, 0, 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\n' || c == '\r' || c == '\t' {
			spaces++
			run = 0
		} else {
			run++
			longest = max(longest, run)
			if c >= '0' && c <= '9' || strings.ContainsRune(`{}[]:,_=+/\"`, rune(c)) {
				dense++
			}
		}
	}
	if len(s) >= 64 && (longest >= 64 || spaces*8 < len(s) && dense*8 >= len(s)) {
		return (len(s)*4 + 2) / 3
	}
	return len(s)
}

// ContextTokens estimates the current context size: the last reported
// usage plus ~4 bytes per token for prose appended since (~3 for dense text).
func (a *Agent) ContextTokens() int {
	return a.LastUsage.PromptTokens + a.LastUsage.CompletionTokens + a.sinceUsage/4
}

func (a *Agent) needsCompact() bool { return a.needsCompactWith(0) }

// needsCompactWith is needsCompact for a conversation that is about to grow
// by extra characters (a message not yet appended).
func (a *Agent) needsCompactWith(extra int) bool {
	limit, _ := a.CompactionLimit()
	return limit > 0 && len(a.messages) > 0 && a.ContextTokens()+extra/4 >= limit
}

// appendMessage adds m to the conversation and records it. meta carries
// extra fields for the session entry.
func (a *Agent) appendMessage(m provider.Message, meta session.Entry) {
	a.messages = append(a.messages, m)
	a.sinceUsage += messageChars(m)
	if a.Record != nil {
		meta.Type = session.TypeMessage
		meta.Message = &m
		a.Record(meta)
	}
}

// messageSaved announces the assistant message just recorded: the event
// that gives its blocks their IDs, then the extensions' block_end events.
func (a *Agent) messageSaved(m provider.Message, emit func(any)) {
	id := ""
	if a.EntryID != nil {
		id = a.EntryID()
	}
	if id == "" || id == a.lastEntry { // nothing was recorded (no file, or read only)
		a.blockSeq++
		id = "n" + strconv.Itoa(a.blockSeq)
	} else {
		a.lastEntry = id
	}
	emit(MessageSaved{EntryID: id})
	if a.Extensions == nil {
		return
	}
	a.cfgMu.Lock()
	sid, cur := a.sessID, a.model.ProviderName+"/"+a.model.Model.ID
	a.cfgMu.Unlock()
	model := cur
	if m.Model != "" {
		model = m.Provider + "/" + m.Model
	}
	if strings.TrimSpace(m.ReasoningContent) != "" {
		a.Extensions.BlockEnd(session.BlockReasoning, session.BlockID(sid, id, session.BlockReasoning), m.ReasoningContent, model)
	}
	if strings.TrimSpace(m.Content) != "" {
		a.Extensions.BlockEnd(session.BlockText, session.BlockID(sid, id, session.BlockText), m.Content, model)
	}
}

// stepEnd tells extensions about a completed model response.
func (a *Agent) stepEnd(m provider.Message, e StepEnd) {
	if a.Extensions == nil {
		return
	}
	a.cfgMu.Lock()
	model := a.model.ProviderName + "/" + a.model.Model.ID
	a.cfgMu.Unlock()
	if m.Model != "" {
		model = m.Provider + "/" + m.Model
	}
	a.Extensions.StepEnd(e, model)
}

// ImagesUnsupported replaces images in requests to a model without image
// input (the history keeps them), as codex does.
const ImagesUnsupported = "[image content omitted because you do not support image input]"

// ImagesCompacted replaces images in the user messages kept by compaction.
const ImagesCompacted = "[image omitted by compaction]"

// withoutImages returns msgs with each image replaced by note. The caller's
// messages are not modified.
func withoutImages(msgs []provider.Message, note string) []provider.Message {
	var out []provider.Message
	for i, m := range msgs {
		if len(m.Images) == 0 {
			continue
		}
		if out == nil {
			out = append([]provider.Message(nil), msgs...)
		}
		out[i] = stripImages(m, note)
	}
	if out == nil {
		return msgs
	}
	return out
}

func stripImages(m provider.Message, note string) provider.Message {
	notes := make([]string, len(m.Images))
	for i := range notes {
		notes[i] = note
	}
	if m.Content != "" {
		m.Content += "\n"
	}
	m.Content += strings.Join(notes, "\n")
	m.Images = nil
	return m
}

func systemPrompt(cwd string, sh shell.Shell, start time.Time, sk []skills.Skill) string {
	presets, _ := agentstate.LoadPresets(agentstate.Dirs(cwd, projectRoot(cwd)))
	return buildPrompt(cwd, sh, start, sk, loadInstructions(cwd), nil, workerPart(nil, presets))
}

// sub is the paragraph about agents (see workerPart), "" for none.
// The MCP server names are sorted, so the text depends on the
// configuration alone.
func buildPrompt(cwd string, sh shell.Shell, start time.Time, sk []skills.Skill, instr []instructionFile, mcp []string, sub string, noGoals ...bool) string {
	var b strings.Builder
	name := sh.ToolName()
	b.WriteString(prompts.Render("system", prompts.System{
		NoGoals: len(noGoals) > 0 && noGoals[0],
		Kind:    string(sh.Kind),
		WinPS51: strings.EqualFold(strings.TrimSuffix(filepath.Base(sh.Path), ".exe"), "powershell"),
		Tool:    name,
		Sub:     sub,
		MCP:     strings.Join(slices.Sorted(slices.Values(mcp)), ", "),
		Cwd:     cwd,
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		Shell:   sh.Path,
		Date:    start.Format("2006-01-02"),
	}))
	b.WriteString("\n")
	writeInstructions(&b, instr)
	b.WriteString(skills.FormatForPrompt(sk, name))
	return b.String()
}

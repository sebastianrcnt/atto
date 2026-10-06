// Package agent runs the model ↔ tool loop. It knows nothing about the UI:
// progress is reported through an emit callback with the event types below.
package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sebastianrcnt/atto/ai"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/prompts"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/shell"
	"github.com/sebastianrcnt/atto/skills"
	"github.com/sebastianrcnt/atto/subagent"
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
	// StepEnd fires after each model response. Context is the estimated
	// context size afterwards.
	StepEnd struct {
		Usage   provider.Usage
		Context int
	}
	// SteerCommitted fires when steering messages are added to the
	// conversation of the running turn.
	SteerCommitted struct{ Texts []string }
	// CompactStart, CompactDelta and CompactEnd bracket a compaction.
	CompactStart struct{ Auto bool }
	CompactDelta struct{ Text string }
	// CompactTrimmed: the compaction request left out the oldest Messages
	// to fit the context window (the conversation keeps them until the
	// compaction replaces it).
	CompactTrimmed struct{ Messages int }
	CompactEnd     struct {
		Notes         string
		Before, After int // estimated context tokens
		Elapsed       time.Duration
	}
)

type Agent struct {
	Cwd string
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
	// Subagent, if set, makes this agent a subagent (atto agent): its
	// prompt says so and carries the preset's instructions. Set it before
	// SetStart (core.Bind).
	Subagent *Subagent

	// LastUsage is the usage of the most recent model call.
	LastUsage provider.Usage
	// sinceUsage counts characters appended after the last reported usage.
	sinceUsage int

	steerMu  sync.Mutex
	steers   []string
	boundary []func() string
	// inputNote goes with the next user message (SetInputNote).
	inputNote string
	// SteerNote, if set, gives text that goes at the end of a steer when it
	// is committed ("" for none): context atto adds about the session's
	// state, for the model only (the goal is running). It runs on the turn's
	// goroutine, so it must be safe to call while the front end runs.
	SteerNote func(steer string) string
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
	for _, text := range s {
		if a.SteerNote != nil {
			if n := a.SteerNote(text); n != "" {
				text += "\n\n" + n
			}
		}
		a.appendMessage(provider.Message{Role: "user", Content: text}, session.Entry{})
	}
	emit(SteerCommitted{s})
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

// Subagent describes an agent working for another one (atto agent).
type Subagent struct {
	Name, Preset string
	Instructions string // the preset's
	// Worktree and Branch: the git worktree it works in, with -worktree.
	Worktree, Branch string
}

// subagentPart is the prompt's paragraph about subagents: for a subagent,
// what it is; for any other agent, the presets it may start when
// subagents are enabled (nothing otherwise). It has no trailing newline.
func subagentPart(sub *Subagent, enabled bool, presets []subagent.Preset) string {
	if sub != nil {
		return prompts.Render("subagent", prompts.Subagent{Name: sub.Name, Preset: sub.Preset, Instructions: sub.Instructions, Worktree: sub.Worktree, Branch: sub.Branch})
	}
	if !enabled {
		return ""
	}
	list := strings.TrimSuffix(subagent.PromptList(presets), "\n")
	return prompts.Render("subagent_parent", map[string]any{"Presets": list})
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
	var presets []subagent.Preset
	if a.Subagent == nil && st.SubagentsEnabled() {
		presets, _ = subagent.LoadPresets(subagent.Dirs(a.Cwd, projectRoot(a.Cwd)))
	}
	sub := subagentPart(a.Subagent, st.SubagentsEnabled(), presets)
	prompt := buildPrompt(a.Cwd, a.Shell, start, sk, files, mcp, sub)
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

// CompactionLimit returns the trigger and the effective cap (zero: window).
func (a *Agent) CompactionLimit() (limit, cap int) {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	m := a.model
	cap = m.Model.Cost.ContextPriceBoundary()
	if n, ok := a.compactLimits[m.ProviderName+"/"+m.Model.ID]; ok && n >= 0 {
		cap = n
	}
	if a.longContext || cap >= m.Model.ContextWindow {
		cap = 0
	}
	return compactLimit(m.Model, cap), cap
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
			if e.Usage != nil {
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
	n := len(m.Content) + len(m.ReasoningContent) + len(m.Images)*imageChars
	for _, tc := range m.ToolCalls {
		n += len(tc.Function.Name) + len(tc.Function.Arguments)
	}
	return n
}

// ContextTokens estimates the current context size: the last reported
// usage plus ~4 characters per token for anything appended since.
func (a *Agent) ContextTokens() int {
	return a.LastUsage.PromptTokens + a.LastUsage.CompletionTokens + a.sinceUsage/4
}

func (a *Agent) needsCompact() bool {
	limit, _ := a.CompactionLimit()
	return limit > 0 && len(a.messages) > 0 && a.ContextTokens() >= limit
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

func (a *Agent) tools() []provider.Tool {
	return []provider.Tool{{
		Type: "function",
		Function: provider.ToolFunction{
			Name:        a.Shell.ToolName(),
			Description: toolDescription(a.Shell),
			Parameters:  bashSchema,
		},
	}}
}

// request builds a request and returns the client to send it with.
func (a *Agent) request(extra ...provider.Message) (provider.Streamer, provider.Request) {
	model, effort := a.Current()
	a.cfgMu.Lock()
	client, sessID := a.client, a.sessID
	a.cfgMu.Unlock()
	msgs := make([]provider.Message, 0, len(a.messages)+len(extra)+1)
	msgs = append(msgs, provider.Message{Role: "system", Content: a.SystemPrompt()})
	msgs = append(msgs, a.messages...)
	msgs = append(msgs, extra...)
	if !model.Model.Images() {
		msgs = withoutImages(msgs, ImagesUnsupported)
	}
	return client, provider.Request{
		SessionID: sessID,
		Model:     model.Model.ID,
		Messages:  msgs,
		Tools:     a.tools(),
		Effort:    effort,
		MaxTokens: model.Model.MaxTokens,
	}
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

// Run sends input and loops through tool calls until the model stops.
// Compaction runs automatically before the turn and between tool calls
// when the context passes AutoCompactLimit.
func (a *Agent) Run(ctx context.Context, input string, emit func(any)) error {
	return a.RunWithImages(ctx, input, nil, emit)
}

// modelChangeNote tells the model that the conversation's last reply came
// from another model, so it doesn't take that reply's words or habits for
// its own. It goes with the next user message only: once this model has
// replied, the last reply is its own.
func (a *Agent) modelChangeNote() string {
	a.cfgMu.Lock()
	cur := a.model.ProviderName + "/" + a.model.Model.ID
	a.cfgMu.Unlock()
	for _, m := range slices.Backward(a.messages) {
		if m.Role != "assistant" {
			continue
		}
		if m.Model == "" { // written before atto recorded models
			return ""
		}
		if prev := m.Provider + "/" + m.Model; prev != cur {
			return fmt.Sprintf("[atto] The model changed from %s to %s. Earlier assistant messages were written by %s.", prev, cur, prev)
		}
		return ""
	}
	return ""
}

// RunWithImages is Run with images attached to the user message. Their
// bytes must be loaded, and saved with images.Save for the session to
// resume with them.
func (a *Agent) RunWithImages(ctx context.Context, input string, imgs []provider.Image, emit func(any)) (err error) {
	note := a.takeInputNote()
	if a.Extensions != nil {
		a.Extensions.TurnStart(input)
		defer func() { a.Extensions.TurnEnd(err) }()
	}
	if a.Hooks != nil {
		o := a.Hooks.UserPromptSubmit(ctx, input)
		emitHook(emit, "UserPromptSubmit", o)
		switch {
		case o.Stop:
			return ErrStoppedByHook
		case o.Block:
			return ErrPromptBlocked
		case o.Context != "":
			input += "\n\n" + o.Context
		}
	}
	if a.Extensions != nil {
		o := a.Extensions.UserPrompt(ctx, input)
		emitHook(emit, ExtensionEvent+"user_prompt", o)
		switch {
		case o.Block:
			return ErrPromptBlocked
		case o.Context != "":
			input += "\n\n" + o.Context
		}
	}
	if n := a.modelChangeNote(); n != "" {
		input += "\n\n" + n
	}
	if note != "" {
		input += "\n\n" + note
	}
	if a.needsCompact() {
		if err := a.compact(ctx, emit, true); err != nil {
			return err
		}
	}
	a.appendMessage(provider.Message{Role: "user", Content: input, Images: imgs}, session.Entry{})
	return a.loop(ctx, emit)
}

// Continue runs the rest of a turn that was stopped, without a new user
// message: the conversation ends with the user's message or tool results
// the model has not answered yet (experimental, for runs left in the
// background).
func (a *Agent) Continue(ctx context.Context, emit func(any)) error {
	return a.loop(ctx, emit)
}

// loop is the turn: model calls and tool calls until the model stops.
func (a *Agent) loop(ctx context.Context, emit func(any)) error {
	stopBlocks := 0 // Stop hook continuations in this turn
	a.stopReq.Store(false)

	for step := 1; ; step++ {
		if a.MaxSteps > 0 && step > a.MaxSteps {
			return ErrMaxSteps
		}
		var thinkStart, thinkEnd time.Time
		drafts := &draftTracker{emit: emit}
		h := provider.Handler{
			OnToolCallStart: drafts.start,
			OnToolCallDelta: drafts.delta,
			OnReasoning: func(s string) {
				if thinkStart.IsZero() {
					thinkStart = time.Now()
				}
				emit(ReasoningDelta{s})
			},
			OnText: func(s string) {
				if !thinkStart.IsZero() && thinkEnd.IsZero() {
					thinkEnd = time.Now()
				}
				emit(TextDelta{s})
			},
		}
		client, req := a.request()
		res, err := client.Stream(ctx, req, h)
		var thinkMs int64
		if !thinkStart.IsZero() {
			if thinkEnd.IsZero() {
				thinkEnd = time.Now()
			}
			thinkMs = thinkEnd.Sub(thinkStart).Milliseconds()
		}
		if err != nil {
			// Keep partial text so the transcript matches what the user saw,
			// but drop half-formed tool calls.
			drafts.endAll()
			if res.Message.Content != "" && !a.DiscardPartial.Load() {
				res.Message.ToolCalls = nil
				a.appendMessage(res.Message, session.Entry{ThinkingMs: thinkMs})
				a.messageSaved(res.Message, emit)
			}
			return err
		}
		usage := res.Usage
		a.appendMessage(res.Message, session.Entry{Usage: &usage, ThinkingMs: thinkMs})
		a.messageSaved(res.Message, emit)
		a.LastUsage, a.sinceUsage = usage, 0
		emit(StepEnd{Usage: usage, Context: a.ContextTokens()})

		if len(res.Message.ToolCalls) == 0 {
			if a.stopReq.Swap(false) {
				return nil
			}
			a.runBoundary()
			if a.commitSteers(emit) {
				continue
			}
			if a.Hooks != nil {
				// An interrupted turn does not run Stop hooks, as in Claude Code.
				if err := ctx.Err(); err != nil {
					return err
				}
				// A Stop hook may block stopping and give the model a reason
				// to keep working. stop_hook_active tells the hook it already
				// did; after MaxStopBlocks the turn ends anyway.
				o := a.Hooks.Stop(ctx, stopBlocks > 0)
				if o.Block && !o.Stop && stopBlocks >= MaxStopBlocks {
					o.Block = false
					o.Notices = append(o.Notices, fmt.Sprintf("Stop hooks blocked %d times in a row; stopping anyway", MaxStopBlocks))
				}
				emitHook(emit, "Stop", o)
				if o.Block && !o.Stop {
					stopBlocks++
					a.appendMessage(provider.Message{Role: "user", Content: StopHookPrefix + o.Reason}, session.Entry{})
					continue
				}
			}
			return nil
		}
		stopTurn := false
		for i, tc := range res.Message.ToolCalls {
			var content string
			var imgs []provider.Image
			var meta session.Entry
			if ctx.Err() != nil {
				content = "[canceled by user]"
				meta.Tool = &session.ToolMeta{Canceled: true, ExitCode: -1}
				drafts.end(i, "")
			} else {
				var stop bool
				content, imgs, meta.Tool, stop = a.runTool(ctx, tc, i, drafts, emit)
				stopTurn = stopTurn || stop
			}
			a.appendMessage(provider.Message{Role: "tool", ToolCallID: tc.ID, Content: content, Images: imgs}, meta)
		}
		drafts.endAll() // drafts beyond the calls the response ended up with
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if stopTurn {
			return ErrStoppedByHook
		}
		if a.stopReq.Swap(false) {
			return nil
		}
		a.runBoundary()
		a.commitSteers(emit)
		if a.needsCompact() {
			if err := a.compact(ctx, emit, true); err != nil {
				return err
			}
		}
	}
}

// runTool runs one call and returns its result for the model: the text,
// the images atto view attached, how it ran, and whether a hook stopped
// the turn.
func (a *Agent) runTool(ctx context.Context, tc provider.ToolCall, index int, drafts *draftTracker, emit func(any)) (string, []provider.Image, *session.ToolMeta, bool) {
	fail := func(msg string) (string, []provider.Image, *session.ToolMeta, bool) {
		drafts.end(index, msg)
		return "error: " + msg, nil, &session.ToolMeta{ExitCode: -1}, false
	}
	// Accept any shell tool name: a session started on another OS, or a
	// model calling "bash" out of habit, still runs in this machine's shell.
	if name := a.Shell.ToolName(); tc.Function.Name != name && !shellToolNames[tc.Function.Name] {
		return fail(fmt.Sprintf("unknown tool %q; the only tool is %s", tc.Function.Name, name))
	}
	var args BashArgs
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		return fail("invalid arguments: " + err.Error())
	}
	if strings.TrimSpace(args.Command) == "" {
		return fail("command is empty")
	}
	if args.Description == "" {
		args.Description = FirstLine(args.Command)
	}
	if a.Hooks != nil {
		updated, o := a.Hooks.PreToolUse(ctx, args)
		emitHook(emit, "PreToolUse", o)
		if o.Stop {
			drafts.end(index, "stopped by hook")
			return "[stopped by hook: " + o.StopReason + "]", nil, &session.ToolMeta{Description: args.Description, ExitCode: -1}, true
		}
		if o.Block {
			drafts.end(index, "blocked by hook")
			return "Blocked by a PreToolUse hook: " + o.Reason, nil, &session.ToolMeta{Description: args.Description, ExitCode: -1}, false
		}
		args = updated
	}
	if a.Extensions != nil {
		updated, o := a.Extensions.ToolCall(ctx, args)
		emitHook(emit, ExtensionEvent+"tool_call", o)
		if o.Block {
			drafts.end(index, "blocked by extension")
			return "Blocked by an extension: " + o.Reason, nil, &session.ToolMeta{Description: args.Description, ExitCode: -1}, false
		}
		args = updated
	}
	emit(ToolStart{ID: tc.ID, Index: index, Args: args, Timeout: args.timeout()})
	drafts.claim(index)
	a.cfgMu.Lock()
	env := a.env
	a.cfgMu.Unlock()
	// A foreground call gets a directory for atto view, whose images are
	// attached to its result. Other commands get none (the variable is
	// cleared, in case atto's own environment has one).
	viewDir := ""
	if !args.Background {
		if d, err := os.MkdirTemp("", "atto-view-"); err == nil {
			viewDir = d
			defer os.RemoveAll(d)
		}
	}
	env = append(slices.Clip(env), config.EnvView+"="+viewDir)
	var bg chan struct{}
	if ShellHost && !args.Background && envValue(env, "ATTO_SESSION_ID") != "" {
		bg = make(chan struct{}, 1)
		a.bgMu.Lock()
		a.bg = bg
		a.bgMu.Unlock()
	}
	res := runShell(ctx, a.Shell, a.Cwd, env, args, func(s string) { emit(ToolOutput{ID: tc.ID, Chunk: s}) }, bg)
	if bg != nil {
		a.bgMu.Lock()
		a.bg = nil
		a.bgMu.Unlock()
	}
	imgs, note := a.viewed(viewDir)
	if note != "" {
		chunk := note + "\n"
		if res.Output != "" && !strings.HasSuffix(res.Output, "\n") {
			chunk = "\n" + chunk
		}
		res.Output += chunk
		emit(ToolOutput{ID: tc.ID, Chunk: chunk})
	}
	out := res.ForModel(args)
	if a.Extensions != nil {
		var o HookOutcome
		out, o = a.Extensions.ToolResult(ctx, args, res, out)
		emitHook(emit, ExtensionEvent+"tool_result", o)
	}
	emit(ToolEnd{ID: tc.ID, Result: res, Text: out, Images: imgs})
	stop := false
	if a.Hooks != nil {
		o := a.Hooks.PostToolUse(ctx, args, res, out)
		emitHook(emit, "PostToolUse", o)
		if o.Block && o.Reason != "" {
			out += "\n[PostToolUse hook] " + o.Reason
		}
		if o.Context != "" {
			out += "\n[PostToolUse hook] " + o.Context
		}
		stop = o.Stop
	}
	return out, imgs, &session.ToolMeta{
		Description: args.Description,
		ExitCode:    res.ExitCode,
		DurationMs:  res.Duration.Milliseconds(),
		TimedOut:    res.TimedOut,
		Canceled:    res.Canceled,
		Job:         res.Job,
		Background:  res.Background,
	}, stop
}

// ViewUnsupported is added to the result of a command that ran atto view
// when the model takes no image input: nothing is attached.
const ViewUnsupported = "[atto view: this model can't view images, so nothing was attached. Work from text instead, or ask the user to switch to a model with image input.]"

// viewed collects the images atto view left in dir during a call and
// stores them for the session. note, if not empty, is for the model: why
// images were not attached.
func (a *Agent) viewed(dir string) (imgs []provider.Image, note string) {
	if dir == "" {
		return nil, ""
	}
	imgs, err := images.Collect(dir)
	if err != nil {
		note = "[atto view: not attached: " + err.Error() + "]"
	}
	if len(imgs) == 0 {
		return nil, note
	}
	if m, _ := a.Current(); !m.Model.Images() {
		return nil, ViewUnsupported
	}
	for _, im := range imgs {
		// Without its file a resumed session sends a note instead.
		_ = images.Save(im)
	}
	return imgs, note
}

// FirstLine is the first line of a command, the description of a call
// that gave none.
func FirstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// CompactNoteWords bounds the length of handoff notes.
const CompactNoteWords = 700

// SummaryPrefix introduces handoff notes in the compacted history.
var SummaryPrefix = prompts.Render("compact_prefix", nil) + "\n\n"

// keepUserTokens is how much recent user text survives compaction (codex
// keeps 20k tokens of user messages).
const keepUserTokens = 20000

// Compact replaces the conversation with handoff notes written by the model.
func (a *Agent) Compact(ctx context.Context, emit func(any)) error {
	return a.compact(ctx, emit, false)
}

// compact asks the model for handoff notes. The request reuses the full
// existing prefix (system, tools, history) and appends the instruction at the
// end, so the prefix cache stays warm. The new history is the most recent
// user messages (up to keepUserTokens) followed by the notes.
func (a *Agent) compact(ctx context.Context, emit func(any), auto bool) error {
	if len(a.messages) == 0 {
		return fmt.Errorf("nothing to compact")
	}
	if a.Hooks != nil {
		emitHook(emit, "PreCompact", a.Hooks.PreCompact(ctx, auto))
	}
	start := time.Now()
	before := a.ContextTokens()
	emit(CompactStart{Auto: auto})

	prompt := provider.Message{Role: "user", Content: prompts.Render("compact", map[string]any{"Words": CompactNoteWords})}
	client, req := a.request(prompt)
	req.ToolChoice = "none"
	model, _ := a.Current()
	est := before + messageChars(prompt)/4
	var res provider.Result
	var err error
	try, sent := 0, 0
	cut := ""
	for again := 0; ; again++ {
		for ; ; try++ {
			// The conversation is at its limit by now, and one large tool result
			// can take it past the point where the request plus a full-size
			// answer fits: give the notes the room that is left, and when that
			// is too little drop the oldest turns from the request (codex trims
			// history the same way). The conversation itself is untouched.
			need := compactRoom << try
			if dropped := fitCompaction(&req, model.Model, est, need); dropped > 0 {
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
		return fmt.Errorf("compaction failed: %w", err)
	}
	if cut != "" {
		return fmt.Errorf("compaction failed: the handoff notes were cut off (%s), twice; the conversation is unchanged", cut)
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

	a.messages = replacement
	a.LastUsage = provider.Usage{}
	a.sinceUsage = len(a.system)
	for _, m := range replacement {
		a.sinceUsage += messageChars(m)
	}
	after, elapsed := a.ContextTokens(), time.Since(start)
	if a.Record != nil {
		a.Record(session.Entry{Type: session.TypeCompaction, Replacement: replacement, Notes: notes, TokensBefore: before,
			TokensAfter: after, ElapsedMs: elapsed.Milliseconds(), Auto: auto, Finish: res.FinishReason})
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
// call keeps its result) until need fits. It returns how many messages it
// dropped.
func fitCompaction(req *provider.Request, m config.Model, est, need int) int {
	window := m.ContextWindow
	if window <= 0 {
		return 0
	}
	const margin = 256 // token estimates are rough; servers add a few of their own
	dropped := 0
	msgs := req.Messages // [system, conversation..., compaction prompt]
	for window-est-margin < need && len(msgs) > 3 {
		// Drop up to (not including) the next user message after the first.
		end := 2
		for end < len(msgs)-1 && msgs[end].Role != "user" {
			end++
		}
		if end >= len(msgs)-1 { // one turn left: keep it
			break
		}
		for _, d := range msgs[1:end] {
			est -= messageChars(d) / 4
		}
		dropped += end - 1
		msgs = append(msgs[:1:1], msgs[end:]...)
	}
	req.Messages = msgs
	if room := window - est - margin; req.MaxTokens <= 0 || req.MaxTokens > room {
		req.MaxTokens = max(room, 1024)
	}
	return dropped
}

// contextExceeded reports whether err says the request didn't fit the
// model's context window.
func contextExceeded(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "context") && (strings.Contains(s, "exceed") || strings.Contains(s, "too long") ||
		strings.Contains(s, "maximum") || strings.Contains(s, "no room"))
}

var shellToolNames = map[string]bool{"bash": true, "powershell": true, "shell": true, "cmd": true}

func systemPrompt(cwd string, sh shell.Shell, start time.Time, sk []skills.Skill) string {
	return buildPrompt(cwd, sh, start, sk, loadInstructions(cwd), nil, "")
}

// sub is the paragraph about subagents (see subagentPart), "" for none.
// The MCP server names are sorted, so the text depends on the
// configuration alone.
func buildPrompt(cwd string, sh shell.Shell, start time.Time, sk []skills.Skill, instr []instructionFile, mcp []string, sub string) string {
	var b strings.Builder
	name := sh.ToolName()
	b.WriteString(prompts.Render("system", prompts.System{
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

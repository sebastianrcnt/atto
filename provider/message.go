// Package provider is atto's side of the model APIs: the transcript types
// stored in session files (Message, ToolCall, Image, Usage) and Client,
// which sends a transcript through package ai (the port of pi-ai) and
// streams the reply back as deltas.
package provider

import (
	"context"
	"encoding/json"
)

// UserAgent identifies atto to providers (some CDNs reject generic agents).
var UserAgent = "atto"

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// Message is one transcript entry, persisted in session files. Its shape
// predates package ai (it follows chat completions); Client converts it to
// ai messages per request.
type Message struct {
	Role             string     `json:"role"`
	Content          string     `json:"content"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	// Reasoning carries opaque Responses API reasoning items so they can be
	// replayed on later requests. Chat completions never sees it.
	Reasoning *ReasoningState `json:"responses_reasoning,omitempty"`
	// Images attached to a user message, sent after its text, or to a
	// tool result (atto view).
	Images []Image `json:"images,omitempty"`

	// Provider, API and Model record which model wrote an assistant
	// message (pi: AssistantMessage provider/api/model). pi treats other
	// models' thinking and tool call ids differently; messages written
	// before atto recorded this count as the current model's.
	Provider string `json:"provider,omitempty"`
	API      string `json:"api,omitempty"`
	Model    string `json:"model,omitempty"`
	// ThinkingSignature is pi's thinkingSignature for chat completions
	// reasoning: the delta field it arrived in ("reasoning",
	// "reasoning_text"; empty means "reasoning_content") or OpenRouter
	// reasoning_details JSON.
	ThinkingSignature string `json:"thinkingSignature,omitempty"`
	// TextSignature is pi's textSignature: the Responses message id/phase.
	TextSignature string `json:"textSignature,omitempty"`
}

// ReasoningState holds the encrypted reasoning items an assistant turn
// produced. With store:false the server keeps no state, so these are sent
// back verbatim before the turn's message and tool calls. They are only
// valid for the model that made them.
type ReasoningState struct {
	Model string            `json:"model"`
	Items []json.RawMessage `json:"items"`
}

type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// Usage is one response's token counts. PromptTokens includes the cached
// (read) and cache-write ones.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
	// CacheWriteTokens and Cost (US dollars, 0 when the model has no
	// prices) were added later; older sessions have neither.
	CacheWriteTokens int     `json:",omitempty"`
	Cost             float64 `json:",omitempty"`
}

type Request struct {
	SessionID  string // routing headers, prompt_cache_key; "$session" in headers
	Model      string
	Messages   []Message
	Tools      []Tool
	ToolChoice string // "", "auto", "none"
	Effort     string // reasoning effort; "off" disables thinking
	MaxTokens  int
}

// Handler receives streaming deltas. Any field may be nil.
type Handler struct {
	OnReasoning func(delta string)
	OnText      func(delta string)
	OnToolCall  func(index int, id, name string)
	// OnToolCallStart fires when the model begins a tool call, before its
	// arguments, and OnToolCallDelta for each piece of them with the
	// arguments JSON received so far (possibly incomplete). index counts
	// calls in the order they start, which is their order in the result.
	// The id and name may not be known yet; OnToolCall reports them once
	// the call is complete. Providers that send a call whole may skip the
	// deltas.
	OnToolCallStart func(index int)
	OnToolCallDelta func(index int, args string)
}

type Result struct {
	Message      Message
	FinishReason string // "stop", "length" or "tool_calls"
	Usage        Usage
}

// Streamer sends one request to a model server and streams the reply.
type Streamer interface {
	Stream(ctx context.Context, req Request, h Handler) (Result, error)
}

// API names, as set in models.json ("api").
const (
	APICompletions = "openai-completions"
	APIResponses   = "openai-responses"
	APICodex       = "openai-codex-responses"
)

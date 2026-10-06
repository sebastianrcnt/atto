// Package ai is a Go port of pi's @earendil-works/pi-ai package: one
// streaming interface over model APIs, an api registry, and the OpenAI
// family of providers. Files map one to one to pi's sources (see
// provider/PORTING.md) so later pi changes can be carried over by reading
// the TypeScript diff next to the Go file.
//
// Portions ported from pi (https://github.com/earendil-works/pi),
// Copyright (c) 2025 Mario Zechner, MIT License. See THIRD_PARTY_NOTICES.
package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
)

// Port of src/types.ts. Names and JSON field names match pi; TypeScript
// unions become interfaces (Message, Content) or string constants.

type Api = string

// Known APIs implemented in this package.
const (
	ApiOpenAICompletions    Api = "openai-completions"
	ApiOpenAIResponses      Api = "openai-responses"
	ApiOpenAICodexResponses Api = "openai-codex-responses"
)

type ProviderID = string

type ToolChoice = string

// ThinkingLevel is pi's reasoning level ("minimal".."max"); ModelThinkingLevel
// adds "off". atto models may define other level names (see Model.Efforts).
type ThinkingLevel = string
type ModelThinkingLevel = string

const (
	ThinkingOff     ModelThinkingLevel = "off"
	ThinkingMinimal ModelThinkingLevel = "minimal"
	ThinkingLow     ModelThinkingLevel = "low"
	ThinkingMedium  ModelThinkingLevel = "medium"
	ThinkingHigh    ModelThinkingLevel = "high"
	ThinkingXHigh   ModelThinkingLevel = "xhigh"
	ThinkingMax     ModelThinkingLevel = "max"
)

// ThinkingLevelMap maps thinking levels to provider values. A nil value
// (JSON null) marks a level as unsupported; a missing key uses the
// provider default.
type ThinkingLevelMap map[string]*string

// Lookup returns the mapped value, whether the key exists, and whether it
// maps to null.
func (m ThinkingLevelMap) Lookup(level string) (value string, present bool, isNull bool) {
	v, ok := m[level]
	if !ok {
		return "", false, false
	}
	if v == nil {
		return "", true, true
	}
	return *v, true, false
}

type SamplingParams = map[string]any

type SamplingParamsByThinkingLevel = map[string]SamplingParams

// ThinkingBudgets are token budgets per thinking level (token-based providers).
type ThinkingBudgets struct {
	Minimal int `json:"minimal,omitempty"`
	Low     int `json:"low,omitempty"`
	Medium  int `json:"medium,omitempty"`
	High    int `json:"high,omitempty"`
}

type CacheRetention = string

const (
	CacheRetentionNone  CacheRetention = "none"
	CacheRetentionShort CacheRetention = "short"
	CacheRetentionLong  CacheRetention = "long"
)

type Transport = string

type ProviderEnv = map[string]string

// ProviderHeaders are extra request headers. pi allows null to suppress a
// default header; Go callers delete the key instead.
type ProviderHeaders = map[string]string

// ProviderResponse is passed to OnResponse.
type ProviderResponse struct {
	Status  int
	Headers http.Header
}

// StreamOptions are the options every API accepts (pi: StreamOptions plus
// ProviderRequestOptions).
type StreamOptions struct {
	// Signal: cancelling Context aborts the request (pi: signal).
	Context context.Context
	APIKey  string
	// HTTPClient replaces http.DefaultClient (pi: fetch).
	HTTPClient *http.Client
	Env        ProviderEnv
	// OnPayload may inspect or replace the request params before sending;
	// return nil to keep them.
	OnPayload func(payload map[string]any, model *Model) map[string]any
	// OnRequestBody receives the serialized request body (atto: shown by
	// /context and kept as the agent's last request).
	OnRequestBody   func(body []byte)
	OnResponse      func(resp ProviderResponse, model *Model)
	Headers         ProviderHeaders
	TimeoutMs       int
	MaxRetries      int
	MaxRetryDelayMs int
	Temperature     *float64
	SamplingParams  SamplingParams
	MaxTokens       int
	Transport       Transport
	CacheRetention  CacheRetention
	SessionID       string
	Metadata        map[string]any
}

func (o *StreamOptions) ctx() context.Context {
	if o == nil || o.Context == nil {
		return context.Background()
	}
	return o.Context
}

// SimpleStreamOptions add provider-neutral reasoning to StreamOptions
// (streamSimple / completeSimple).
type SimpleStreamOptions struct {
	StreamOptions
	ToolChoice      ToolChoice
	Reasoning       ThinkingLevel
	ThinkingBudgets *ThinkingBudgets
}

// StreamFunction is the uniform stream contract of an API implementation.
// options is the API's own options type (e.g. *OpenAICompletionsOptions)
// or *StreamOptions.
type StreamFunction func(model *Model, context TranscriptContext, options any) *AssistantMessageEventStream

// SimpleStreamFunction is streamSimple.
type SimpleStreamFunction func(model *Model, context TranscriptContext, options *SimpleStreamOptions) *AssistantMessageEventStream

// ProviderStreams is what an API module exports: stream and streamSimple.
type ProviderStreams struct {
	Stream       StreamFunction
	StreamSimple SimpleStreamFunction
}

// TextSignatureV1 is the structured textSignature of OpenAI Responses messages.
type TextSignatureV1 struct {
	V     int    `json:"v"`
	ID    string `json:"id"`
	Phase string `json:"phase,omitempty"`
}

// Content is a block of message content: *TextContent, *ThinkingContent,
// *ImageContent or *ToolCall.
type Content interface{ contentType() string }

type TextContent struct {
	Type          string `json:"type"` // "text"
	Text          string `json:"text"`
	TextSignature string `json:"textSignature,omitempty"`
}

type ThinkingContent struct {
	Type              string `json:"type"` // "thinking"
	Thinking          string `json:"thinking"`
	ThinkingSignature string `json:"thinkingSignature,omitempty"`
	Redacted          bool   `json:"redacted,omitempty"`
}

type ImageContent struct {
	Type     string `json:"type"` // "image"
	Data     string `json:"data"` // base64
	MimeType string `json:"mimeType"`
}

type ToolCall struct {
	Type             string         `json:"type"` // "toolCall"
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	Arguments        map[string]any `json:"arguments"`
	ThoughtSignature string         `json:"thoughtSignature,omitempty"`
	Namespace        string         `json:"namespace,omitempty"`
	// RawArguments is the arguments JSON exactly as the model streamed it
	// (atto extension). Replays send these bytes instead of re-serializing
	// Arguments, so a model's own spacing and key order survive and the
	// server's prompt cache keeps matching.
	RawArguments string `json:"-"`
}

func (*TextContent) contentType() string     { return "text" }
func (*ThinkingContent) contentType() string { return "thinking" }
func (*ImageContent) contentType() string    { return "image" }
func (*ToolCall) contentType() string        { return "toolCall" }

// NewText, NewThinking, NewImage and NewToolCall build content blocks with
// their type tag set.
func NewText(text string) *TextContent { return &TextContent{Type: "text", Text: text} }
func NewThinking(thinking string) *ThinkingContent {
	return &ThinkingContent{Type: "thinking", Thinking: thinking}
}
func NewImage(data, mimeType string) *ImageContent {
	return &ImageContent{Type: "image", Data: data, MimeType: mimeType}
}
func NewToolCall(id, name string, args map[string]any) *ToolCall {
	if args == nil {
		args = map[string]any{}
	}
	return &ToolCall{Type: "toolCall", ID: id, Name: name, Arguments: args}
}

type UsageCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
	Total      float64 `json:"total"`
}

type Usage struct {
	Input        int `json:"input"`
	Output       int `json:"output"`
	CacheRead    int `json:"cacheRead"`
	CacheWrite   int `json:"cacheWrite"`
	CacheWrite1h int `json:"cacheWrite1h,omitempty"`
	// Reasoning tokens are a subset of Output; nil when not reported.
	Reasoning   *int      `json:"reasoning,omitempty"`
	TotalTokens int       `json:"totalTokens"`
	Cost        UsageCost `json:"cost"`
}

type StopReason = string

const (
	StopPending  StopReason = "pending"
	StopStop     StopReason = "stop"
	StopLength   StopReason = "length"
	StopToolUse  StopReason = "toolUse"
	StopError    StopReason = "error"
	StopAborted  StopReason = "aborted"
	StopDeferred StopReason = "deferred"
)

// Message is *SystemMessage, *UserMessage, *AssistantMessage or
// *ToolResultMessage.
type Message interface{ MessageRole() string }

// SystemSection is one named prompt section (pi: sections record, kept in
// order). Text nil removes the section.
type SystemSection struct {
	Name string
	Text *string
}

type SystemMessage struct {
	Role         string          `json:"role"` // "system"
	Content      string          `json:"content"`
	Sections     []SystemSection `json:"-"`
	ToolsAdded   []Tool          `json:"toolsAdded,omitempty"`
	ToolsRemoved []ToolReference `json:"toolsRemoved,omitempty"`
	Timestamp    int64           `json:"timestamp"`
}

// UserMessage content is either a string (Text, with Parts nil) or blocks
// of *TextContent and *ImageContent (Parts).
type UserMessage struct {
	Role      string // "user"
	Text      string
	Parts     []Content
	Timestamp int64
}

// IsText reports whether the content is a plain string.
func (m *UserMessage) IsText() bool { return m.Parts == nil }

func (m *UserMessage) MarshalJSON() ([]byte, error) {
	var content any = m.Text
	if !m.IsText() {
		content = m.Parts
	}
	return json.Marshal(struct {
		Role      string `json:"role"`
		Content   any    `json:"content"`
		Timestamp int64  `json:"timestamp"`
	}{"user", content, m.Timestamp})
}

type AssistantMessage struct {
	Role                  string     `json:"role"` // "assistant"
	Content               []Content  `json:"content"`
	Api                   Api        `json:"api"`
	Provider              ProviderID `json:"provider"`
	Model                 string     `json:"model"`
	ResponseModel         string     `json:"responseModel,omitempty"`
	ResponseID            string     `json:"responseId,omitempty"`
	ProviderThinkingLevel string     `json:"providerThinkingLevel,omitempty"`
	ThinkingLevel         string     `json:"thinkingLevel,omitempty"`
	Usage                 Usage      `json:"usage"`
	StopReason            StopReason `json:"stopReason"`
	ErrorMessage          string     `json:"errorMessage,omitempty"`
	RawStopReason         string     `json:"rawStopReason,omitempty"`
	EndTurn               *bool      `json:"endTurn,omitempty"`
	Timestamp             int64      `json:"timestamp"`
}

type ToolResultMessage struct {
	Role       string    `json:"role"` // "toolResult"
	ToolCallID string    `json:"toolCallId"`
	ToolName   string    `json:"toolName"`
	Content    []Content `json:"content"`
	IsError    bool      `json:"isError"`
	Timestamp  int64     `json:"timestamp"`
}

func (*SystemMessage) MessageRole() string     { return "system" }
func (*UserMessage) MessageRole() string       { return "user" }
func (*AssistantMessage) MessageRole() string  { return "assistant" }
func (*ToolResultMessage) MessageRole() string { return "toolResult" }

// Tool declares a function the model may call. Parameters is a JSON
// schema, kept as raw bytes so its key order is what the caller wrote.
// pi's constrainedSampling (strict / grammar tools) is not ported.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ToolReference struct {
	Name string `json:"name"`
}

// Context is the request input of the public stream entry points.
// SystemPrompt and Tools are shorthand for a leading system message.
type Context struct {
	SystemPrompt string
	Messages     []Message
	Tools        []Tool
}

// TranscriptContext is a normalized Context: the prompt and tools live in
// the leading system message. Build it with NormalizeContext.
type TranscriptContext struct {
	Messages []Message
}

// Event types of AssistantMessageEventStream, in pi's order: start, then
// text/thinking/toolcall start-delta-end triples, then done or error.
const (
	EventStart         = "start"
	EventTextStart     = "text_start"
	EventTextDelta     = "text_delta"
	EventTextEnd       = "text_end"
	EventThinkingStart = "thinking_start"
	EventThinkingDelta = "thinking_delta"
	EventThinkingEnd   = "thinking_end"
	EventToolCallStart = "toolcall_start"
	EventToolCallDelta = "toolcall_delta"
	EventToolCallEnd   = "toolcall_end"
	EventDone          = "done"
	EventError         = "error"
)

// AssistantMessageEvent is one stream event. Which fields are set depends
// on Type, as in pi's union. Partial is the live message being built by
// the stream's goroutine: read it only after the stream ended.
type AssistantMessageEvent struct {
	Type         string
	ContentIndex int
	Delta        string
	// Content is the final text of a text_end / thinking_end block.
	Content  string
	ToolCall *ToolCall
	Partial  *AssistantMessage
	// Reason is the stop reason of done / error.
	Reason StopReason
	// Message is set on done, Error on error.
	Message *AssistantMessage
	Error   *AssistantMessage
}

// OpenAICompletionsCompat and OpenAIResponsesCompat share one Go struct:
// models.json's "compat" object uses one key namespace for both, and
// fields an API does not read are ignored. Unset pointers mean
// "auto-detect".
type Compat struct {
	// openai-completions
	SupportsStore                               *bool                 `json:"supportsStore,omitempty"`
	SupportsDeveloperRole                       *bool                 `json:"supportsDeveloperRole,omitempty"`
	SupportsReasoningEffort                     *bool                 `json:"supportsReasoningEffort,omitempty"`
	SupportsUsageInStreaming                    *bool                 `json:"supportsUsageInStreaming,omitempty"`
	SupportsFinishReason                        *bool                 `json:"supportsFinishReason,omitempty"`
	MaxTokensField                              string                `json:"maxTokensField,omitempty"`
	RequiresToolResultName                      *bool                 `json:"requiresToolResultName,omitempty"`
	RequiresAssistantAfterToolResult            *bool                 `json:"requiresAssistantAfterToolResult,omitempty"`
	RequiresThinkingAsText                      *bool                 `json:"requiresThinkingAsText,omitempty"`
	RequiresReasoningContentOnAssistantMessages *bool                 `json:"requiresReasoningContentOnAssistantMessages,omitempty"`
	ThinkingFormat                              string                `json:"thinkingFormat,omitempty"`
	ChatTemplateKwargs                          map[string]any        `json:"chatTemplateKwargs,omitempty"`
	ChatTemplateArgs                            map[string]any        `json:"chatTemplateArgs,omitempty"`
	OpenRouterRouting                           map[string]any        `json:"openRouterRouting,omitempty"`
	VercelGatewayRouting                        *VercelGatewayRouting `json:"vercelGatewayRouting,omitempty"`
	ZaiToolStream                               *bool                 `json:"zaiToolStream,omitempty"`
	ThinkingTokenBudgetField                    string                `json:"thinkingTokenBudgetField,omitempty"`
	SupportsThinkingTokenBudget                 *bool                 `json:"supportsThinkingTokenBudget,omitempty"`
	SupportsMidConvoToolAdditions               *bool                 `json:"supportsMidConvoToolAdditions,omitempty"`
	CacheControlFormat                          string                `json:"cacheControlFormat,omitempty"`
	SendSessionAffinityHeaders                  *bool                 `json:"sendSessionAffinityHeaders,omitempty"`
	VllmPriority                                *float64              `json:"vllmPriority,omitempty"`
	// both
	SupportsOpenAIGrammarTools     *bool  `json:"supportsOpenAIGrammarTools,omitempty"`
	SupportsMidConvoSystemMessages *bool  `json:"supportsMidConvoSystemMessages,omitempty"`
	SupportsStrictMode             *bool  `json:"supportsStrictMode,omitempty"`
	SessionAffinityFormat          string `json:"sessionAffinityFormat,omitempty"`
	SupportsLongCacheRetention     *bool  `json:"supportsLongCacheRetention,omitempty"`
	// openai-responses
	SupportsAdditionalTools         *bool `json:"supportsAdditionalTools,omitempty"`
	SupportsToolSearch              *bool `json:"supportsToolSearch,omitempty"`
	SupportsExplicitPromptCacheMode *bool `json:"supportsExplicitPromptCacheMode,omitempty"`
	SupportsMaxOutputTokens         *bool `json:"supportsMaxOutputTokens,omitempty"`
}

type VercelGatewayRouting struct {
	Only  []string `json:"only,omitempty"`
	Order []string `json:"order,omitempty"`
}

type ModelCostRates struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

type ModelCostTier struct {
	InputTokensAbove int `json:"inputTokensAbove"`
	ModelCostRates
}

type ModelCost struct {
	ModelCostRates
	Tiers []ModelCostTier `json:"tiers,omitempty"`
}

// Model is a chat model (pi: Model<Api>).
type Model struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Api      Api        `json:"api"`
	Provider ProviderID `json:"provider"`
	BaseURL  string     `json:"baseUrl"`
	// Input modalities: "text", "image".
	Input            []string          `json:"input"`
	Cost             ModelCost         `json:"cost"`
	Headers          map[string]string `json:"headers,omitempty"`
	Reasoning        bool              `json:"reasoning"`
	ThinkingLevelMap ThinkingLevelMap  `json:"thinkingLevelMap,omitempty"`
	ContextWindow    int               `json:"contextWindow"`
	// MaxTokens is the default output cap. atto: 0 sends none.
	MaxTokens                     int                           `json:"maxTokens"`
	SamplingParams                SamplingParams                `json:"samplingParams,omitempty"`
	SamplingParamsByThinkingLevel SamplingParamsByThinkingLevel `json:"samplingParamsByThinkingLevel,omitempty"`
	Compat                        *Compat                       `json:"compat,omitempty"`

	// atto extensions.

	// Efforts, when set, is the exact list of thinking levels in order
	// and replaces pi's derivation from Reasoning and ThinkingLevelMap.
	// Levels may have names pi does not know (e.g. "on").
	Efforts []string `json:"efforts,omitempty"`
	// ExtraBody is merged into openai-completions request bodies before
	// the named fields. String placeholders: "$effort" (the mapped level;
	// dropped when off and unmapped), "$thinking" (bool) and
	// "$thinkingType" ("enabled"/"disabled").
	ExtraBody map[string]any `json:"extraBody,omitempty"`
}

// SupportsImages reports whether the model accepts image input.
func (m *Model) SupportsImages() bool {
	return slices.Contains(m.Input, "image")
}

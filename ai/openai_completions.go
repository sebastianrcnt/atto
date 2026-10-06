package ai

// Ported from pi (https://github.com/earendil-works/pi), Copyright (c) 2025
// Mario Zechner, MIT License; see THIRD_PARTY_NOTICES.

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
)

// Port of src/api/openai-completions.ts: OpenAI-compatible chat
// completions (OpenAI, llama.cpp, vLLM, OpenRouter, OpenCode, ...).
//
// Not ported: grammar-constrained custom tools, Kimi-style tool additions
// in mid-conversation system messages, GitHub Copilot headers.
// atto extensions: Model.ExtraBody placeholders, llama.cpp
// timings.cache_n as cache reads, raw tool-call argument bytes, and
// keyless requests (no Authorization header without an API key).

func init() {
	registerBuiltinApi(ApiOpenAICompletions, ProviderStreams{Stream: streamOpenAICompletionsAny, StreamSimple: StreamSimpleOpenAICompletions})
}

// OpenAICompletionsOptions are the API's own options.
type OpenAICompletionsOptions struct {
	StreamOptions
	ToolChoice      ToolChoice
	ReasoningEffort ThinkingLevel
	ThinkingBudgets *ThinkingBudgets
	// thinkingLevel is the level the caller asked for before "off" became
	// an empty ReasoningEffort; atto's ExtraBody placeholders need it.
	thinkingLevel string
}

// ResolvedCompletionsCompat is Compat with every field decided.
type ResolvedCompletionsCompat struct {
	SupportsStore                               bool
	SupportsDeveloperRole                       bool
	SupportsReasoningEffort                     bool
	SupportsUsageInStreaming                    bool
	SupportsFinishReason                        bool
	MaxTokensField                              string
	RequiresToolResultName                      bool
	RequiresAssistantAfterToolResult            bool
	RequiresThinkingAsText                      bool
	RequiresReasoningContentOnAssistantMessages bool
	ThinkingFormat                              string
	OpenRouterRouting                           map[string]any
	VercelGatewayRouting                        *VercelGatewayRouting
	ChatTemplateKwargs                          map[string]any
	ChatTemplateArgs                            map[string]any
	ZaiToolStream                               bool
	SupportsThinkingTokenBudget                 bool
	ThinkingTokenBudgetField                    string
	SupportsStrictMode                          bool
	SupportsOpenAIGrammarTools                  bool
	SupportsMidConvoSystemMessages              bool
	SupportsMidConvoToolAdditions               bool
	CacheControlFormat                          string
	SendSessionAffinityHeaders                  bool
	SessionAffinityFormat                       string
	SupportsLongCacheRetention                  bool
	VllmPriority                                *float64
}

func getClientApiKey(provider, apiKey string, headers map[string]string) (string, error) {
	if apiKey != "" {
		return apiKey, nil
	}
	if hasHeader(headers, "authorization") || hasHeader(headers, "cf-aig-authorization") {
		return "", nil
	}
	// atto: keyless local servers (llama.cpp, vLLM) are common; send no
	// Authorization header instead of failing like pi does.
	return "", nil
}

func hasToolHistory(messages []Message) bool {
	for _, msg := range messages {
		switch m := msg.(type) {
		case *ToolResultMessage:
			return true
		case *AssistantMessage:
			for _, b := range m.Content {
				if _, ok := b.(*ToolCall); ok {
					return true
				}
			}
		}
	}
	return false
}

// --- reasoning_details (OpenRouter replay metadata) ---

func isOpenAIReasoningDetail(d map[string]any) bool {
	if id, ok := d["id"]; ok && id != nil {
		if _, s := id.(string); !s {
			return false
		}
	}
	if f, ok := d["format"]; ok {
		if _, s := f.(string); !s {
			return false
		}
	}
	if i, ok := d["index"]; ok {
		if _, n := i.(float64); !n {
			return false
		}
	}
	switch d["type"] {
	case "reasoning.summary":
		_, ok := d["summary"].(string)
		return ok
	case "reasoning.encrypted":
		_, ok := d["data"].(string)
		return ok
	case "reasoning.text":
		if _, ok := d["text"].(string); !ok {
			return false
		}
		if s, ok := d["signature"]; ok && s != nil {
			_, str := s.(string)
			return str
		}
		return true
	}
	return false
}

func parseOpenAIReasoningDetails(signature string) []map[string]any {
	if signature == "" {
		return nil
	}
	var parsed []map[string]any
	if json.Unmarshal([]byte(signature), &parsed) != nil || len(parsed) == 0 {
		return nil
	}
	for _, d := range parsed {
		if d == nil || !isOpenAIReasoningDetail(d) {
			return nil
		}
	}
	return parsed
}

func parseLegacyEncryptedReasoningDetail(signature string) map[string]any {
	if signature == "" {
		return nil
	}
	var d map[string]any
	if json.Unmarshal([]byte(signature), &d) != nil || d == nil || !isOpenAIReasoningDetail(d) || d["type"] != "reasoning.encrypted" {
		return nil
	}
	if id, _ := d["id"].(string); id == "" {
		return nil
	}
	if data, _ := d["data"].(string); data == "" {
		return nil
	}
	return d
}

func fillMissingCommonReasoningDetailFields(target, source map[string]any) {
	if v, ok := target["id"]; !ok || v == nil {
		if s, ok := source["id"]; ok {
			target["id"] = s
		}
	}
	if f, _ := target["format"].(string); f == "" {
		if s, ok := source["format"]; ok {
			target["format"] = s
		}
	}
	if _, ok := target["index"]; !ok {
		if s, ok := source["index"]; ok {
			target["index"] = s
		}
	}
}

func appendOpenAIReasoningDetail(details []map[string]any, d map[string]any) []map[string]any {
	if n := len(details); n > 0 {
		last := details[n-1]
		if d["type"] == "reasoning.text" && last["type"] == "reasoning.text" {
			last["text"] = last["text"].(string) + d["text"].(string)
			if s, _ := last["signature"].(string); s == "" {
				if v, ok := d["signature"]; ok {
					last["signature"] = v
				}
			}
			fillMissingCommonReasoningDetailFields(last, d)
			return details
		}
		if d["type"] == "reasoning.summary" && last["type"] == "reasoning.summary" {
			last["summary"] = last["summary"].(string) + d["summary"].(string)
			fillMissingCommonReasoningDetailFields(last, d)
			return details
		}
	}
	c := make(map[string]any, len(d))
	maps.Copy(c, d)
	return append(details, c)
}

var openAICompletionsReasoningFields = []string{"reasoning", "reasoning_content", "reasoning_text"}

func resolveCacheRetention(cacheRetention CacheRetention, env ProviderEnv) CacheRetention {
	if cacheRetention != "" {
		return cacheRetention
	}
	if GetProviderEnvValue("PI_CACHE_RETENTION", env) == "long" {
		return CacheRetentionLong
	}
	return CacheRetentionShort
}

func completionsOptionsOf(options any) *OpenAICompletionsOptions {
	switch o := options.(type) {
	case *OpenAICompletionsOptions:
		if o != nil {
			return o
		}
	case *StreamOptions:
		if o != nil {
			return &OpenAICompletionsOptions{StreamOptions: *o}
		}
	}
	return &OpenAICompletionsOptions{}
}

func streamOpenAICompletionsAny(model *Model, context TranscriptContext, options any) *AssistantMessageEventStream {
	return StreamOpenAICompletions(model, context, completionsOptionsOf(options))
}

// completionsChunk is the subset of a ChatCompletionChunk pi reads.
type completionsChunk struct {
	ID      string          `json:"id"`
	Model   string          `json:"model"`
	Choices []chunkChoice   `json:"choices"`
	Usage   *chunkUsage     `json:"usage"`
	Error   json.RawMessage `json:"error"`
	// atto: llama.cpp reports prompt cache reuse here.
	Timings *struct {
		CacheN int `json:"cache_n"`
	} `json:"timings"`
}

type chunkChoice struct {
	Delta *struct {
		Content          *string          `json:"content"`
		ReasoningContent *string          `json:"reasoning_content"`
		Reasoning        *string          `json:"reasoning"`
		ReasoningText    *string          `json:"reasoning_text"`
		ToolCalls        []toolCallDelta  `json:"tool_calls"`
		ReasoningDetails []map[string]any `json:"reasoning_details"`
	} `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
	Usage        *chunkUsage `json:"usage"`
}

type toolCallDelta struct {
	Index    *int   `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function *struct {
		Name      *string `json:"name"`
		Arguments *string `json:"arguments"`
	} `json:"function"`
}

type chunkUsage struct {
	PromptTokens         int  `json:"prompt_tokens"`
	CompletionTokens     int  `json:"completion_tokens"`
	CachedTokens         *int `json:"cached_tokens"`
	PromptCacheHitTokens *int `json:"prompt_cache_hit_tokens"`
	PromptTokensDetails  *struct {
		CachedTokens     *int `json:"cached_tokens"`
		CacheWriteTokens int  `json:"cache_write_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

type streamingToolCall struct {
	*ToolCall
	partialArgs string
	streamIndex *int
}

// StreamOpenAICompletions streams a chat completion (pi: stream).
func StreamOpenAICompletions(model *Model, context TranscriptContext, options *OpenAICompletionsOptions) *AssistantMessageEventStream {
	stream := NewAssistantMessageEventStream()
	compat := GetCompletionsCompat(model)
	normalized := ResolveTranscript(context, compat.SupportsMidConvoSystemMessages)
	if options == nil {
		options = &OpenAICompletionsOptions{}
	}

	go func() {
		output := newAssistantOutput(model, model.Api)
		// reasoning_details are replay metadata: kept in memory while
		// streaming, serialized once when the block is finalized.
		var streamedReasoningDetails []map[string]any
		applyStreamedReasoningDetails := func(b *ThinkingContent) {
			if streamedReasoningDetails != nil {
				s, _ := json.Marshal(streamedReasoningDetails)
				b.ThinkingSignature = string(s)
			}
		}
		var toolBlocks []*streamingToolCall
		finalizeToolCall := func(tb *streamingToolCall) {
			tb.Arguments = ParseStreamingJSON(tb.partialArgs)
			tb.RawArguments = tb.partialArgs
		}

		err := func() error {
			ctx, cancel := requestContext(&options.StreamOptions)
			defer cancel()
			apiKey, err := getClientApiKey(model.Provider, options.APIKey, options.Headers)
			if err != nil {
				return err
			}
			cacheRetention := resolveCacheRetention(options.CacheRetention, options.Env)
			cacheSessionID := options.SessionID
			if cacheRetention == CacheRetentionNone {
				cacheSessionID = ""
			}
			headers := completionsHeaders(model, cacheSessionID, compat)
			params := buildCompletionsParams(model, normalized, options, compat, cacheRetention)
			if options.OnPayload != nil {
				if next := options.OnPayload(params, model); next != nil {
					params = next
				}
			}
			body, err := marshalBody(params)
			if err != nil {
				return err
			}
			if options.OnRequestBody != nil {
				options.OnRequestBody(body)
			}
			url := strings.TrimRight(model.BaseURL, "/") + "/chat/completions"
			resp, err := retryProviderRequest(ctx, options.MaxRetries, options.MaxRetryDelayMs, func() (*http.Response, error) {
				return postJSON(ctx, options.HTTPClient, url, body, apiKey, headers, options.Headers)
			})
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if options.OnResponse != nil {
				options.OnResponse(ProviderResponse{Status: resp.StatusCode, Headers: resp.Header}, model)
			}
			stream.Push(AssistantMessageEvent{Type: EventStart, Partial: output})

			var textBlock *TextContent
			var thinkingBlock *ThinkingContent
			hasFinishReason := false
			byIndex := map[int]*streamingToolCall{}
			byID := map[string]*streamingToolCall{}
			indexOf := func(b Content) int {
				for i, c := range output.Content {
					if c == b {
						return i
					}
				}
				return -1
			}
			finishBlock := func(b Content) {
				ci := indexOf(b)
				if ci < 0 {
					return
				}
				switch blk := b.(type) {
				case *TextContent:
					stream.Push(AssistantMessageEvent{Type: EventTextEnd, ContentIndex: ci, Content: blk.Text, Partial: output})
				case *ThinkingContent:
					applyStreamedReasoningDetails(blk)
					stream.Push(AssistantMessageEvent{Type: EventThinkingEnd, ContentIndex: ci, Content: blk.Thinking, Partial: output})
				case *ToolCall:
					for _, tb := range toolBlocks {
						if tb.ToolCall == blk {
							finalizeToolCall(tb)
						}
					}
					stream.Push(AssistantMessageEvent{Type: EventToolCallEnd, ContentIndex: ci, ToolCall: blk, Partial: output})
				}
			}
			ensureTextBlock := func() *TextContent {
				if textBlock == nil {
					textBlock = NewText("")
					output.Content = append(output.Content, textBlock)
					stream.Push(AssistantMessageEvent{Type: EventTextStart, ContentIndex: indexOf(textBlock), Partial: output})
				}
				return textBlock
			}
			ensureThinkingBlock := func(signature string) *ThinkingContent {
				if thinkingBlock == nil {
					thinkingBlock = NewThinking("")
					thinkingBlock.ThinkingSignature = signature
					output.Content = append(output.Content, thinkingBlock)
					stream.Push(AssistantMessageEvent{Type: EventThinkingStart, ContentIndex: indexOf(thinkingBlock), Partial: output})
				}
				return thinkingBlock
			}
			ensureToolCallBlock := func(d toolCallDelta) *streamingToolCall {
				name := ""
				if d.Function != nil && d.Function.Name != nil {
					name = *d.Function.Name
				}
				var tb *streamingToolCall
				if d.Index != nil {
					tb = byIndex[*d.Index]
				}
				if tb == nil && d.ID != "" {
					tb = byID[d.ID]
				}
				if tb == nil {
					tb = &streamingToolCall{ToolCall: NewToolCall(d.ID, name, nil), streamIndex: d.Index}
					toolBlocks = append(toolBlocks, tb)
					if d.Index != nil {
						byIndex[*d.Index] = tb
					}
					if d.ID != "" {
						byID[d.ID] = tb
					}
					output.Content = append(output.Content, tb.ToolCall)
					stream.Push(AssistantMessageEvent{Type: EventToolCallStart, ContentIndex: indexOf(tb.ToolCall), Partial: output})
				}
				if d.Index != nil && tb.streamIndex == nil {
					tb.streamIndex = d.Index
					byIndex[*d.Index] = tb
				}
				if d.ID != "" {
					byID[d.ID] = tb
				}
				if tb.Name == "" && name != "" {
					tb.Name = name
				}
				return tb
			}

			err = readSSE(resp.Body, func(ev sseEvent) (bool, error) {
				if strings.HasPrefix(ev.Data, "[DONE]") {
					return false, nil
				}
				if ev.Data == "" {
					return true, nil
				}
				var chunk completionsChunk
				if json.Unmarshal([]byte(ev.Data), &chunk) != nil {
					return true, nil
				}
				if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
					return false, chunkError(chunk.Error)
				}
				if output.ResponseID == "" {
					output.ResponseID = chunk.ID
				}
				if chunk.Model != "" && chunk.Model != model.ID && output.ResponseModel == "" {
					output.ResponseModel = chunk.Model
				}
				if chunk.Usage != nil {
					output.Usage = parseChunkUsage(chunk.Usage, model)
				}
				if chunk.Timings != nil {
					applyLlamaCacheN(&output.Usage, chunk.Timings.CacheN, model)
				}
				if len(chunk.Choices) == 0 {
					return true, nil
				}
				choice := chunk.Choices[0]
				// Some providers (e.g. Moonshot) put usage in the choice.
				if chunk.Usage == nil && choice.Usage != nil {
					output.Usage = parseChunkUsage(choice.Usage, model)
				}
				if choice.FinishReason != nil && *choice.FinishReason != "" {
					output.RawStopReason = *choice.FinishReason
					stop, msg := mapCompletionsStopReason(*choice.FinishReason)
					output.StopReason = stop
					if msg != "" {
						output.ErrorMessage = msg
					}
					hasFinishReason = true
				}
				d := choice.Delta
				if d == nil {
					return true, nil
				}
				if d.Content != nil && *d.Content != "" {
					b := ensureTextBlock()
					b.Text += *d.Content
					stream.Push(AssistantMessageEvent{Type: EventTextDelta, ContentIndex: indexOf(b), Delta: *d.Content, Partial: output})
				}
				// Endpoints use different reasoning fields (llama.cpp:
				// reasoning_content). Use the first non-empty one; some send
				// two with the same text.
				var field, delta string
				for _, f := range []struct {
					name string
					v    *string
				}{{"reasoning_content", d.ReasoningContent}, {"reasoning", d.Reasoning}, {"reasoning_text", d.ReasoningText}} {
					if f.v != nil && *f.v != "" {
						field, delta = f.name, *f.v
						break
					}
				}
				if field != "" {
					signature := field
					if model.Provider == "opencode-go" && field == "reasoning" {
						signature = "reasoning_content"
					}
					b := ensureThinkingBlock(signature)
					b.Thinking += delta
					stream.Push(AssistantMessageEvent{Type: EventThinkingDelta, ContentIndex: indexOf(b), Delta: delta, Partial: output})
				}
				for _, tcd := range d.ToolCalls {
					tb := ensureToolCallBlock(tcd)
					if tb.ID == "" && tcd.ID != "" {
						tb.ID = tcd.ID
						byID[tcd.ID] = tb
					}
					var delta string
					if tcd.Function != nil && tcd.Function.Arguments != nil && *tcd.Function.Arguments != "" {
						delta = *tcd.Function.Arguments
						tb.partialArgs += delta
						tb.Arguments = ParseStreamingJSON(tb.partialArgs)
					}
					stream.Push(AssistantMessageEvent{Type: EventToolCallDelta, ContentIndex: indexOf(tb.ToolCall), Delta: delta, Partial: output})
				}
				for _, detail := range d.ReasoningDetails {
					if detail == nil || !isOpenAIReasoningDetail(detail) {
						continue
					}
					ensureThinkingBlock("")
					if streamedReasoningDetails == nil {
						streamedReasoningDetails = []map[string]any{}
					}
					streamedReasoningDetails = appendOpenAIReasoningDetail(streamedReasoningDetails, detail)
				}
				return true, nil
			})
			for _, b := range output.Content {
				finishBlock(b)
			}
			if err != nil {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if output.StopReason == StopAborted {
				return fmt.Errorf("Request was aborted")
			}
			if !hasFinishReason && !compat.SupportsFinishReason {
				output.StopReason = StopStop
				if hasToolCall(output) {
					output.StopReason = StopToolUse
				}
			}
			if output.StopReason == StopError {
				if output.ErrorMessage != "" {
					return fmt.Errorf("%s", output.ErrorMessage)
				}
				return fmt.Errorf("Provider returned an error stop reason")
			}
			if (compat.SupportsFinishReason && !hasFinishReason) || output.StopReason == StopPending {
				return fmt.Errorf("Stream ended without finish_reason")
			}
			return nil
		}()
		if err == nil {
			stream.Push(AssistantMessageEvent{Type: EventDone, Reason: output.StopReason, Message: output})
			stream.End()
			return
		}
		for _, b := range output.Content {
			if t, ok := b.(*ThinkingContent); ok {
				applyStreamedReasoningDetails(t)
			}
		}
		for _, tb := range toolBlocks {
			if tb.RawArguments == "" {
				finalizeToolCall(tb)
			}
		}
		output.StopReason = StopError
		if options.ctx().Err() != nil {
			output.StopReason = StopAborted
		}
		output.ErrorMessage = FormatProviderError(err, "")
		stream.Push(AssistantMessageEvent{Type: EventError, Reason: output.StopReason, Error: output})
		stream.End()
	}()
	return stream
}

func hasToolCall(m *AssistantMessage) bool {
	for _, b := range m.Content {
		if _, ok := b.(*ToolCall); ok {
			return true
		}
	}
	return false
}

func chunkError(raw json.RawMessage) error {
	var e struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
	}
	_ = json.Unmarshal(raw, &e)
	se := &streamError{Message: e.Message, Raw: raw}
	if e.Code != nil {
		se.Code = fmt.Sprint(e.Code)
	}
	return se
}

// StreamSimpleOpenAICompletions maps provider-neutral options (pi: streamSimple).
func StreamSimpleOpenAICompletions(model *Model, context TranscriptContext, options *SimpleStreamOptions) *AssistantMessageEventStream {
	if options == nil {
		options = &SimpleStreamOptions{}
	}
	base := BuildBaseOptions(model, context, options, options.APIKey)
	var clamped string
	if options.Reasoning != "" {
		clamped = ClampThinkingLevel(model, options.Reasoning)
	}
	effort := clamped
	if clamped == ThinkingOff {
		effort = ""
	}
	return StreamOpenAICompletions(model, context, &OpenAICompletionsOptions{
		StreamOptions: base, ToolChoice: options.ToolChoice, ReasoningEffort: effort,
		ThinkingBudgets: options.ThinkingBudgets, thinkingLevel: clamped,
	})
}

// completionsHeaders are the client's default headers (pi: createClient).
func completionsHeaders(model *Model, sessionID string, compat ResolvedCompletionsCompat) map[string]string {
	headers := map[string]string{}
	maps.Copy(headers, model.Headers)
	if sessionID != "" && compat.SendSessionAffinityHeaders {
		if compat.SessionAffinityFormat == "openrouter" {
			headers["x-session-id"] = sessionID
		} else {
			if compat.SessionAffinityFormat == "openai" {
				headers["session_id"] = sessionID
			}
			headers["x-client-request-id"] = sessionID
			headers["x-session-affinity"] = sessionID
		}
	}
	return headers
}

func buildCompletionsParams(model *Model, context TranscriptContext, options *OpenAICompletionsOptions, compat ResolvedCompletionsCompat, cacheRetention CacheRetention) map[string]any {
	transcriptTools := ResolveTranscriptTools(context.Messages, compat.SupportsMidConvoSystemMessages && compat.SupportsMidConvoToolAdditions)
	messages := ConvertCompletionsMessages(model, context, compat)
	cacheControl := getCompatCacheControl(compat, cacheRetention)

	params := map[string]any{}
	// atto: ExtraBody first, so the named fields below win.
	maps.Copy(params, resolveExtraBody(model, options.thinkingLevel))
	params["model"] = model.ID
	params["messages"] = messages
	params["stream"] = true
	if (strings.Contains(model.BaseURL, "api.openai.com") && cacheRetention != CacheRetentionNone) ||
		(cacheRetention == CacheRetentionLong && compat.SupportsLongCacheRetention) {
		if options.SessionID != "" {
			params["prompt_cache_key"] = ClampOpenAIPromptCacheKey(options.SessionID)
		}
	}
	if cacheRetention == CacheRetentionLong && compat.SupportsLongCacheRetention {
		params["prompt_cache_retention"] = "24h"
	}
	if compat.SupportsUsageInStreaming {
		params["stream_options"] = obj("include_usage", true)
	}
	if compat.SupportsStore {
		params["store"] = false
	}
	if options.MaxTokens > 0 {
		params[compat.MaxTokensField] = options.MaxTokens
	}
	if options.Temperature != nil {
		params["temperature"] = *options.Temperature
	}
	var tools []object
	if len(transcriptTools.RequestTools) > 0 {
		tools = convertCompletionsTools(transcriptTools.RequestTools, compat)
		params["tools"] = tools
		if compat.ZaiToolStream {
			params["tool_stream"] = true
		}
	} else if hasToolHistory(context.Messages) {
		// Anthropic behind LiteLLM-style proxies requires tools when the
		// conversation has tool calls.
		params["tools"] = []object{}
	}
	if cacheControl != nil {
		applyAnthropicCacheControl(messages, tools, cacheControl)
	}
	if options.ToolChoice != "" {
		params["tool_choice"] = options.ToolChoice
	}
	if compat.VllmPriority != nil {
		params["priority"] = *compat.VllmPriority
	}

	budgetField := compat.ThinkingTokenBudgetField
	if budgetField == "" && compat.SupportsThinkingTokenBudget {
		budgetField = "thinking_token_budget"
	}
	thinkingBudget := resolveClampedThinkingBudget(model, options, params, compat)

	effort := options.ReasoningEffort
	tlm := model.ThinkingLevelMap
	mappedOr := func(level string) string {
		if v, present, isNull := tlm.Lookup(level); present && !isNull {
			return v
		}
		return level
	}
	offValue, offPresent, offNull := tlm.Lookup(ThinkingOff)
	switch {
	case compat.ThinkingFormat == "zai" && model.Reasoning:
		if effort != "" {
			params["thinking"] = obj("type", "enabled", "clear_thinking", false)
		} else {
			params["thinking"] = obj("type", "disabled")
		}
		if effort != "" && compat.SupportsReasoningEffort {
			if v, present, isNull := tlm.Lookup(effort); !present {
				params["reasoning_effort"] = effort
			} else if !isNull {
				params["reasoning_effort"] = v
			}
		}
	case compat.ThinkingFormat == "qwen" && model.Reasoning:
		params["enable_thinking"] = effort != ""
		if effort != "" && compat.SupportsReasoningEffort {
			params["reasoning_effort"] = mappedOr(effort)
		}
	case compat.ThinkingFormat == "qwen-chat-template" && model.Reasoning:
		params["chat_template_kwargs"] = obj("enable_thinking", effort != "", "preserve_thinking", true)
	case compat.ThinkingFormat == "chat-template" && model.Reasoning:
		if v := buildChatTemplateValues(model, options, compat.ChatTemplateKwargs, thinkingBudget); v != nil {
			params["chat_template_kwargs"] = v
		}
	case compat.ThinkingFormat == "baseten" && model.Reasoning:
		if v := buildChatTemplateValues(model, options, compat.ChatTemplateArgs, thinkingBudget); v != nil {
			params["chat_template_args"] = v
		}
		if compat.SupportsReasoningEffort {
			level := effort
			if level == "" {
				level = ThinkingOff
			}
			if v, present, isNull := tlm.Lookup(level); present && !isNull {
				params["reasoning_effort"] = v
			} else if !present && effort != "" {
				params["reasoning_effort"] = effort
			}
		}
	case compat.ThinkingFormat == "deepseek" && model.Reasoning:
		if effort != "" {
			params["thinking"] = obj("type", "enabled")
		} else if !offNull {
			params["thinking"] = obj("type", "disabled")
		}
		if effort != "" && compat.SupportsReasoningEffort {
			params["reasoning_effort"] = mappedOr(effort)
		}
	case compat.ThinkingFormat == "openrouter" && model.Reasoning:
		if effort != "" {
			params["reasoning"] = obj("effort", mappedOr(effort))
		} else if !offNull {
			v := "none"
			if offPresent {
				v = offValue
			}
			params["reasoning"] = obj("effort", v)
		}
	case compat.ThinkingFormat == "ant-ling" && model.Reasoning && effort != "":
		if v, present, isNull := tlm.Lookup(effort); present && !isNull {
			params["reasoning"] = obj("effort", v)
		}
	case compat.ThinkingFormat == "together" && model.Reasoning:
		params["reasoning"] = obj("enabled", effort != "")
		if effort != "" && compat.SupportsReasoningEffort {
			params["reasoning_effort"] = mappedOr(effort)
		}
	case compat.ThinkingFormat == "string-thinking" && model.Reasoning:
		if effort != "" {
			params["thinking"] = mappedOr(effort)
		} else if !offNull {
			v := "none"
			if offPresent {
				v = offValue
			}
			params["thinking"] = v
		}
	case effort != "" && model.Reasoning && compat.SupportsReasoningEffort:
		params["reasoning_effort"] = mappedOr(effort)
	case effort == "" && model.Reasoning && compat.SupportsReasoningEffort:
		if offPresent && !offNull {
			params["reasoning_effort"] = offValue
		}
	}

	// Cap reasoning with a top-level budget field: reasoning and the answer
	// share max_tokens on these servers.
	if budgetField != "" && thinkingBudget > 0 {
		params[budgetField] = thinkingBudget
	}
	if model.Compat != nil && model.Compat.OpenRouterRouting != nil {
		params["provider"] = model.Compat.OpenRouterRouting
	}
	if model.Compat != nil && model.Compat.VercelGatewayRouting != nil {
		r := model.Compat.VercelGatewayRouting
		if r.Only != nil || r.Order != nil {
			gw := map[string]any{}
			if r.Only != nil {
				gw["only"] = r.Only
			}
			if r.Order != nil {
				gw["order"] = r.Order
			}
			params["providerOptions"] = map[string]any{"gateway": gw}
		}
	}
	// Last, so model and request sampling parameters override named fields.
	level := effort
	if level == "" {
		level = ThinkingOff
	}
	maps.Copy(params, ResolveSamplingParams(model, level, options.SamplingParams))
	return params
}

func resolveClampedThinkingBudget(model *Model, options *OpenAICompletionsOptions, params map[string]any, compat ResolvedCompletionsCompat) int {
	if options.ReasoningEffort == "" || !model.Reasoning {
		return 0
	}
	ceiling := model.MaxTokens
	if n, ok := params[compat.MaxTokensField].(int); ok {
		ceiling = n
	}
	b := ClampThinkingBudgetToAnswerRoom(ThinkingBudgetForLevel(options.ReasoningEffort, options.ThinkingBudgets), ceiling)
	return max(b, 0)
}

func buildChatTemplateValues(model *Model, options *OpenAICompletionsOptions, values map[string]any, thinkingBudget int) map[string]any {
	out := map[string]any{}
	for k, v := range values {
		if r, ok := resolveChatTemplateKwargValue(model, options, v, thinkingBudget); ok {
			out[k] = r
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// resolveChatTemplateKwargValue resolves {"$var": "thinking.enabled" |
// "thinking.effort" | "thinking.budget", "omitWhenOff": bool}.
func resolveChatTemplateKwargValue(model *Model, options *OpenAICompletionsOptions, value any, thinkingBudget int) (any, bool) {
	m, ok := value.(map[string]any)
	if !ok {
		return value, true
	}
	effort := options.ReasoningEffort
	if omit, _ := m["omitWhenOff"].(bool); omit && effort == "" {
		return nil, false
	}
	switch m["$var"] {
	case "thinking.enabled":
		return effort != "", true
	case "thinking.budget":
		if thinkingBudget <= 0 {
			return nil, false
		}
		return thinkingBudget, true
	}
	level := effort
	if level == "" {
		level = ThinkingOff
	}
	v, present, isNull := model.ThinkingLevelMap.Lookup(level)
	switch {
	case !present:
		if effort == "" {
			return nil, false
		}
		return effort, true
	case isNull:
		return nil, false
	}
	return v, true
}

// resolveExtraBody substitutes atto's ExtraBody placeholders for level.
func resolveExtraBody(model *Model, level string) map[string]any {
	if len(model.ExtraBody) == 0 {
		return nil
	}
	out := map[string]any{}
	for k, v := range model.ExtraBody {
		if r, keep := substituteExtraBody(model, v, level); keep {
			out[k] = r
		}
	}
	return out
}

func substituteExtraBody(model *Model, v any, level string) (any, bool) {
	switch x := v.(type) {
	case nil:
		return nil, false
	case string:
		switch x {
		case "$effort":
			if mapped, present, isNull := model.ThinkingLevelMap.Lookup(level); present && !isNull {
				return mapped, true
			}
			if level == ThinkingOff || level == "" {
				return nil, false
			}
			return level, true
		case "$thinking":
			return level != ThinkingOff, true
		case "$thinkingType":
			if level == ThinkingOff {
				return "disabled", true
			}
			return "enabled", true
		}
		return x, true
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, vv := range x {
			if r, ok := substituteExtraBody(model, vv, level); ok {
				m[k] = r
			}
		}
		return m, true
	case []any:
		var a []any
		for _, vv := range x {
			if r, ok := substituteExtraBody(model, vv, level); ok {
				a = append(a, r)
			}
		}
		return a, true
	}
	return v, true
}

type cacheControlValue struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

func getCompatCacheControl(compat ResolvedCompletionsCompat, cacheRetention CacheRetention) *cacheControlValue {
	if compat.CacheControlFormat != "anthropic" || cacheRetention == CacheRetentionNone {
		return nil
	}
	c := &cacheControlValue{Type: "ephemeral"}
	if cacheRetention == CacheRetentionLong && compat.SupportsLongCacheRetention {
		c.TTL = "1h"
	}
	return c
}

func applyAnthropicCacheControl(messages []object, tools []object, cc *cacheControlValue) {
	for _, m := range messages {
		if r, _ := m.get("role"); r == "system" || r == "developer" {
			addCacheControlToTextContent(m, cc)
			break
		}
	}
	if len(tools) > 0 {
		tools[len(tools)-1].set("cache_control", cc)
	}
	for _, message := range slices.Backward(messages) {
		if r, _ := message.get("role"); r == "user" || r == "assistant" || r == "tool" {
			if addCacheControlToTextContent(message, cc) {
				return
			}
		}
	}
}

func addCacheControlToTextContent(m object, cc *cacheControlValue) bool {
	content, _ := m.get("content")
	switch c := content.(type) {
	case string:
		if c == "" {
			return false
		}
		m.set("content", []object{obj("type", "text", "text", c, "cache_control", cc)})
		return true
	case []object:
		for i := len(c) - 1; i >= 0; i-- {
			if t, _ := c[i].get("type"); t == "text" {
				c[i].set("cache_control", cc)
				return true
			}
		}
	}
	return false
}

var nonIDChars = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// ConvertCompletionsMessages converts the transcript to chat messages.
func ConvertCompletionsMessages(model *Model, context TranscriptContext, compat ResolvedCompletionsCompat) []object {
	normalized := ResolveTranscript(context, compat.SupportsMidConvoSystemMessages)
	params := []object{}

	normalizeToolCallID := func(id string, _ *Model, _ *AssistantMessage) string {
		// Responses API ids look like "{call_id}|{item_id}", up to 450+
		// characters; chat completions allows 40.
		if before, after, ok := strings.Cut(id, "|"); ok {
			callID := nonIDChars.ReplaceAllString(before, "_")
			itemID := nonIDChars.ReplaceAllString(after, "_")
			combined := callID
			if itemID != "" {
				combined = callID + "_" + itemID
			}
			if len(combined) <= 40 {
				return combined
			}
			hash := ShortHash(id)
			if len(hash) > 8 {
				hash = hash[:8]
			}
			prefix := callID[:min(len(callID), max(1, 40-len(hash)-1))]
			return prefix + "_" + hash
		}
		if model.Provider == "openai" && len(id) > 40 {
			return id[:40]
		}
		return id
	}
	transformed := TransformMessages(normalized.Messages, model, normalizeToolCallID)
	instructionRole := "system"
	if model.Reasoning && compat.SupportsDeveloperRole {
		instructionRole = "developer"
	}

	lastRole := ""
	for i := 0; i < len(transformed); i++ {
		msg := transformed[i]
		if compat.RequiresAssistantAfterToolResult && lastRole == "toolResult" && msg.MessageRole() == "user" {
			params = append(params, obj("role", "assistant", "content", "I have processed the tool results."))
		}
		switch m := msg.(type) {
		case *SystemMessage:
			text := RenderSystemMessageUpdate(m)
			if i == 0 {
				text = GetSystemMessageText(m)
			}
			if text != "" {
				params = append(params, obj("role", instructionRole, "content", text))
			}
		case *UserMessage:
			if m.IsText() {
				params = append(params, obj("role", "user", "content", m.Text))
				break
			}
			content := []object{}
			for _, item := range m.Parts {
				switch b := item.(type) {
				case *TextContent:
					if b.Text != "" {
						content = append(content, obj("type", "text", "text", b.Text))
					}
				case *ImageContent:
					content = append(content, obj("type", "image_url", "image_url", obj("url", "data:"+b.MimeType+";base64,"+b.Data)))
				}
			}
			if len(content) == 0 {
				continue
			}
			params = append(params, obj("role", "user", "content", content))
		case *AssistantMessage:
			var content any // nil marshals as null
			if compat.RequiresAssistantAfterToolResult {
				content = ""
			}
			assistant := obj("role", "assistant", "content", content)

			var textParts []object
			var assistantText strings.Builder
			var thinkingBlocks []*ThinkingContent
			var toolCalls []*ToolCall
			for _, b := range m.Content {
				switch blk := b.(type) {
				case *TextContent:
					if strings.TrimSpace(blk.Text) != "" {
						textParts = append(textParts, obj("type", "text", "text", blk.Text))
						assistantText.WriteString(blk.Text)
					}
				case *ThinkingContent:
					thinkingBlocks = append(thinkingBlocks, blk)
				case *ToolCall:
					toolCalls = append(toolCalls, blk)
				}
			}
			var preserved []map[string]any
			for _, t := range thinkingBlocks {
				if d := parseOpenAIReasoningDetails(t.ThinkingSignature); d != nil {
					preserved = d
					break
				}
			}
			if preserved == nil {
				for _, tc := range toolCalls {
					if d := parseLegacyEncryptedReasoningDetail(tc.ThoughtSignature); d != nil {
						preserved = append(preserved, d)
					}
				}
			}
			var nonEmpty []*ThinkingContent
			for _, t := range thinkingBlocks {
				if strings.TrimSpace(t.Thinking) != "" {
					nonEmpty = append(nonEmpty, t)
				}
			}
			if len(nonEmpty) > 0 {
				if compat.RequiresThinkingAsText {
					var ts []string
					for _, t := range nonEmpty {
						ts = append(ts, t.Thinking)
					}
					assistant.set("content", append([]object{obj("type", "text", "text", strings.Join(ts, "\n\n"))}, textParts...))
				} else {
					// Assistant content is always a plain string: some models
					// mirror a block array literally in their output.
					if assistantText.Len() > 0 {
						assistant.set("content", assistantText.String())
					}
					if preserved == nil {
						signature := nonEmpty[0].ThinkingSignature
						if model.Provider == "opencode-go" && signature == "reasoning" {
							signature = "reasoning_content"
						}
						if slices.Contains(openAICompletionsReasoningFields, signature) {
							var ts []string
							for _, t := range nonEmpty {
								ts = append(ts, t.Thinking)
							}
							assistant.set(signature, strings.Join(ts, "\n"))
						}
					}
				}
			} else if assistantText.Len() > 0 {
				assistant.set("content", assistantText.String())
			}
			if len(toolCalls) > 0 {
				calls := make([]object, 0, len(toolCalls))
				for _, tc := range toolCalls {
					calls = append(calls, obj("id", tc.ID, "type", "function", "function", obj("name", tc.Name, "arguments", toolCallArguments(tc))))
				}
				assistant.set("tool_calls", calls)
			}
			if preserved != nil {
				assistant.set("reasoning_details", preserved)
			}
			if compat.RequiresReasoningContentOnAssistantMessages && model.Reasoning {
				if _, ok := assistant.get("reasoning_content"); !ok {
					assistant.set("reasoning_content", "")
				}
			}
			// Skip assistant messages with neither content nor tool calls:
			// providers reject them (e.g. aborted turns without output).
			c, _ := assistant.get("content")
			hasContent := false
			switch cv := c.(type) {
			case string:
				hasContent = cv != ""
			case []object:
				hasContent = len(cv) > 0
			}
			if _, hasCalls := assistant.get("tool_calls"); !hasContent && !hasCalls {
				continue
			}
			params = append(params, assistant)
		case *ToolResultMessage:
			var imageBlocks []object
			j := i
			for ; j < len(transformed); j++ {
				tm, ok := transformed[j].(*ToolResultMessage)
				if !ok {
					break
				}
				text := ContentText(tm.Content, "\n")
				hasImages := false
				for _, c := range tm.Content {
					if _, ok := c.(*ImageContent); ok {
						hasImages = true
					}
				}
				switch {
				case text != "":
				case hasImages:
					text = "(see attached image)"
				default:
					text = "(no tool output)"
				}
				result := obj("role", "tool", "content", text, "tool_call_id", tm.ToolCallID)
				if compat.RequiresToolResultName && tm.ToolName != "" {
					result.set("name", tm.ToolName)
				}
				params = append(params, result)
				if hasImages && model.SupportsImages() {
					for _, c := range tm.Content {
						if im, ok := c.(*ImageContent); ok {
							imageBlocks = append(imageBlocks, obj("type", "image_url", "image_url", obj("url", "data:"+im.MimeType+";base64,"+im.Data)))
						}
					}
				}
			}
			i = j - 1
			if len(imageBlocks) > 0 {
				if compat.RequiresAssistantAfterToolResult {
					params = append(params, obj("role", "assistant", "content", "I have processed the tool results."))
				}
				parts := append([]object{obj("type", "text", "text", "Attached image(s) from tool result:")}, imageBlocks...)
				params = append(params, obj("role", "user", "content", parts))
				lastRole = "user"
			} else {
				lastRole = "toolResult"
			}
			continue
		}
		lastRole = msg.MessageRole()
	}
	return params
}

// toolCallArguments are the arguments to replay: the model's own bytes
// when known (atto), else pi's JSON.stringify of the parsed object. Bytes
// that aren't JSON (a model's broken call, kept as streamed) are replayed
// as the parsed object instead: servers that parse the history reject
// them, and every later request of the session would fail.
func toolCallArguments(tc *ToolCall) string {
	if tc.RawArguments != "" && json.Valid([]byte(tc.RawArguments)) {
		return tc.RawArguments
	}
	b, _ := json.Marshal(tc.Arguments)
	if tc.Arguments == nil {
		return "{}"
	}
	return string(b)
}

func convertCompletionsTools(tools []Tool, compat ResolvedCompletionsCompat) []object {
	out := make([]object, 0, len(tools))
	for _, t := range tools {
		params := t.Parameters
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		fn := obj("name", t.Name, "description", t.Description, "parameters", params)
		if compat.SupportsStrictMode {
			fn.set("strict", false)
		}
		out = append(out, obj("type", "function", "function", fn))
	}
	return out
}

func parseChunkUsage(raw *chunkUsage, model *Model) Usage {
	cacheRead := 0
	switch {
	case raw.PromptTokensDetails != nil && raw.PromptTokensDetails.CachedTokens != nil:
		cacheRead = *raw.PromptTokensDetails.CachedTokens
	case raw.PromptCacheHitTokens != nil:
		cacheRead = *raw.PromptCacheHitTokens
	case raw.CachedTokens != nil:
		cacheRead = *raw.CachedTokens
	}
	cacheWrite := 0
	if raw.PromptTokensDetails != nil {
		cacheWrite = raw.PromptTokensDetails.CacheWriteTokens
	}
	// cached_tokens counts cache reads; writes are reported separately and
	// not subtracted from it.
	input := max(0, raw.PromptTokens-cacheRead-cacheWrite)
	reasoning := 0
	if raw.CompletionTokensDetails != nil {
		reasoning = raw.CompletionTokensDetails.ReasoningTokens
	}
	u := Usage{
		Input: input, Output: raw.CompletionTokens, CacheRead: cacheRead, CacheWrite: cacheWrite,
		Reasoning: &reasoning, TotalTokens: input + raw.CompletionTokens + cacheRead + cacheWrite,
	}
	CalculateCost(model, &u)
	return u
}

// applyLlamaCacheN counts llama.cpp's timings.cache_n (prompt tokens
// reused from its cache) as cache reads when the usage block reports
// fewer (atto extension).
func applyLlamaCacheN(u *Usage, cacheN int, model *Model) {
	if cacheN <= u.CacheRead {
		return
	}
	prompt := u.Input + u.CacheRead + u.CacheWrite
	u.CacheRead = cacheN
	u.Input = max(0, prompt-u.CacheRead-u.CacheWrite)
	u.TotalTokens = u.Input + u.Output + u.CacheRead + u.CacheWrite
	CalculateCost(model, u)
}

func mapCompletionsStopReason(reason string) (StopReason, string) {
	switch reason {
	case "stop", "end":
		return StopStop, ""
	case "length":
		return StopLength, ""
	case "function_call", "tool_calls":
		return StopToolUse, ""
	case "content_filter":
		return StopError, "Provider finish_reason: content_filter"
	case "network_error":
		return StopError, "Provider finish_reason: network_error"
	}
	return StopError, "Provider finish_reason: " + reason
}

// detectCompletionsCompat auto-detects compatibility from provider and URL.
func detectCompletionsCompat(model *Model) ResolvedCompletionsCompat {
	provider, baseURL := model.Provider, model.BaseURL
	has := func(s string) bool { return strings.Contains(baseURL, s) }
	isZai := provider == "zai" || provider == "zai-coding-cn" || has("api.z.ai") || has("open.bigmodel.cn")
	isTogether := provider == "together" || has("api.together.ai") || has("api.together.xyz")
	isMoonshot := provider == "moonshotai" || provider == "moonshotai-cn" || has("api.moonshot.")
	isOpenRouter := provider == "openrouter" || has("openrouter.ai")
	isCloudflareWorkersAI := provider == "cloudflare-workers-ai" || has("api.cloudflare.com")
	isCloudflareAiGateway := provider == "cloudflare-ai-gateway" || has("gateway.ai.cloudflare.com")
	isNvidia := provider == "nvidia" || has("integrate.api.nvidia.com")
	isAntLing := provider == "ant-ling" || has("api.ant-ling.com")
	isCerebras := provider == "cerebras" || has("cerebras.ai")
	isDeepSeek := provider == "deepseek" || strings.Contains(strings.ToLower(baseURL), "deepseek.com")
	isGrok := provider == "xai" || has("api.x.ai")
	isNonStandard := isNvidia || isCerebras || isGrok || isTogether || has("chutes.ai") || isDeepSeek || isZai ||
		isMoonshot || provider == "opencode" || has("opencode.ai") || isCloudflareWorkersAI || isCloudflareAiGateway || isAntLing
	useMaxTokens := has("chutes.ai") || isDeepSeek || isMoonshot || isCloudflareAiGateway || isTogether || isNvidia || isAntLing || isZai
	isOpenRouterDeveloperRoleModel := isOpenRouter && (strings.HasPrefix(model.ID, "anthropic/") || strings.HasPrefix(model.ID, "openai/"))
	cacheControlFormat := ""
	if provider == "openrouter" && strings.HasPrefix(model.ID, "anthropic/") {
		cacheControlFormat = "anthropic"
	}
	thinkingFormat := "openai"
	switch {
	case isDeepSeek:
		thinkingFormat = "deepseek"
	case isZai:
		thinkingFormat = "zai"
	case isTogether:
		thinkingFormat = "together"
	case isAntLing:
		thinkingFormat = "ant-ling"
	case isOpenRouter:
		thinkingFormat = "openrouter"
	}
	maxTokensField := "max_completion_tokens"
	if useMaxTokens {
		maxTokensField = "max_tokens"
	}
	affinity := "openai"
	if isOpenRouter {
		affinity = "openrouter"
	}
	return ResolvedCompletionsCompat{
		SupportsStore:                               !isNonStandard,
		SupportsDeveloperRole:                       isOpenRouterDeveloperRoleModel || (!isNonStandard && !isOpenRouter),
		SupportsReasoningEffort:                     !isGrok && !isZai && !isMoonshot && !isTogether && !isCloudflareAiGateway && !isNvidia && !isAntLing,
		SupportsUsageInStreaming:                    true,
		SupportsFinishReason:                        true,
		MaxTokensField:                              maxTokensField,
		RequiresReasoningContentOnAssistantMessages: isDeepSeek,
		ThinkingFormat:                              thinkingFormat,
		ChatTemplateKwargs:                          map[string]any{},
		ChatTemplateArgs:                            map[string]any{},
		CacheControlFormat:                          cacheControlFormat,
		SendSessionAffinityHeaders:                  isOpenRouter,
		SessionAffinityFormat:                       affinity,
		SupportsLongCacheRetention:                  !(isTogether || isCloudflareWorkersAI || isCloudflareAiGateway || isNvidia || isAntLing),
	}
}

func pick(v *bool, d bool) bool {
	if v != nil {
		return *v
	}
	return d
}

func pickString(v, d string) string {
	if v != "" {
		return v
	}
	return d
}

// GetCompletionsCompat auto-detects compat, then applies model.Compat.
func GetCompletionsCompat(model *Model) ResolvedCompletionsCompat {
	d := detectCompletionsCompat(model)
	c := model.Compat
	if c == nil {
		return d
	}
	r := d
	r.SupportsStore = pick(c.SupportsStore, d.SupportsStore)
	r.SupportsDeveloperRole = pick(c.SupportsDeveloperRole, d.SupportsDeveloperRole)
	r.SupportsReasoningEffort = pick(c.SupportsReasoningEffort, d.SupportsReasoningEffort)
	r.SupportsUsageInStreaming = pick(c.SupportsUsageInStreaming, d.SupportsUsageInStreaming)
	r.SupportsFinishReason = pick(c.SupportsFinishReason, d.SupportsFinishReason)
	r.MaxTokensField = pickString(c.MaxTokensField, d.MaxTokensField)
	r.RequiresToolResultName = pick(c.RequiresToolResultName, d.RequiresToolResultName)
	r.RequiresAssistantAfterToolResult = pick(c.RequiresAssistantAfterToolResult, d.RequiresAssistantAfterToolResult)
	r.RequiresThinkingAsText = pick(c.RequiresThinkingAsText, d.RequiresThinkingAsText)
	r.RequiresReasoningContentOnAssistantMessages = pick(c.RequiresReasoningContentOnAssistantMessages, d.RequiresReasoningContentOnAssistantMessages)
	r.ThinkingFormat = pickString(c.ThinkingFormat, d.ThinkingFormat)
	r.OpenRouterRouting = c.OpenRouterRouting
	if c.VercelGatewayRouting != nil {
		r.VercelGatewayRouting = c.VercelGatewayRouting
	}
	if c.ChatTemplateKwargs != nil {
		r.ChatTemplateKwargs = c.ChatTemplateKwargs
	}
	if c.ChatTemplateArgs != nil {
		r.ChatTemplateArgs = c.ChatTemplateArgs
	}
	r.ZaiToolStream = pick(c.ZaiToolStream, d.ZaiToolStream)
	r.SupportsThinkingTokenBudget = pick(c.SupportsThinkingTokenBudget, d.SupportsThinkingTokenBudget)
	r.ThinkingTokenBudgetField = pickString(c.ThinkingTokenBudgetField, d.ThinkingTokenBudgetField)
	r.SupportsStrictMode = pick(c.SupportsStrictMode, d.SupportsStrictMode)
	r.SupportsOpenAIGrammarTools = pick(c.SupportsOpenAIGrammarTools, d.SupportsOpenAIGrammarTools)
	r.SupportsMidConvoSystemMessages = pick(c.SupportsMidConvoSystemMessages, d.SupportsMidConvoSystemMessages)
	r.SupportsMidConvoToolAdditions = pick(c.SupportsMidConvoToolAdditions, d.SupportsMidConvoToolAdditions)
	r.CacheControlFormat = pickString(c.CacheControlFormat, d.CacheControlFormat)
	r.SendSessionAffinityHeaders = pick(c.SendSessionAffinityHeaders, d.SendSessionAffinityHeaders)
	r.SessionAffinityFormat = pickString(c.SessionAffinityFormat, d.SessionAffinityFormat)
	r.SupportsLongCacheRetention = pick(c.SupportsLongCacheRetention, d.SupportsLongCacheRetention)
	r.VllmPriority = c.VllmPriority
	return r
}

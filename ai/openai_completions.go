package ai

// Ported from pi (https://github.com/earendil-works/pi), Copyright (c) 2025
// Mario Zechner, MIT License; see THIRD_PARTY_NOTICES.

import (
	"encoding/json"
	"fmt"
	"net/http"
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
		output.ErrorCause = err
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

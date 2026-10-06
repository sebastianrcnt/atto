package ai

// Ported from pi (https://github.com/earendil-works/pi), Copyright (c) 2025
// Mario Zechner, MIT License; see THIRD_PARTY_NOTICES.

import (
	"fmt"
	"maps"
	"net/http"
	"strings"
)

// Port of src/api/openai-responses.ts: the OpenAI Responses API with
// store: false and encrypted reasoning replay.

func init() {
	registerBuiltinApi(ApiOpenAIResponses, ProviderStreams{Stream: streamOpenAIResponsesAny, StreamSimple: StreamSimpleOpenAIResponses})
}

var openAIToolCallProviders = map[string]bool{"openai": true, "openai-codex": true, "opencode": true}

// OpenAI Responses rejects max_output_tokens below 16.
const openAIResponsesMinOutputTokens = 16

const chatGPTUsageURL = "https://chatgpt.com/settings/usage"

// OpenAIResponsesOptions are the API's own options.
type OpenAIResponsesOptions struct {
	StreamOptions
	ReasoningEffort  ThinkingLevel
	ReasoningSummary string // "auto", "detailed", "concise"
	ServiceTier      string
	ToolChoice       ToolChoice
}

// isChatGPTSignIn: OpenAI API keys start with "sk-"; any other credential
// sent directly to OpenAI is a Sign in with ChatGPT access token.
func isChatGPTSignIn(model *Model, apiKey string) bool {
	return model.Provider == "openai" && model.BaseURL == "https://api.openai.com/v1" && apiKey != "" && !strings.HasPrefix(apiKey, "sk-")
}

func detectSessionAffinityFormat(model *Model) string {
	if model.Provider == "openrouter" || strings.Contains(model.BaseURL, "openrouter.ai") {
		return "openrouter"
	}
	return "openai"
}

// ResolvedResponsesCompat is OpenAIResponsesCompat with defaults applied.
type ResolvedResponsesCompat struct {
	SupportsDeveloperRole           bool
	SupportsMidConvoSystemMessages  bool
	SessionAffinityFormat           string
	SupportsLongCacheRetention      bool
	SupportsStrictMode              bool
	SupportsExplicitPromptCacheMode bool
	SupportsMaxOutputTokens         bool
}

func GetResponsesCompat(model *Model) ResolvedResponsesCompat {
	c := model.Compat
	if c == nil {
		c = &Compat{}
	}
	return ResolvedResponsesCompat{
		SupportsDeveloperRole:           pick(c.SupportsDeveloperRole, true),
		SupportsMidConvoSystemMessages:  pick(c.SupportsMidConvoSystemMessages, false),
		SessionAffinityFormat:           pickString(c.SessionAffinityFormat, detectSessionAffinityFormat(model)),
		SupportsLongCacheRetention:      pick(c.SupportsLongCacheRetention, true),
		SupportsStrictMode:              pick(c.SupportsStrictMode, false),
		SupportsExplicitPromptCacheMode: pick(c.SupportsExplicitPromptCacheMode, false),
		SupportsMaxOutputTokens:         pick(c.SupportsMaxOutputTokens, true),
	}
}

func responsesOptionsOf(options any) *OpenAIResponsesOptions {
	switch o := options.(type) {
	case *OpenAIResponsesOptions:
		if o != nil {
			return o
		}
	case *StreamOptions:
		if o != nil {
			return &OpenAIResponsesOptions{StreamOptions: *o}
		}
	}
	return &OpenAIResponsesOptions{}
}

func streamOpenAIResponsesAny(model *Model, context TranscriptContext, options any) *AssistantMessageEventStream {
	return StreamOpenAIResponses(model, context, responsesOptionsOf(options))
}

// StreamOpenAIResponses streams a Responses API request (pi: stream).
func StreamOpenAIResponses(model *Model, context TranscriptContext, options *OpenAIResponsesOptions) *AssistantMessageEventStream {
	stream := NewAssistantMessageEventStream()
	compat := GetResponsesCompat(model)
	normalized := ResolveTranscript(context, compat.SupportsMidConvoSystemMessages)
	if options == nil {
		options = &OpenAIResponsesOptions{}
	}
	go func() {
		output := newAssistantOutput(model, model.Api)
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
			headers := map[string]string{}
			maps.Copy(headers, model.Headers)
			if cacheSessionID != "" {
				switch compat.SessionAffinityFormat {
				case "openrouter":
					headers["x-session-id"] = cacheSessionID
				case "openai":
					headers["session_id"] = cacheSessionID
					headers["x-client-request-id"] = cacheSessionID
				default:
					headers["x-client-request-id"] = cacheSessionID
				}
			}
			params := buildResponsesParams(model, normalized, options, compat)
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
			url := strings.TrimRight(model.BaseURL, "/") + "/responses"
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
			src := newSSESource(resp.Body)
			defer src.close()
			err = ProcessResponsesStream(sseResponsesEvents(src), output, stream, model, ResponsesStreamOptions{
				ServiceTier:             options.ServiceTier,
				ApplyServiceTierPricing: func(u *Usage, tier string) { applyServiceTierPricing(u, tier, model.ID, true) },
			})
			if err != nil {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			switch output.StopReason {
			case StopPending:
				return fmt.Errorf("OpenAI Responses stream ended without a stop reason")
			case StopAborted, StopError:
				if output.ErrorMessage != "" {
					return fmt.Errorf("%s", output.ErrorMessage)
				}
				return fmt.Errorf("An unknown error occurred")
			}
			return nil
		}()
		if err == nil {
			stream.Push(AssistantMessageEvent{Type: EventDone, Reason: output.StopReason, Message: output})
			stream.End()
			return
		}
		output.StopReason = StopError
		if options.ctx().Err() != nil {
			output.StopReason = StopAborted
		}
		prefix := model.Provider + " API error"
		if model.Provider == "openai" {
			prefix = "OpenAI API error"
		}
		msg := FormatProviderError(err, prefix)
		// Sign in with ChatGPT shares the subscription's usage limit with
		// other apps.
		if strings.Contains(msg, "subscription_sharing_usage_limit_exceeded") {
			msg += "\nCheck your ChatGPT usage: " + chatGPTUsageURL
		}
		output.ErrorMessage = msg
		stream.Push(AssistantMessageEvent{Type: EventError, Reason: output.StopReason, Error: output})
		stream.End()
	}()
	return stream
}

// StreamSimpleOpenAIResponses maps provider-neutral options (pi: streamSimple).
func StreamSimpleOpenAIResponses(model *Model, context TranscriptContext, options *SimpleStreamOptions) *AssistantMessageEventStream {
	if options == nil {
		options = &SimpleStreamOptions{}
	}
	base := BuildBaseOptions(model, context, options, options.APIKey)
	effort := ""
	if options.Reasoning != "" {
		effort = ClampThinkingLevel(model, options.Reasoning)
	}
	if effort == ThinkingOff {
		effort = ""
	}
	return StreamOpenAIResponses(model, context, &OpenAIResponsesOptions{StreamOptions: base, ReasoningEffort: effort, ToolChoice: options.ToolChoice})
}

func buildResponsesParams(model *Model, context TranscriptContext, options *OpenAIResponsesOptions, compat ResolvedResponsesCompat) map[string]any {
	transcriptTools := ResolveTranscriptTools(context.Messages, false)
	messages := ConvertResponsesMessages(model, context, openAIToolCallProviders, ConvertResponsesMessagesOptions{
		SupportsMidConvoSystemMessages: compat.SupportsMidConvoSystemMessages,
	})
	cacheRetention := resolveCacheRetention(options.CacheRetention, options.Env)
	// Sign in with ChatGPT rejects these request fields.
	omitUnsupported := isChatGPTSignIn(model, options.APIKey)
	params := map[string]any{
		"model":  model.ID,
		"input":  messages,
		"stream": true,
		"store":  false,
	}
	if cacheRetention != CacheRetentionNone && options.SessionID != "" {
		params["prompt_cache_key"] = ClampOpenAIPromptCacheKey(options.SessionID)
	}
	if !omitUnsupported {
		if cacheRetention == CacheRetentionLong && compat.SupportsLongCacheRetention && !compat.SupportsExplicitPromptCacheMode {
			params["prompt_cache_retention"] = "24h"
		}
		if compat.SupportsExplicitPromptCacheMode {
			switch {
			case cacheRetention == CacheRetentionNone:
				params["prompt_cache_options"] = obj("mode", "explicit")
			case cacheRetention == CacheRetentionLong && compat.SupportsLongCacheRetention:
				params["prompt_cache_options"] = obj("ttl", "30m")
			}
		}
	}
	if options.MaxTokens > 0 && compat.SupportsMaxOutputTokens && !omitUnsupported {
		params["max_output_tokens"] = max(options.MaxTokens, openAIResponsesMinOutputTokens)
	}
	if options.Temperature != nil && !omitUnsupported {
		params["temperature"] = *options.Temperature
	}
	if options.ServiceTier != "" {
		params["service_tier"] = options.ServiceTier
	}
	if len(transcriptTools.RequestTools) > 0 {
		f := false
		params["tools"] = ConvertResponsesTools(transcriptTools.RequestTools, compat.SupportsStrictMode, &f)
	}
	if options.ToolChoice != "" {
		params["tool_choice"] = options.ToolChoice
	}
	reasoningEffort := options.ReasoningEffort
	if reasoningEffort == "" && options.ReasoningSummary != "" {
		reasoningEffort = ThinkingMedium
	}
	if model.Reasoning {
		if reasoningEffort != "" {
			effort := reasoningEffort
			if options.ReasoningEffort != "" {
				if v, present, isNull := model.ThinkingLevelMap.Lookup(options.ReasoningEffort); present && !isNull {
					effort = v
				}
			}
			summary := options.ReasoningSummary
			if summary == "" {
				summary = "auto"
			}
			params["reasoning"] = obj("effort", effort, "summary", summary)
			params["include"] = []string{"reasoning.encrypted_content"}
		} else if v, present, isNull := model.ThinkingLevelMap.Lookup(ThinkingOff); model.Provider != "github-copilot" && !isNull {
			if !present {
				v = "none"
			}
			params["reasoning"] = obj("effort", v)
		}
		if model.Provider == "xai" {
			params["include"] = []string{"reasoning.encrypted_content"}
		}
	}
	level := reasoningEffort
	if level == "" {
		level = ThinkingOff
	}
	maps.Copy(params, ResolveSamplingParams(model, level, options.SamplingParams))
	return params
}

// applyServiceTierPricing scales cost for flex and priority tiers.
func applyServiceTierPricing(u *Usage, tier, modelID string, fastIsPriority bool) {
	m := 1.0
	switch tier {
	case "flex":
		m = 0.5
	case "priority", "fast":
		if tier == "fast" && !fastIsPriority {
			return
		}
		m = 2
		if modelID == "gpt-5.5" {
			m = 2.5
		}
	}
	if m == 1 {
		return
	}
	u.Cost.Input *= m
	u.Cost.Output *= m
	u.Cost.CacheRead *= m
	u.Cost.CacheWrite *= m
	u.Cost.Total = u.Cost.Input + u.Cost.Output + u.Cost.CacheRead + u.Cost.CacheWrite
}

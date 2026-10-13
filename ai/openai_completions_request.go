package ai

// Ported from pi (https://github.com/earendil-works/pi), Copyright (c) 2025
// Mario Zechner, MIT License; see THIRD_PARTY_NOTICES.

import (
	"maps"
	"slices"
	"strings"
)

func resolveCacheRetention(cacheRetention CacheRetention, env ProviderEnv) CacheRetention {
	if cacheRetention != "" {
		return cacheRetention
	}
	if GetProviderEnvValue("PI_CACHE_RETENTION", env) == "long" {
		return CacheRetentionLong
	}
	return CacheRetentionShort
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

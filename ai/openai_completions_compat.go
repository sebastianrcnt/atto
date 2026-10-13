package ai

// Ported from pi (https://github.com/earendil-works/pi), Copyright (c) 2025
// Mario Zechner, MIT License; see THIRD_PARTY_NOTICES.

import (
	"strings"
)

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

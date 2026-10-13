package ai

// Ported from pi (https://github.com/earendil-works/pi), Copyright (c) 2025
// Mario Zechner, MIT License; see THIRD_PARTY_NOTICES.

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

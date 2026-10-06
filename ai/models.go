package ai

// Ported from pi (https://github.com/earendil-works/pi), Copyright (c) 2025
// Mario Zechner, MIT License; see THIRD_PARTY_NOTICES.

import "slices"

// Port of the model helpers in src/models.ts. pi's Models/Provider
// runtime (createModels, credential stores, dynamic catalogs) is not
// ported; atto's config package plays that role.

// CalculateCost fills usage.Cost from the model's rates.
func CalculateCost(model *Model, usage *Usage) UsageCost {
	inputTokens := usage.Input + usage.CacheRead + usage.CacheWrite
	rates := model.Cost.ModelCostRates
	matched := -1
	for _, tier := range model.Cost.Tiers {
		if inputTokens > tier.InputTokensAbove && tier.InputTokensAbove > matched {
			rates, matched = tier.ModelCostRates, tier.InputTokensAbove
		}
	}
	// Anthropic charges 2x base input for 1h cache writes.
	longWrite := float64(usage.CacheWrite1h)
	shortWrite := float64(usage.CacheWrite) - longWrite
	usage.Cost.Input = rates.Input / 1e6 * float64(usage.Input)
	usage.Cost.Output = rates.Output / 1e6 * float64(usage.Output)
	usage.Cost.CacheRead = rates.CacheRead / 1e6 * float64(usage.CacheRead)
	usage.Cost.CacheWrite = (rates.CacheWrite*shortWrite + rates.Input*2*longWrite) / 1e6
	usage.Cost.Total = usage.Cost.Input + usage.Cost.Output + usage.Cost.CacheRead + usage.Cost.CacheWrite
	return usage.Cost
}

var extendedThinkingLevels = []string{ThinkingOff, ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh, ThinkingMax}

// ContextPriceBoundary is the first positive context-size price tier.
func (c *ModelCost) ContextPriceBoundary() int {
	if c == nil {
		return 0
	}
	n := 0
	for _, t := range c.Tiers {
		if t.InputTokensAbove > 0 && (n == 0 || t.InputTokensAbove < n) {
			n = t.InputTokensAbove
		}
	}
	return n
}

// InputMultiplier reports the active tier's input price relative to base.
func (c *ModelCost) InputMultiplier(tokens int) float64 {
	if c == nil || c.Input <= 0 {
		return 1
	}
	rate, matched := c.Input, -1
	for _, t := range c.Tiers {
		if tokens > t.InputTokensAbove && t.InputTokensAbove > matched {
			rate, matched = t.Input, t.InputTokensAbove
		}
	}
	return rate / c.Input
}

// GetSupportedThinkingLevels lists the levels a model accepts. A model
// with Efforts set (atto) uses exactly that list.
func GetSupportedThinkingLevels(model *Model) []string {
	if len(model.Efforts) > 0 {
		return slices.Clone(model.Efforts)
	}
	if !model.Reasoning {
		return []string{ThinkingOff}
	}
	var out []string
	for _, level := range extendedThinkingLevels {
		_, present, isNull := model.ThinkingLevelMap.Lookup(level)
		if isNull {
			continue
		}
		if (level == ThinkingXHigh || level == ThinkingMax) && !present {
			continue
		}
		out = append(out, level)
	}
	return out
}

// ClampThinkingLevel returns level if supported, else the nearest
// supported level above it, else below it.
func ClampThinkingLevel(model *Model, level string) string {
	available := GetSupportedThinkingLevels(model)
	if slices.Contains(available, level) {
		return level
	}
	first := ThinkingOff
	if len(available) > 0 {
		first = available[0]
	}
	requested := slices.Index(extendedThinkingLevels, level)
	if requested < 0 {
		return first
	}
	for i := requested; i < len(extendedThinkingLevels); i++ {
		if slices.Contains(available, extendedThinkingLevels[i]) {
			return extendedThinkingLevels[i]
		}
	}
	for i := requested - 1; i >= 0; i-- {
		if slices.Contains(available, extendedThinkingLevels[i]) {
			return extendedThinkingLevels[i]
		}
	}
	return first
}

// ModelsAreEqual compares provider and id.
func ModelsAreEqual(a, b *Model) bool {
	if a == nil || b == nil {
		return false
	}
	return a.ID == b.ID && a.Provider == b.Provider
}

// newAssistantOutput is the empty message every API starts streaming into.
func newAssistantOutput(model *Model, api Api) *AssistantMessage {
	return &AssistantMessage{
		Role: "assistant", Content: []Content{}, Api: api, Provider: model.Provider, Model: model.ID,
		StopReason: StopPending, Timestamp: nowMillis(),
	}
}

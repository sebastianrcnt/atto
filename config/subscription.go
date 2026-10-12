package config

import (
	"slices"

	"github.com/sebastianrcnt/atto/ai"
)

// subscriptionContext is the default context window, in tokens, that OpenAI
// gives a model when usage is drawn from a ChatGPT subscription (Codex,
// ChatGPT Work) and that has no long-context fee there, where it differs
// from the API's first price tier. Keys are OpenAI model IDs.
//
// Sources, as of 2026-10-11/12:
//   - OpenAI Developer Community announcement, 2026-10-11: "Default context
//     length for GPT 6.1-Sol is now 500k tokens" (up from 272k) in Codex
//     and ChatGPT Work.
//   - Tibo (Codex lead) on X, 2026-10-11: no long-context fees for
//     GPT-6.1 Sol when usage is drawn from the subscription.
//   - The model list the Codex CLI fetched from OpenAI on 2026-10-12:
//     gpt-6.1-sol context_window 500000 (max_context_window 872000);
//     gpt-6-luna and gpt-6-sol still 272000, so they keep their tier cap.
//
// The API prices (and their 272k tier) are unchanged; only subscription
// usage is affected.
var subscriptionContext = map[string]int{
	"gpt-6.1-sol": 500000,
}

// subscriptionContextProviders serve OpenAI's models on a ChatGPT
// subscription: the openai provider with Sign in with ChatGPT, and the
// legacy openai-codex backend.
var subscriptionContextProviders = []string{"openai", "openai-codex"}

// SubscriptionContext is the context r's model has on its subscription
// plan, without a long-context fee (0: none known, or not on a
// subscription). Compaction caps at it instead of the price tier.
func (r ModelRef) SubscriptionContext() int {
	if !r.Provider.Subscription || !slices.Contains(subscriptionContextProviders, r.ProviderName) {
		return 0
	}
	return subscriptionContext[r.Model.ID]
}

// waiveTiers drops the price tiers below the subscription context: the
// usage estimate then never applies a long-context surcharge the plan does
// not charge.
func (r *ModelRef) waiveTiers() {
	n := r.SubscriptionContext()
	c := r.Model.Cost
	if n <= 0 || c == nil || len(c.Tiers) == 0 {
		return
	}
	waived := *c
	waived.Tiers = slices.DeleteFunc(slices.Clone(c.Tiers), func(t ai.ModelCostTier) bool { return t.InputTokensAbove < n })
	r.Model.Cost = &waived
}

package ai

import "testing"

func TestContextPrice(t *testing.T) {
	var none *ModelCost
	if none.ContextPriceBoundary() != 0 || none.InputMultiplier(999999) != 1 {
		t.Fatal("unpriced model")
	}
	c := &ModelCost{Input: 0.1, Tiers: []ModelCostTier{{InputTokensAbove: 500000, Input: 0.3}, {InputTokensAbove: 272000, Input: 0.2}, {InputTokensAbove: 0, Input: 0.1}}}
	if got := c.ContextPriceBoundary(); got != 272000 {
		t.Fatal(got)
	}
	for _, tc := range []struct {
		tokens int
		want   float64
	}{{0, 1}, {272000, 1}, {272001, 2}, {500000, 2}, {500001, 3}} {
		if got := c.InputMultiplier(tc.tokens); got < tc.want-1e-9 || got > tc.want+1e-9 {
			t.Fatal(tc, got)
		}
	}
}

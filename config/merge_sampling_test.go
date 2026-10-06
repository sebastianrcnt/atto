package config

import "testing"

func TestMergeModelSamplingByThinkingLevel(t *testing.T) {
	base := Model{SamplingParamsByThinkingLevel: map[string]map[string]any{
		"low":  {"temperature": 0.5, "top_p": 0.9},
		"high": {"temperature": 1.0},
	}}
	over := Model{SamplingParamsByThinkingLevel: map[string]map[string]any{
		"low":    {"temperature": 0.7},
		"medium": {"top_p": 0.8},
	}}
	got := mergeModel(base, over).SamplingParamsByThinkingLevel
	if got["low"]["temperature"] != 0.7 || got["low"]["top_p"] != 0.9 || got["high"]["temperature"] != 1.0 || got["medium"]["top_p"] != 0.8 {
		t.Fatalf("merged sampling params: %v", got)
	}
	if base.SamplingParamsByThinkingLevel["low"]["temperature"] != 0.5 {
		t.Fatal("merge mutated base")
	}
}

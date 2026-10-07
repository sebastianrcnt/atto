package main

import (
	"encoding/json"
	"fmt"
	"os"

	"atto2/agent"
)

func writeMetrics(path string, a *agent.Agent, answer string, runErr error) error {
	k := a.Machine.Kernel
	stats := map[string]any{"report": answer, "steps": a.Steps, "prompt_tokens": a.Cortex.Tokens, "completion_tokens": a.CompletionTokens, "syscalls": len(k.Log), "pure_runs": k.PureRuns, "impure_runs": k.ImpureRuns}
	if runErr != nil {
		stats["error"] = runErr.Error()
	}
	if path != "" {
		raw, _ := json.MarshalIndent(stats, "", "  ")
		if writeErr := os.WriteFile(path, raw, 0o600); writeErr != nil {
			fmt.Fprintln(os.Stderr, "atto2:", writeErr)
			if runErr == nil {
				return writeErr
			}
		}
	}
	return runErr
}

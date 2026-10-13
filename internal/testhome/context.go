package testhome

import (
	"fmt"
	"os"
)

// ClearExecutionContext removes the calling atto session's execution context
// from an ordinary test run. Tests that need a session, an agent guard or a tool
// call ID must set it explicitly with t.Setenv. Call this only after dispatching
// helper-process modes: shell hosts, supervisors and workers must keep the
// environment supplied by the test that started them.
func ClearExecutionContext() error {
	// Keep this test utility independent of config so config's own tests can
	// use it too. ATTO_SUBAGENT is the legacy spelling of ATTO_AGENT.
	for _, key := range []string{"ATTO_SESSION_ID", "ATTO_AGENT", "ATTO_SUBAGENT", "ATTO_TOOL_CALL_ID"} {
		if err := os.Unsetenv(key); err != nil {
			return fmt.Errorf("clearing test execution context %s: %w", key, err)
		}
	}
	return nil
}

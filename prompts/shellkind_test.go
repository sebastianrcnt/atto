package prompts

import (
	"strings"
	"testing"
)

// A machine without bash runs commands with sh; the model is told to write
// POSIX sh. Bash says nothing about POSIX and keeps its text.
func TestShellKindSh(t *testing.T) {
	const posix = "write POSIX sh, not bash-only syntax such as [[ ]], arrays, `source` or process substitution"
	sys := Render("system", System{Kind: "sh", Tool: "bash", Date: "d"})
	tool := Render("bash_tool", map[string]any{"Kind": "sh"})
	for name, got := range map[string]string{"system": sys, "bash_tool": tool} {
		if !strings.Contains(got, posix) {
			t.Errorf("%s lacks the POSIX line: %s", name, got)
		}
		if strings.Contains(got, "Run a bash command") || strings.Contains(got, "PowerShell") {
			t.Errorf("%s names another shell: %s", name, got)
		}
	}
	if !strings.HasPrefix(tool, "Run a POSIX sh command in the working directory") {
		t.Errorf("tool prompt: %s", tool)
	}
	if !strings.Contains(sys, "You have one tool, bash, but this machine has no bash: it runs commands with POSIX sh") {
		t.Errorf("system prompt: %s", sys)
	}
	for _, kind := range []string{"bash", ""} {
		for name, got := range map[string]string{
			"system":    Render("system", System{Kind: kind, Tool: "bash", Date: "d"}),
			"bash_tool": Render("bash_tool", map[string]any{"Kind": kind}),
		} {
			if strings.Contains(got, "POSIX") {
				t.Errorf("kind %q %s mentions POSIX: %s", kind, name, got)
			}
		}
	}
	const bashTool = "Run a bash command in the working directory and return its combined stdout/stderr. Each call runs in a fresh shell (use absolute paths or `cd dir && ...`). Stdin is not connected;"
	if got := Render("bash_tool", map[string]any{"Kind": "bash"}); !strings.HasPrefix(got, bashTool) {
		t.Errorf("bash tool prompt changed: %s", got)
	}
	const bashSystem = "You have one tool, bash. Use it for everything: exploring (ls, rg, cat, sed -n), editing files (heredocs, sed, python scripts, patch), building, and testing.\nEvery bash call needs"
	if got := Render("system", System{Kind: "bash", Tool: "bash", Date: "d"}); !strings.Contains(got, bashSystem) {
		t.Errorf("bash system prompt changed: %s", got)
	}
}

package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/shell"
)

func commandDescription(t *testing.T, schema json.RawMessage) string {
	t.Helper()
	var s struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &s); err != nil {
		t.Fatal(err)
	}
	return s.Properties["command"].Description
}

func TestShellKindReachesTheModel(t *testing.T) {
	start := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		sh      shell.Shell
		command string
		posix   bool
	}{
		{shell.Shell{Kind: shell.Bash, Path: "/bin/bash"}, "The bash command to run.", false},
		{shell.Shell{Kind: shell.Sh, Path: "/bin/sh"}, "The POSIX sh command to run.", true},
		{shell.Shell{Kind: shell.PowerShell, Path: `C:\pwsh.exe`}, "The PowerShell command to run.", false},
		{shell.Shell{Kind: shell.Cmd, Path: "cmd.exe"}, "The cmd.exe command to run.", false},
	} {
		t.Run(string(c.sh.Kind), func(t *testing.T) {
			a := &Agent{Shell: c.sh}
			tools := a.tools()
			if len(tools) != 1 {
				t.Fatalf("tools: %v", tools)
			}
			if got := commandDescription(t, tools[0].Function.Parameters); got != c.command {
				t.Errorf("command description %q, want %q", got, c.command)
			}
			prompt := buildPrompt("/p", c.sh, start, nil, nil, nil, "")
			for name, text := range map[string]string{"tool description": tools[0].Function.Description, "system prompt": prompt} {
				if got := strings.Contains(text, "POSIX sh, not bash-only syntax such as [[ ]], arrays, `source` or process substitution"); got != c.posix {
					t.Errorf("%s POSIX line = %v: %s", name, got, text)
				}
			}
			if !strings.Contains(prompt, "- Shell: "+c.sh.Path) {
				t.Errorf("environment line lacks the shell path: %s", prompt)
			}
		})
	}
	// Bash keeps the schema byte for byte, so request bodies and prompt
	// caches do not change.
	if got := toolSchema(shell.Shell{Kind: shell.Bash, Path: "/bin/bash"}); string(got) != string(bashSchema) {
		t.Error("the bash schema changed")
	}
}

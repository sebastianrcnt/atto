package prompts

import (
	"strings"
	"testing"
)

// samples is the data each template is rendered with; a new template
// must be listed here.
var samples = map[string]any{
	"system":                 System{Kind: "powershell", WinPS51: true, Tool: "powershell", Sub: "SUB", MCP: "a, b", Cwd: "/w", OS: "linux", Arch: "amd64", Shell: "/bin/sh", Date: "2026-01-02"},
	"bash_tool":              map[string]any{"Kind": "bash"},
	"subagent":               Subagent{Name: "w1", Preset: "docs", Instructions: "Be brief."},
	"subagent_parent":        map[string]any{"Presets": "Subagent presets:\n- docs"},
	"compact":                map[string]any{"Words": 700},
	"compact_prefix":         nil,
	"branch_summary":         map[string]any{"Start": "X", "Words": 600, "Focus": "tests"},
	"branch_summary_prefix":  nil,
	"goal_continuation":      Goal{Objective: "obj", Turns: 2, Used: 10, Budget: 100, Remaining: 90},
	"goal_budget":            Goal{Objective: "obj", Used: 100, Budget: 100, Seconds: 9},
	"goal_budget_lines":      Goal{Used: 1},
	"goal_objective_updated": Goal{Objective: "obj", Used: 10},
}

func TestEveryTemplateRenders(t *testing.T) {
	for _, tt := range tmpl.Templates() {
		name := tt.Name()
		if name == "" {
			continue
		}
		data, ok := samples[name]
		if !ok {
			t.Errorf("%s: no sample data", name)
			continue
		}
		out := Render(name, data)
		if out == "" || strings.Contains(out, "<no value>") || strings.HasSuffix(out, "\n\n\n") {
			t.Errorf("%s: bad output %q", name, out)
		}
	}
}

func TestRender(t *testing.T) {
	for _, c := range []struct {
		name string
		data any
		want string
	}{
		{"bash_tool", map[string]any{"Kind": "cmd"}, "Run a cmd.exe command in the working directory"},
		{"compact", map[string]any{"Words": 700}, "Stay under 700 words."},
		{"goal_continuation", Goal{Objective: "a &lt; b", Used: 5}, "<objective>\na &lt; b\n</objective>"},
		{"goal_continuation", Goal{Used: 5}, "- Token budget: none\n- Tokens remaining: unbounded\n- Goal turns so far: 0"},
		{"goal_continuation", Goal{Used: 5, Budget: 8, Remaining: 3}, "- Token budget: 8\n- Tokens remaining: 3\n"},
		{"system", System{Kind: "bash", Tool: "bash", Date: "d"}, `run "atto goal resume '<why>'" (never resume on your own)`},
		{"subagent", Subagent{Name: "w", Preset: "p"}, `You are subagent "w" (preset p)`},
	} {
		if got := Render(c.name, c.data); !strings.Contains(got, c.want) {
			t.Errorf("%s: %q not in %q", c.name, c.want, got)
		}
	}
	// A template ends where its last line does.
	if got := Render("subagent", Subagent{Name: "w", Preset: "p", Instructions: "I"}); !strings.HasSuffix(got, "Instructions for this subagent:\nI") {
		t.Errorf("subagent: %q", got)
	}
	if got := Render("branch_summary", map[string]any{"Start": "X", "Words": 1, "Focus": ""}); !strings.HasSuffix(got, "Do not call tools.") {
		t.Errorf("branch_summary: %q", got)
	}
	if got := Render("system", System{Kind: "bash", Tool: "bash", Date: "d"}); !strings.HasSuffix(got, "- Session started: d") {
		t.Errorf("system: %q", got)
	}
}

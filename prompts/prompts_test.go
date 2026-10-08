package prompts

import (
	"strings"
	"testing"
)

// samples is the data each template is rendered with; a new template
// must be listed here.
var samples = map[string]any{
	"system":                   System{Kind: "powershell", WinPS51: true, Tool: "powershell", Sub: "SUB", MCP: "a, b", Cwd: "/w", OS: "linux", Arch: "amd64", Shell: "/bin/sh", Date: "2026-01-02"},
	"bash_tool":                map[string]any{"Kind": "bash"},
	"agent":                    Agent{Name: "w1", Preset: "docs", Instructions: "Be brief.", Path: "/root/w1", Parent: "/root"},
	"agent_parent":             map[string]any{"Presets": "Roles (-role; default general):\n- docs"},
	"compact":                  map[string]any{"Words": 700},
	"compact_prefix":           nil,
	"branch_summary":           map[string]any{"Start": "X", "Words": 600, "Focus": "tests"},
	"branch_summary_prefix":    nil,
	"goal_continuation":        Goal{Objective: "obj", Turns: 2},
	"goal_objective_updated":   Goal{Objective: "obj"},
	"goal_cleared":             nil,
	"goal_paused":              Goal{},
	"goal_state_waiting":       Goal{},
	"goal_state_paused":        Goal{Label: "paused", Interrupted: true},
	"goal_state_running_steer": nil,
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
		{"goal_continuation", Goal{Objective: "a &lt; b"}, "<objective>\na &lt; b\n</objective>"},
		{"goal_continuation", Goal{Turns: 3}, "verified.\n\nGoal turns so far: 3\n\nUser messages:"},
		{"system", System{Kind: "bash", Tool: "bash", Date: "d"}, `run "atto goal resume '<why>'" (never resume on your own)`},
		{"agent", Agent{Name: "w", Preset: "p", Path: "/root/w", Parent: "/root"}, `You are agent /root/w, in a team of atto agents working for the user: /root started you with role p`},
	} {
		if got := Render(c.name, c.data); !strings.Contains(got, c.want) {
			t.Errorf("%s: %q not in %q", c.name, c.want, got)
		}
	}
	// A template ends where its last line does.
	if got := Render("agent", Agent{Name: "w", Preset: "p", Instructions: "I"}); !strings.HasSuffix(got, "Instructions for your role:\nI") {
		t.Errorf("agent: %q", got)
	}
	if got := Render("branch_summary", map[string]any{"Start": "X", "Words": 1, "Focus": ""}); !strings.HasSuffix(got, "Do not call tools.") {
		t.Errorf("branch_summary: %q", got)
	}
	if got := Render("system", System{Kind: "bash", Tool: "bash", Date: "d"}); !strings.HasSuffix(got, "- Session started: d") {
		t.Errorf("system: %q", got)
	}
}

func TestAgentTemplateNames(t *testing.T) {
	for _, name := range []string{"agent", "agent_parent"} {
		if tmpl.Lookup(name) == nil {
			t.Errorf("missing agent template %q", name)
		}
	}
}

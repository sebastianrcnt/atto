package config

import (
	"encoding/json"
	"os"
	"testing"
)

func TestAgentSettingsCompatibility(t *testing.T) {
	// The old subagents key stays readable; explicit agents settings win.
	// The gate and limits older versions had are accepted and ignored.
	for _, c := range []struct {
		name, raw     string
		model, effort string
	}{
		{"old", `{"subagents":{"enabled":true,"maxConcurrent":5,"maxDepth":3,"model":"a/b"}}`, "a/b", ""},
		{"new", `{"agents":{"enabled":false,"maxConcurrent":7,"effort":"low"}}`, "", "low"},
		{"both", `{"agents":{"model":"x/y"},"subagents":{"enabled":true,"model":"a/b"}}`, "x/y", ""},
		{"default", `{}`, "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(EnvDir, t.TempDir())
			if err := os.WriteFile(SettingsPath(), []byte(c.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			check := func(when string) {
				s, err := LoadSettings()
				m, e := s.AgentDefaults()
				if err != nil || m != c.model || e != c.effort {
					t.Fatalf("%s: settings: %+v %v", when, s, err)
				}
			}
			check("read")
			if err := UpdateSettings(map[string]any{"unknown": "preserved"}); err != nil {
				t.Fatal(err)
			}
			check("rewritten")
			data, err := os.ReadFile(SettingsPath())
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			// Writes retire subagents without losing unknown keys or agent values.
			if _, ok := fields["subagents"]; ok || fields["unknown"] != "preserved" {
				t.Fatalf("migration: %s", data)
			}
		})
	}
}

func TestAgentEnvironmentCompatibility(t *testing.T) {
	// ATTO_SUBAGENT remains an alias at the old child-command guard sites.
	if EnvLegacyAgent != "ATTO_SUBAGENT" {
		t.Fatal("legacy environment spelling changed")
	}
	for _, c := range []struct {
		agent, legacy string
		want          bool
	}{
		{"", "", false}, {"1", "", true}, {"", "1", true}, {"1", "1", true},
	} {
		t.Setenv(EnvAgent, c.agent)
		t.Setenv(EnvLegacyAgent, c.legacy)
		if got := InAgentCommand(); got != c.want {
			t.Errorf("agent=%q legacy=%q: %v", c.agent, c.legacy, got)
		}
	}
}

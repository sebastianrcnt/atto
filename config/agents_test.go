package config

import (
	"encoding/json"
	"os"
	"testing"
)

func TestAgentSettingsCompatibility(t *testing.T) {
	// The old subagents key stays readable; explicit agents settings win.
	for _, c := range []struct {
		name, raw string
		enabled   bool
		limit     int
	}{
		{"old", `{"subagents":{"enabled":true,"maxConcurrent":5}}`, true, 5},
		{"new", `{"agents":{"enabled":true,"maxConcurrent":7}}`, true, 7},
		{"both", `{"agents":{"enabled":false,"maxConcurrent":2},"subagents":{"enabled":true,"maxConcurrent":5}}`, false, 2},
		{"default", `{}`, false, DefaultMaxAgents},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(EnvDir, t.TempDir())
			if err := os.WriteFile(SettingsPath(), []byte(c.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			s, err := LoadSettings()
			if err != nil || s.AgentsEnabled() != c.enabled || s.AgentLimit() != c.limit {
				t.Fatalf("settings: %+v %v", s, err)
			}
			if err := UpdateSettings(map[string]any{"unknown": "preserved"}); err != nil {
				t.Fatal(err)
			}
			s, err = LoadSettings()
			if err != nil || s.AgentsEnabled() != c.enabled || s.AgentLimit() != c.limit {
				t.Fatalf("settings changed on migration: %+v %v", s, err)
			}
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

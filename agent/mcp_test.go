package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/shell"
)

type fakeMCP []string

func (f fakeMCP) PromptServers() []string { return f }

func TestMCPLineIsDeterministicAndOnlyWhenConfigured(t *testing.T) {
	sh := shell.Default()
	start := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	none := buildPrompt("/p", sh, start, nil, nil, nil, "")
	if strings.Contains(none, "MCP") {
		t.Fatal("the prompt mentions MCP with no servers")
	}
	a := buildPrompt("/p", sh, start, nil, nil, []string{"linear", "github"}, "")
	b := buildPrompt("/p", sh, start, nil, nil, []string{"github", "linear"}, "")
	if a != b {
		t.Fatal("the prompt depends on the order the servers were listed in")
	}
	if !strings.Contains(a, "(configured: github, linear).\n\nWork autonomously") {
		t.Fatalf("line missing or misplaced:\n%s", a)
	}
	// The rest of the prompt is untouched: cutting the line out gives the plain prompt.
	before, _, _ := strings.Cut(a, "MCP servers are available")
	j := strings.Index(a, "Work autonomously")
	if before+a[j:] != none {
		t.Fatal("the MCP line changed more than itself")
	}
}

func TestAgentPromptTakesMCPServersAtRebuild(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("ATTO_DIR", t.TempDir())
	a := New(config.ModelRef{ProviderName: "t", Model: config.Model{ID: "m"}}, "", t.TempDir())
	if strings.Contains(a.SystemPrompt(), "MCP servers are") {
		t.Fatal("MCP in a fresh prompt")
	}
	servers := fakeMCP{"one"}
	a.MCP = servers
	if strings.Contains(a.SystemPrompt(), "MCP servers are") {
		t.Fatal("the prompt changed without a rebuild: that would break the prompt cache")
	}
	if !a.Reload() || !strings.Contains(a.SystemPrompt(), "(configured: one).") {
		t.Fatal("Reload did not take the servers")
	}
	if a.Reload() {
		t.Fatal("an unchanged rebuild reported a change")
	}
}

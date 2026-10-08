package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/config"
)

func TestTrustRefusesAgentShell(t *testing.T) {
	for _, env := range []string{config.EnvAgent, "ATTO_SESSION_ID"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(config.EnvAgent, "")
			t.Setenv("ATTO_SESSION_ID", "")
			t.Setenv(config.EnvDir, t.TempDir())
			t.Setenv(env, "agent")
			for _, args := range [][]string{nil, {"approve", "all"}, {"revoke", "all"}} {
				var out strings.Builder
				if err := RunTrust(args, &out); err == nil || !strings.Contains(err.Error(), "not from an agent's shell") {
					t.Fatalf("atto trust %v inside %s: %v", args, env, err)
				}
			}
			if _, err := os.Stat(config.MCPApprovalsPath()); !os.IsNotExist(err) {
				t.Fatal("refused command wrote approvals")
			}
		})
	}
}

func TestTrustListApproveAndRevoke(t *testing.T) {
	t.Setenv(config.EnvAgent, "")
	t.Setenv("ATTO_SESSION_ID", "")
	t.Setenv(config.EnvDir, t.TempDir())
	cwd := t.TempDir()
	t.Chdir(cwd)
	if err := os.Mkdir(filepath.Join(cwd, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.ProjectMCPPath(cwd), []byte(`{"mcpServers":{"one":{"command":"one"},"two":{"command":"two"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		var out strings.Builder
		if err := RunTrust(args, &out); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if out := run(); !strings.Contains(out, "mcp  one  one  pending") || !strings.Contains(out, "atto trust approve mcp two") {
		t.Fatalf("list: %s", out)
	}
	if out := run("approve", "mcp", "one"); !strings.Contains(out, "Approved mcp one") {
		t.Fatalf("approve one: %s", out)
	}
	if out := run("list", "-json"); !strings.Contains(out, `"status": "approved"`) || !strings.Contains(out, `"status": "pending"`) {
		t.Fatalf("partial approvals: %s", out)
	}
	run("approve", "all")
	if out := run(); strings.Contains(out, "pending") {
		t.Fatalf("approve all: %s", out)
	}
	run("revoke", "mcp", "one")
	if out := run(); !strings.Contains(out, "mcp  one  one  pending") || !strings.Contains(out, "mcp  two  two  approved") {
		t.Fatalf("revoke one: %s", out)
	}
	run("revoke", "all")
	if out := run(); strings.Contains(out, "approved") {
		t.Fatalf("revoke all: %s", out)
	}
	for _, args := range [][]string{{"approve"}, {"approve", "mcp", "missing"}, {"revoke", "unknown", "one"}, {"nope"}} {
		if err := RunTrust(args, &strings.Builder{}); err == nil {
			t.Fatalf("invalid arguments accepted: %v", args)
		}
	}
}

func TestTrustRevokeAllDoesNotRequireValidProjectConfiguration(t *testing.T) {
	t.Setenv(config.EnvAgent, "")
	t.Setenv("ATTO_SESSION_ID", "")
	t.Setenv(config.EnvDir, t.TempDir())
	cwd := t.TempDir()
	t.Chdir(cwd)
	if err := os.WriteFile(config.ProjectMCPPath(cwd), []byte(`{"mcpServers":{"old":{"command":"old"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunTrust([]string{"approve", "all"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.ProjectMCPPath(cwd), []byte(`not JSON`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunTrust([]string{"revoke", "all"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.ProjectMCPPath(cwd), []byte(`{"mcpServers":{"old":{"command":"old"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := RunTrust(nil, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "pending") {
		t.Fatal("restoring old content restored revoked approval")
	}
}

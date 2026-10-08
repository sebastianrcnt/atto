package trust

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/extensions"
	"github.com/sebastianrcnt/atto/mcp"
)

func project(t *testing.T) string {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv(config.EnvDir, filepath.Join(root, "user"))
	cwd := filepath.Join(root, "project")
	for path, text := range map[string]string{
		filepath.Join(cwd, ".git", "HEAD"):                           "x",
		config.ProjectMCPPath(cwd):                                   `{"mcpServers":{"repo":{"command":"project-server"}}}`,
		config.MCPPath():                                             `{"mcpServers":{"user":{"command":"user-server"}}}`,
		filepath.Join(config.ProjectExtensionsDir(cwd), "deploy.ts"): "export default () => {}",
		filepath.Join(config.ExtensionsDir(), "personal.ts"):         "export default () => {}",
	} {
		write(t, path, text)
	}
	return cwd
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func discover(t *testing.T, cwd string) []Item {
	t.Helper()
	items, err := Discover(cwd)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestDiscoveryAndContentDecisions(t *testing.T) {
	cwd := project(t)
	items := discover(t, cwd)
	if len(items) != 2 || len(Unapproved(items)) != 2 {
		t.Fatalf("only project code belongs in trust: %+v", items)
	}
	for _, in := range items {
		if err := Approve(in); err != nil {
			t.Fatal(err)
		}
	}
	if pending := Unapproved(discover(t, cwd)); len(pending) != 0 {
		t.Fatalf("approval was not remembered: %+v", pending)
	}
	// The existing per-kind commands and files see unified approvals.
	servers, _ := mcp.Load(cwd)
	for _, s := range servers {
		if mcp.ApprovalOf(s) != mcp.Approved {
			t.Fatalf("existing MCP approval does not see trust approval: %+v", s)
		}
	}
	for _, in := range extensions.Inspect(cwd) {
		if in.Source == extensions.Project && in.Status != extensions.Ready {
			t.Fatalf("existing extension approval does not see trust approval: %+v", in)
		}
	}
	write(t, config.ProjectMCPPath(cwd), `{"mcpServers":{"repo":{"command":"changed"}}}`)
	pending := Unapproved(discover(t, cwd))
	if len(pending) != 1 || pending[0].Kind != MCP {
		t.Fatalf("only changed content should be pending: %+v", pending)
	}
	if err := Deny(pending[0]); err != nil {
		t.Fatal(err)
	}
	if len(Unapproved(discover(t, cwd))) != 0 {
		t.Fatal("denied content should not ask again")
	}
	for _, in := range discover(t, cwd) {
		if err := Revoke(in); err != nil {
			t.Fatal(err)
		}
	}
	if len(Unapproved(discover(t, cwd))) != 2 {
		t.Fatal("revocation should forget both approval and denial")
	}
}

func TestApprovalUsesDisplayedHash(t *testing.T) {
	cwd := project(t)
	items := discover(t, cwd)
	write(t, config.ProjectMCPPath(cwd), `{"mcpServers":{"repo":{"command":"changed"}}}`)
	write(t, filepath.Join(config.ProjectExtensionsDir(cwd), "deploy.ts"), "export default (atto: any) => atto.log('changed')")
	for _, in := range items {
		if err := Approve(in); err != nil {
			t.Fatal(err)
		}
	}
	if len(Unapproved(discover(t, cwd))) != 2 {
		t.Fatal("allow approved code changed after discovery, without showing it")
	}
}

func TestNonInteractiveWarningNamesEveryItemAndApprovalCommand(t *testing.T) {
	cwd := project(t)
	items := discover(t, cwd)
	var out strings.Builder
	Warn(&out, items)
	for _, in := range items {
		for _, want := range []string{in.Kind + " " + in.Name, "will not run", in.Command()} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("warning lacks %q:\n%s", want, out.String())
			}
		}
	}
	for _, in := range items {
		if err := Approve(in); err != nil {
			t.Fatal(err)
		}
	}
	out.Reset()
	Warn(&out, discover(t, cwd))
	if out.Len() != 0 {
		t.Fatalf("approved content should not warn: %s", out.String())
	}
}

func TestWarningQuotesUnusualItemNames(t *testing.T) {
	in := Item{Kind: MCP, Name: "server's name"}
	if got := in.Command(); got != `atto trust approve mcp 'server'\''s name'` {
		t.Fatalf("command is not safe to copy into the shell: %s", got)
	}
}

func TestHookAdapterAndHashChanges(t *testing.T) {
	cwd := project(t)
	write(t, config.SettingsPath(), `{"hooks":{"Stop":[{"hooks":[{"command":"user"}]}]}}`)
	write(t, config.ProjectSettingsPath(cwd), `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"check"}]}]}}`)
	items := discover(t, cwd)
	if len(items) != 3 || items[0].Kind != Hook || !strings.Contains(items[0].Target, "PreToolUse [Bash]") {
		t.Fatalf("project hooks should join other executable content, never user hooks: %+v", items)
	}
	in := items[0]
	if err := Approve(in); err != nil {
		t.Fatal(err)
	}
	if discover(t, cwd)[0].Status != Approved {
		t.Fatal("hook approval was not stored")
	}
	write(t, config.ProjectSettingsPath(cwd), `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"changed"}]}]}}`)
	if discover(t, cwd)[0].Status != Pending {
		t.Fatal("changed hook retained approval")
	}
	// Approving the snapshot again must not approve unseen changed content.
	if err := Approve(in); err != nil {
		t.Fatal(err)
	}
	changed := discover(t, cwd)[0]
	if changed.Status != Pending {
		t.Fatal("hook approval used changed content instead of the displayed hash")
	}
	if err := Deny(changed); err != nil {
		t.Fatal(err)
	}
	if discover(t, cwd)[0].Status != Denied {
		t.Fatal("hook denial was not stored")
	}
	if err := Revoke(changed); err != nil {
		t.Fatal(err)
	}
	if discover(t, cwd)[0].Status != Pending {
		t.Fatal("hook denial survived revocation")
	}
}

func TestRevokeAllForgetsRemovedAndOldContent(t *testing.T) {
	cwd := project(t)
	hookPath := config.ProjectSettingsPath(cwd)
	hookText := `{"hooks":{"Stop":[{"hooks":[{"command":"old hook"}]}]}}`
	write(t, hookPath, hookText)
	items := discover(t, cwd)
	for _, in := range items {
		if err := Approve(in); err != nil {
			t.Fatal(err)
		}
	}
	// Remove every item before revocation. Nothing in today's discovery list
	// can tell us that these old content hashes had been approved.
	write(t, hookPath, `{}`)
	write(t, config.ProjectMCPPath(cwd), `{"mcpServers":{}}`)
	extPath := filepath.Join(config.ProjectExtensionsDir(cwd), "deploy.ts")
	if err := os.Remove(extPath); err != nil {
		t.Fatal(err)
	}
	if len(discover(t, cwd)) != 0 {
		t.Fatal("items were not removed")
	}
	if err := RevokeAll(cwd); err != nil {
		t.Fatal(err)
	}
	write(t, hookPath, hookText)
	write(t, config.ProjectMCPPath(cwd), `{"mcpServers":{"repo":{"command":"project-server"}}}`)
	write(t, extPath, "export default () => {}")
	if len(Unapproved(discover(t, cwd))) != 3 {
		t.Fatal("restoring removed content restored revoked project trust")
	}
}

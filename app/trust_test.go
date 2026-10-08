package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/hooks/hooktest"
	"github.com/sebastianrcnt/atto/trust"
	"github.com/sebastianrcnt/atto/tui"
)

func projectTrustApp(t *testing.T) *App {
	t.Helper()
	a := testApp(t)
	writeTestFile(t, filepath.Join(a.cwd, ".git", "HEAD"), "x")
	writeTestFile(t, config.ProjectSettingsPath(a.cwd), `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo project"}]}]}}`)
	writeTestFile(t, config.ProjectMCPPath(a.cwd), `{"mcpServers":{"server":{"command":"run-server"}}}`)
	writeTestFile(t, filepath.Join(config.ProjectExtensionsDir(a.cwd), "deploy.ts"), `export default () => {}`)

	return a
}

func chooseTrust(t *testing.T, a *App, choice string) projectTrustPrompt {
	t.Helper()
	p, ok := a.modal.(projectTrustPrompt)
	if !ok {
		t.Fatalf("expected project trust prompt, got %T", a.modal)
	}
	found := false
	for i, in := range p.list.Items {
		if in.Value == choice {
			p.list.Selected, found = i, true
		}
	}
	if !found {
		t.Fatalf("choice %q missing from %+v", choice, p.list.Items)
	}
	p.HandleInput("\r")
	return p
}

func trustItems(t *testing.T, a *App) []trust.Item {
	t.Helper()
	items, err := trust.Discover(a.cwd)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestProjectTrustPromptAllowsEveryKindByContent(t *testing.T) {
	a := projectTrustApp(t)
	a.ui.Do(func() {
		a.askProjectApprovals()
		p := a.modal.(projectTrustPrompt)
		text := tui.StripEscapes(strings.Join(p.Render(80), "\n"))
		for _, want := range []string{"This project wants to run code", "hook", "SessionStart", "echo project", "mcp", "server", "ext", "deploy", trustAllowAll, trustReview, trustDeny} {
			if !strings.Contains(text, want) {
				t.Errorf("prompt lacks %q:\n%s", want, text)
			}
		}
		if len(p.items) != 3 {
			t.Fatalf("one prompt should contain every pending kind: %+v", p.items)
		}
		chooseTrust(t, a, trustAllowAll)
		if a.modal != nil {
			t.Fatalf("allow all left another prompt open: %T", a.modal)
		}
		if items := trustItems(t, a); items[0].Status != trust.Approved {
			t.Fatal("approved hook was not enabled")
		}
	})
	for _, in := range trustItems(t, a) {
		if in.Status != trust.Approved {
			t.Errorf("allow all did not approve %+v", in)
		}
	}
	// Approval is never a wildcard for new or changed content.
	writeTestFile(t, config.ProjectMCPPath(a.cwd), `{"mcpServers":{"server":{"command":"changed"},"new":{"command":"new"}}}`)
	a.ui.Do(func() {
		a.cmdReload("")
		p := a.modal.(projectTrustPrompt)
		if len(p.items) != 2 || p.items[0].Kind != trust.MCP || p.items[1].Kind != trust.MCP {
			t.Fatalf("reload should only ask about new/changed content: %+v", p.items)
		}
		chooseTrust(t, a, trustDeny)
	})
}

func TestProjectTrustReviewDenialAndEsc(t *testing.T) {
	a := projectTrustApp(t)
	a.ui.Do(func() {
		a.askProjectApprovals()
		chooseTrust(t, a, trustReview)
		p := chooseTrust(t, a, trustAllow)
		if len(p.items) != 1 || p.items[0].Kind != trust.Hook {
			t.Fatalf("review should start with the hook: %+v", p.items)
		}
		p = chooseTrust(t, a, trustDeny)
		if p.items[0].Kind != trust.MCP {
			t.Fatalf("review did not continue to the server: %+v", p.items)
		}
		a.modal.HandleInput("\x1b")
		if a.modal != nil {
			t.Fatalf("review did not finish: %T", a.modal)
		}
		a.askProjectApprovals()
		if a.modal != nil {
			t.Fatal("asked again about content already reviewed in this run")
		}
	})
	items := trustItems(t, a)
	if items[0].Status != trust.Approved || items[1].Status != trust.Denied || items[2].Status != trust.Pending {
		t.Fatalf("review decisions: %+v", items)
	}
	// A fresh session remembers decisions, but an Esc is only "not now".
	a.ui.Do(func() {
		a.trustAsked = nil
		a.askProjectApprovals()
		p := a.modal.(projectTrustPrompt)
		if len(p.items) != 1 || p.items[0].Kind != trust.Extension {
			t.Fatalf("only escaped content should be pending: %+v", p.items)
		}
		chooseTrust(t, a, trustDeny)
		a.trustAsked = nil
		a.askProjectApprovals()
		if a.modal != nil {
			t.Fatal("denials should persist for unchanged content")
		}
	})
}

func TestProjectTrustWaitsForFreeScreenAndStartupDecision(t *testing.T) {
	a := projectTrustApp(t)
	a.ui.Do(func() {

		completed := false
		a.trustDone = func() { completed = true }
		other := &tui.SelectList{Title: "other"}
		a.openModal(other)
		a.askProjectApprovals()
		if a.modal != tui.Component(other) || completed {
			t.Fatal("trust replaced another prompt or prematurely started the session")
		}
		a.closeModal()
		a.askProjectApprovals()
		if completed {
			t.Fatal("session started before the trust decision")
		}
		chooseTrust(t, a, trustDeny)
		if !completed || trustItems(t, a)[0].Status == trust.Approved {
			t.Fatal("denying should finish startup without enabling project hooks")
		}
	})
	for _, in := range trustItems(t, a) {
		if in.Status != trust.Denied {
			t.Fatalf("deny all missed %+v", in)
		}
	}
}

func TestProjectTrustNeverPromptsForUserHooks(t *testing.T) {
	a := testApp(t)
	writeTestFile(t, config.SettingsPath(), `{"hooks":{"SessionStart":[{"hooks":[{"command":"echo user"}]}]}}`)
	a.ui.Do(func() {
		a.askProjectApprovals()
		if a.modal != nil {
			t.Fatal("user-level hooks prompted for project trust")
		}
	})
	if _, err := os.Stat(config.HookApprovalsPath()); !os.IsNotExist(err) {
		t.Fatal("user hook inspection wrote approval storage")
	}
}

func TestProjectTrustStartupHooksRunOnlyAfterApproval(t *testing.T) {
	cwd, _ := testEnv(t)
	a := newApp(nullTerm{}, config.ModelsFile{}, cwd)
	writeTestFile(t, filepath.Join(cwd, ".git", "HEAD"), "x")
	writeTestFile(t, config.ProjectSettingsPath(cwd), `{"hooks":{"SessionStart":[{"hooks":[{"command":"echo project"}]}]}}`)
	userLog := filepath.Join(a.cwd, "user-start.log")
	projectLog := filepath.Join(a.cwd, "project-start.log")
	for path, command := range map[string]string{
		config.SettingsPath():             hooktest.LogStdinNoNewline(userLog),
		config.ProjectSettingsPath(a.cwd): hooktest.LogStdinNoNewline(projectLog),
	} {
		data, _ := json.Marshal(config.Settings{Hooks: map[string][]config.HookMatcher{"SessionStart": {{Hooks: []config.HookSpec{{Type: "command", Command: command}}}}}})
		writeTestFile(t, path, string(data))
	}
	a = startAppStartup(t, cwd, nullTerm{}, false)
	a.ui.Do(func() {
		a.trustDone = func() { a.rpcErr("thread/sessionStart", nil) }
		a.askProjectApprovals()
		for _, path := range []string{userLog, projectLog} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("hooks ran before trust: %s", path)
			}
		}
	})
	a.ui.Do(func() { chooseTrust(t, a, trustAllowAll) })
	within(t, a, "both startup hooks after approval", func() bool {
		u, _ := os.ReadFile(userLog)
		p, _ := os.ReadFile(projectLog)
		return len(u) > 0 && len(p) > 0
	})
	for _, path := range []string{userLog, projectLog} {
		data, _ := os.ReadFile(path)
		if strings.Count(string(data), `"hook_event_name":"SessionStart"`) != 1 {
			t.Fatalf("startup hook should run exactly once: %s", data)
		}
	}
}

func TestReloadTrustPromptWaitsForExistingModal(t *testing.T) {
	a := projectTrustApp(t)
	a.ui.Do(func() {
		other := &tui.SelectList{Title: "other", OnCancel: a.closeModal}
		a.openModal(other)
		a.cmdReload("")
		if a.modal != tui.Component(other) || !a.trustWaiting {
			t.Fatal("reload should defer trust until the existing modal closes")
		}
		a.modal.HandleInput("\x1b")
	})
	within(t, a, "deferred project trust prompt", func() bool { _, ok := a.modal.(projectTrustPrompt); return ok })
	a.ui.Do(func() { chooseTrust(t, a, trustDeny) })
}

package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/mcp"
	"github.com/sebastianrcnt/atto/tui"
)

const (
	mcpAllow    = "Allow"
	mcpDeny     = "Deny"
	mcpAllowAll = "Allow all for this project"
)

func mcpApp(t *testing.T) *App {
	cwd, _ := testEnv(t)
	writeTestFile(t, filepath.Join(cwd, ".git", "HEAD"), "x")
	writeTestFile(t, filepath.Join(cwd, ".mcp.json"), `{"mcpServers":{"one":{"command":"run-one","args":["--flag"]},"two":{"command":"run-two"}}}`)
	return startApp(t, cwd)
}

func statusOf(t *testing.T, a *App, name string) string {
	t.Helper()
	var out struct {
		Servers []mcp.Info `json:"servers"`
	}
	if err := a.conn.c.Call(context.Background(), "mcp/list", map[string]any{"threadId": a.threadID}, &out); err != nil {
		t.Fatal(err)
	}
	for _, in := range out.Servers {
		if in.Name == name {
			return in.Status
		}
	}
	t.Fatalf("no server %s", name)
	return ""
}

func choose(t *testing.T, a *App, choice string) string {
	t.Helper()
	sel, ok := a.modal.(*tui.SelectList)
	if !ok {
		t.Fatalf("approval prompt %T", a.modal)
	}
	for i, it := range sel.Items {
		if it.Value == choice {
			sel.Selected = i
		}
	}
	title := sel.Title
	sel.HandleInput("\r")
	return title
}

func TestMCPApprovalPromptAtSessionStart(t *testing.T) {
	a := mcpApp(t)
	within(t, a, "first approval", func() bool { return a.modal != nil })
	a.ui.Do(func() {
		title := choose(t, a, mcpAllow)
		if !strings.Contains(title, "server one: run-one --flag") {
			t.Errorf("title %q", title)
		}
	})
	within(t, a, "second approval", func() bool { return a.modal != nil && strings.Contains(plainLines(a.renderInput(100)), "server two") })
	a.ui.Do(func() { choose(t, a, mcpDeny) })
	within(t, a, "approvals complete", func() bool { return a.modal == nil })
	settle(a)
	if got := statusOf(t, a, "one"); got != mcp.NotStarted {
		t.Errorf("one=%s", got)
	}
	if got := statusOf(t, a, "two"); got != mcp.DeniedStatus {
		t.Errorf("two=%s", got)
	}
	typeLine(a, "/reload")
	settle(a)
	a.ui.Do(func() {
		if a.modal != nil {
			t.Errorf("asked again: %T", a.modal)
		}
	})
}

func TestMCPApprovalPromptAllowAllAndEsc(t *testing.T) {
	a := mcpApp(t)
	within(t, a, "approval", func() bool { return a.modal != nil })
	a.ui.Do(func() { choose(t, a, mcpAllowAll) })
	within(t, a, "all approved", func() bool { return a.modal == nil })
	settle(a)
	if statusOf(t, a, "one") != mcp.NotStarted || statusOf(t, a, "two") != mcp.NotStarted {
		t.Fatal("allow all failed")
	}
	b := mcpApp(t)
	within(t, b, "first approval", func() bool { return b.modal != nil })
	key(b, "\x1b")
	within(t, b, "second approval", func() bool { return b.modal != nil && strings.Contains(plainLines(b.renderInput(100)), "server two") })
	key(b, "\x1b")
	within(t, b, "no approval", func() bool { return b.modal == nil })
	settle(b)
	if got := statusOf(t, b, "one"); got != mcp.NeedsApproval {
		t.Errorf("after Esc %s", got)
	}
}

func TestMCPApprovalPromptWaitsForAFreeScreen(t *testing.T) {
	cwd, _ := testEnv(t)
	a := startApp(t, cwd)
	writeTestFile(t, filepath.Join(cwd, ".git", "HEAD"), "x")
	writeTestFile(t, filepath.Join(cwd, ".mcp.json"), `{"mcpServers":{"one":{"command":"run-one"}}}`)
	var other *tui.SelectList
	a.ui.Do(func() {
		other = &tui.SelectList{Title: "something else", OnCancel: a.closeModal}
		a.openModal(other)
		a.rpcErr("thread/reload", nil)
	})
	settle(a)
	a.ui.Do(func() {
		if a.modal != tui.Component(other) {
			t.Fatalf("approval replaced another dialog: %T", a.modal)
		}
		a.closeModal()
	})
	within(t, a, "deferred approval", func() bool { return a.modal != nil })
	key(a, "\x1b")
}

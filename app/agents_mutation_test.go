package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/tui"
)

func selectCenter(t *testing.T, a *App, id string) *agentCenter {
	t.Helper()
	var c *agentCenter
	a.ui.Do(func() { a.cmdAgents(""); c = a.modal.(*agentCenter) })
	waitCenter(t, a)
	a.ui.Do(func() {
		found := false
		for i, it := range c.shown() {
			if it.id == id {
				c.sel = i
				found = true
			}
		}
		if !found {
			t.Errorf("inventory missing %s: %+v", id, c.items)
		}
	})
	return c
}
func centerSaved(a *App) *session.Writer {
	w := session.New(a.cwd)
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "selected saved session"}})
	w.Close()
	return w
}
func TestCenterDeleteRequiresInlineConfirmation(t *testing.T) {
	a, _ := recordedApp(t)
	w := centerSaved(a)
	c := selectCenter(t, a, w.ID)
	key(a, "d")
	if text := centerText(a); !strings.Contains(text, "Confirm delete?") {
		t.Fatalf("confirmation missing:\n%s", text)
	}
	if _, err := session.Find(w.ID); err != nil {
		t.Fatal("delete was silent", err)
	}
	key(a, "\x1b")
	if c.confirm != "" {
		t.Fatal("cancel did not clear confirmation")
	}
	key(a, "d")
	key(a, "\r")
	within(t, a, "confirmed delete", func() bool { _, err := session.Find(w.ID); return err != nil })
}
func TestCenterArchiveAndUnarchiveInArchivedView(t *testing.T) {
	a, _ := recordedApp(t)
	w := centerSaved(a)
	c := selectCenter(t, a, w.ID)
	key(a, "a")
	within(t, a, "archive", func() bool { path, err := session.Find(w.ID); return err == nil && strings.HasSuffix(path, ".zst") })
	key(a, "v")
	within(t, a, "archived view", func() bool {
		for i, it := range c.shown() {
			if it.id == w.ID && it.archived {
				c.sel = i
				return true
			}
		}
		return false
	})
	key(a, "a")
	within(t, a, "unarchive", func() bool { path, err := session.Find(w.ID); return err == nil && !strings.HasSuffix(path, ".zst") })
}
func TestCenterLiveWorkerAsksBeforeArchiveDelete(t *testing.T) {
	for _, action := range []string{"a", "d"} {
		t.Run(action, func(t *testing.T) {
			a, _ := recordedApp(t)
			w := centerSaved(a)
			if err := a.conn.c.Call(context.Background(), "thread/resume", map[string]any{"threadId": w.ID}, nil); err != nil {
				t.Fatal(err)
			}
			c := selectCenter(t, a, w.ID)
			key(a, action)
			if !strings.Contains(c.confirm, "Stop it and") {
				t.Fatalf("live worker not confirmed: %s", c.confirm)
			}
			if !a.conn.own.Loaded(w.ID) {
				t.Fatal("worker stopped before confirmation")
			}
			key(a, "n")
			if !a.conn.own.Loaded(w.ID) {
				t.Fatal("cancel stopped worker")
			}
			key(a, action)
			key(a, "y")
			within(t, a, "confirmed live mutation", func() bool { return !a.conn.own.Loaded(w.ID) })
		})
	}
}
func TestResumeCenterArchiveAndDeleteUseProtocol(t *testing.T) {
	a, _ := recordedApp(t)
	w := centerSaved(a)
	c := selectCenter(t, a, w.ID)
	a.ui.Do(func() { c.scope = a.cwd; c.resume = true })
	key(a, "a")
	within(t, a, "resume archive", func() bool { path, err := session.Find(w.ID); return err == nil && strings.HasSuffix(path, ".zst") })
	key(a, "v")
	within(t, a, "resume archived inventory", func() bool {
		for i, it := range c.shown() {
			if it.id == w.ID {
				c.sel = i
				return true
			}
		}
		return false
	})
	key(a, "d")
	if _, err := session.Find(w.ID); err != nil {
		t.Fatal("resume silent delete")
	}
	key(a, "y")
	within(t, a, "resume confirmed delete", func() bool { _, err := session.Find(w.ID); return err != nil })
}
func TestCenterFoldsOnlyFinishedAgentSubtrees(t *testing.T) {
	rows := []server.ThreadSummary{
		{ID: "root", Name: "parent"},
		{ID: "fuel", Name: "fuel", Archived: true, Agent: &server.ThreadAgent{ParentThreadID: "root", Name: "fuel", Lifecycle: agentstate.Closed}},
		{ID: "lint", Name: "lint", Archived: true, Agent: &server.ThreadAgent{ParentThreadID: "fuel", Name: "lint", Lifecycle: agentstate.Closed}},
	}
	c := &agentCenter{}
	c.apply(rows)
	if got := treeIDs(c.shown()); len(got) != 1 || got[0] != "root" {
		t.Fatalf("finished tree not folded: %v", got)
	}
	c.HandleInput(" ")
	if len(c.shown()) != 2 {
		t.Fatal("Space did not unfold parent")
	}
	c.sel = 1
	c.HandleInput(" ")
	if len(c.shown()) != 3 || c.shown()[2].agentPath != "/root/fuel/lint" {
		t.Fatal("Space did not unfold child")
	}
	if text := tui.StripEscapes(strings.Join(c.Render(160), "\n")); !strings.Contains(text, "fuel/lint") {
		t.Fatal("short path missing")
	}
	c.apply(rows)
	if len(c.shown()) != 3 {
		t.Fatal("refresh lost explicit expansion")
	}
	rows[2].Archived = false
	rows[2].Agent.Lifecycle = agentstate.Open
	c = &agentCenter{}
	c.apply(rows)
	if len(c.shown()) != 3 {
		t.Fatal("open descendant folded")
	}
}
func TestCenterRefreshIsExplicitProtocolRequest(t *testing.T) {
	a, _ := recordedApp(t)
	old := listCenter
	calls := make(chan struct{}, 5)
	listCenter = func(client *server.Client) ([]server.ThreadSummary, error) { calls <- struct{}{}; return old(client) }
	defer func() { listCenter = old }()
	selectCenter(t, a, a.threadID)
	select {
	case <-calls:
	default:
		t.Fatal("opening did not list")
	}
	time.Sleep(2100 * time.Millisecond)
	select {
	case <-calls:
		t.Fatal("center polled without refresh key")
	default:
	}
	key(a, "r")
	select {
	case <-calls:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not list")
	}
	waitCenter(t, a)
}
func TestLoadedBlockConfigDedupeHomeProject(t *testing.T) {
	cwd := loadedEnv(t, "http://127.0.0.1:9/v1")
	home := filepath.Dir(cwd)
	a := startApp(t, home)
	blocks := loadedBlocks(a)
	if len(blocks) != 1 {
		t.Fatal("missing Loaded block")
	}
	var configRow string
	for _, line := range blocks[0].Render(180) {
		if strings.Contains(tui.StripEscapes(line), "Config") {
			configRow = tui.StripEscapes(line)
		}
	}
	if strings.Count(configRow, filepath.Join("~", ".atto", "settings.json")) != 1 {
		t.Fatalf("duplicate Config settings: %s", configRow)
	}
	// Verify this really used the same user and project file.
	if _, err := os.Stat(filepath.Join(home, ".atto", "settings.json")); err != nil {
		t.Fatal(err)
	}
}

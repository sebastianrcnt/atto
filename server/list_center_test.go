package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/outputs"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/provider/providertest"
	"github.com/sebastianrcnt/atto/session"
)

func inventory(t *testing.T, c *Client, params map[string]any) []ThreadSummary {
	t.Helper()
	var out struct {
		Threads []ThreadSummary `json:"threads"`
	}
	if err := c.Call(context.Background(), "thread/list", params, &out); err != nil {
		t.Fatal(err)
	}
	return out.Threads
}
func findInventory(rows []ThreadSummary, id string) *ThreadSummary {
	for i := range rows {
		if rows[i].ID == id {
			return &rows[i]
		}
	}
	return nil
}
func inventoryAgent(t *testing.T, parent string) *session.Writer {
	t.Helper()
	w := session.NewAgent(t.TempDir(), parent)
	w.Append(session.Entry{Type: session.TypeName, Name: "lint"})
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "First task"}})
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: "Last answer"}})
	w.Close()
	if err := agentstate.Save(agentstate.State{Session: w.ID, Parent: parent, Name: "lint", Cwd: t.TempDir(), Task: "First task", Preset: "review", Model: "fake/m", Branch: "atto/lint", SpawnedBy: &session.SpawnedBy{Model: "fake/m", Effort: "high", Turn: 3, ToolCallID: "call-1"}, Created: time.Now()}); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestThreadListCenterMetadataDefaultsAndArchiveOptions(t *testing.T) {
	h := newHarness(t)
	parent := session.New(t.TempDir())
	parent.Append(session.Entry{Type: session.TypeName, Name: "Empty recorded parent"})
	parent.Close()
	w := inventoryAgent(t, parent.ID)
	ordinary := session.New(t.TempDir())
	ordinary.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "Old ordinary"}})
	ordinary.Close()
	if _, err := session.Archive(ordinary.Path); err != nil {
		t.Fatal(err)
	}
	if findInventory(inventory(t, h.c, nil), w.ID) != nil {
		t.Fatal("default listed agent")
	}
	rows := inventory(t, h.c, map[string]any{"includeAgents": true})
	row := findInventory(rows, w.ID)
	if row == nil || row.Agent == nil || row.Agent.ParentThreadID != parent.ID || row.Agent.RootThreadID != parent.ID || row.Agent.Depth != 1 || row.Agent.Role != "review" || row.Agent.SpawnedBy.ToolCallID != "call-1" || row.Branch != "atto/lint" || row.LastMessage != "Last answer" || row.Preview != "First task" {
		t.Fatalf("metadata: %+v", row)
	}
	if findInventory(rows, parent.ID) == nil {
		t.Fatal("empty parent missing")
	}
	if findInventory(rows, ordinary.ID) != nil {
		t.Fatal("archive leaked into active scope")
	}
	if h.s.Loaded(w.ID) {
		t.Fatal("inventory started agent")
	}
	if err := h.c.Call(context.Background(), "thread/archive", map[string]any{"threadId": w.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if findInventory(inventory(t, h.c, map[string]any{"includeAgents": true, "includeArchived": true}), w.ID) != nil {
		t.Fatal("closed agent leaked without opt-in")
	}
	row = findInventory(inventory(t, h.c, map[string]any{"includeAgents": true, "includeArchived": true, "includeClosedAgents": true}), w.ID)
	if row == nil || !row.Archived || row.Agent.Lifecycle != agentstate.Closed {
		t.Fatalf("closed metadata: %+v", row)
	}
	if err := h.c.Call(context.Background(), "thread/unarchive", map[string]any{"threadId": w.ID}, nil); err != nil {
		t.Fatal(err)
	}
	st, _ := agentstate.Load(w.ID)
	if st.Lifecycle != agentstate.Closed {
		t.Fatal("unarchive reopened agent")
	}
	if err := h.c.Call(context.Background(), "thread/delete", map[string]any{"threadId": w.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Find(w.ID); err == nil {
		t.Fatal("deleted transcript remains")
	}
	if st, err := agentstate.Load(w.ID); err != nil || st.Lifecycle != agentstate.Closed {
		t.Fatal("closed record deleted", err)
	}
}

func TestThreadListNeedsYouFlags(t *testing.T) {
	h := newHarness(t)
	h.call("prompt/clientOpen", map[string]any{"prompt": map[string]any{"kind": "input", "title": "Need input", "requestId": "inventory"}})
	row := findInventory(inventory(t, h.c, nil), h.id)
	if row == nil || !row.OpenPrompt {
		t.Fatalf("open prompt missing: %+v", row)
	}
	saved := session.New(t.TempDir())
	saved.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "Waiting"}})
	saved.Close()
	g, _ := goal.New("finish")
	g.Status = goal.Blocked
	if err := goal.Save(saved.ID, g); err != nil {
		t.Fatal(err)
	}
	row = findInventory(inventory(t, h.c, nil), saved.ID)
	if row == nil || !row.GoalWaiting {
		t.Fatalf("saved goal missing: %+v", row)
	}
}

func TestThreadDeleteUnloadedResourceCleanup(t *testing.T) {
	h := newHarness(t)
	saved := session.New(t.TempDir())
	saved.Append(session.Entry{Type: session.TypeName, Name: "Delete me"})
	saved.Close()
	dirs := []string{jobs.Root(saved.ID), events.Dir(saved.ID)}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "fixture"), []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	g, _ := goal.New("delete objective")
	_ = goal.Save(saved.ID, g)
	if err := h.c.Call(context.Background(), "thread/delete", map[string]any{"threadId": saved.ID}, nil); err != nil {
		t.Fatal(err)
	}
	for _, path := range append(dirs, goal.Path(saved.ID), saved.Path) {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("resource remains %s: %v", path, err)
		}
	}
}

func TestAgentArchiveDeleteRefuseRunningSubtree(t *testing.T) {
	h := newHarness(t)
	w := inventoryAgent(t, h.id)
	child := inventoryAgent(t, w.ID)
	st, err := agentstate.Load(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	job, err := jobs.StartWorkerTurn(w.ID, t.TempDir(), "running test turn", os.Getpid(), jobs.Control{File: filepath.Join(t.TempDir(), "stop"), Content: "stop"}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = jobs.EndWorkerTurn(w.ID, job.ID, true) })
	st.Turns, st.Job, st.JobOwner = 1, job.ID, w.ID
	if err := agentstate.Save(st); err != nil {
		t.Fatal(err)
	}
	turn := agentstate.Turn{N: 1, Status: agentstate.Running, Started: time.Now().Add(-time.Second), PromptTokens: 120, CachedTokens: 20, OutputTokens: 30}
	if err := agentstate.SaveTurn(child.ID, turn); err != nil {
		t.Fatal(err)
	}
	row := findInventory(inventory(t, h.c, map[string]any{"includeAgents": true}), child.ID)
	if row == nil || row.Agent.LastTurn.Status != agentstate.Running || row.Agent.LastTurn.OutputTokens != 30 || row.Agent.DurationMS < 1000 {
		t.Fatalf("running turn stats %+v", row)
	}
	for _, method := range []string{"thread/archive", "thread/delete"} {
		if err := h.c.Call(context.Background(), method, map[string]any{"threadId": w.ID, "stop": true}, nil); err == nil {
			t.Fatal("silently stopped running subtree", method)
		}
		parent, _ := agentstate.Load(w.ID)
		if parent.Lifecycle != agentstate.Open {
			t.Fatalf("refusal changed parent lifecycle: %s", parent.Lifecycle)
		}
	}
	_ = jobs.EndWorkerTurn(w.ID, job.ID, false)
	turn.Status, turn.Ended = agentstate.Done, time.Now()
	_ = agentstate.SaveTurn(child.ID, turn)
	if err := h.c.Call(context.Background(), "thread/archive", map[string]any{"threadId": w.ID}, nil); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{w.ID, child.ID} {
		st, _ := agentstate.Load(id)
		if st.Lifecycle != agentstate.Closed {
			t.Fatalf("descendant left open %s", id)
		}
	}
}

func TestArchiveDeleteBusyWorkerNeedsConfirmedStop(t *testing.T) {
	for _, method := range []string{"thread/archive", "thread/delete"} {
		t.Run(method, func(t *testing.T) {
			h := newHarness(t, providertest.Reply{Text: "work", Gate: make(chan struct{})})
			h.call("turn/start", map[string]any{"input": "working"})
			h.m.Started(5 * time.Second)
			if _, err := h.try(h.c, method, nil); err == nil {
				t.Fatal("busy worker stopped without confirmation")
			}
			if _, err := h.try(h.c, method, map[string]any{"stop": true}); err != nil {
				t.Fatal(err)
			}
			if h.s.Loaded(h.id) {
				t.Fatal("runtime still loaded")
			}
		})
	}
}

func TestThreadListDefaultExcludesLoadedAgent(t *testing.T) {
	h := newHarness(t)
	w := inventoryAgent(t, h.id)
	if err := h.c.Call(context.Background(), "thread/resume", map[string]any{"threadId": w.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if !h.s.Loaded(w.ID) {
		t.Fatal("fixture not loaded")
	}
	if findInventory(inventory(t, h.c, nil), w.ID) != nil {
		t.Fatal("loaded agent leaked into default")
	}
	gateway := workerFacade(t, h.s)
	client := Connect(context.Background(), gateway)
	defer client.Close()
	if findInventory(inventory(t, client, nil), w.ID) != nil {
		t.Fatal("routed agent leaked into default")
	}
}

func TestThreadDeleteKeepsSharedImagesAndRemovesOutputs(t *testing.T) {
	h := newHarness(t)
	name := strings.Repeat("a", 64) + ".png"
	if err := os.MkdirAll(images.Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	imagePath := filepath.Join(images.Dir(), name)
	if err := os.WriteFile(imagePath, []byte("image fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	var writers []*session.Writer
	for range 2 {
		w := session.New(t.TempDir())
		w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "shared " + name}})
		w.Close()
		writers = append(writers, w)
	}
	if _, err := session.Archive(writers[1].Path); err != nil {
		t.Fatal(err)
	}
	dir := outputs.SessionDir(writers[0].ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "output"), []byte("text"), 0600)
	if err := h.c.Call(context.Background(), "thread/delete", map[string]any{"threadId": writers[0].ID}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("outputs not removed", err)
	}
	if _, err := os.Stat(imagePath); err != nil {
		t.Fatal("shared archive image deleted", err)
	}
	if err := h.c.Call(context.Background(), "thread/delete", map[string]any{"threadId": writers[1].ID}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(imagePath); !os.IsNotExist(err) {
		t.Fatal("unshared image not removed", err)
	}
}

func TestThreadLoadedConfigDedupeHomeProject(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".atto")
	t.Setenv(config.EnvDir, dir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	m := providertest.New(t)
	m.Install(t, dir)
	if err := os.WriteFile(config.SettingsPath(), []byte(`{"model":"fake/m"}`), 0600); err != nil {
		t.Fatal(err)
	}
	s := New("test", home)
	defer s.Close()
	c := Connect(context.Background(), s)
	defer c.Close()
	var info ThreadInfo
	if err := c.Call(context.Background(), "thread/start", map[string]any{"cwd": home}, &info); err != nil {
		t.Fatal(err)
	}
	if info.Context == nil {
		t.Fatal("missing loaded context")
	}
	count := 0
	for _, file := range info.Context.Config {
		if session.SameDir(file.Path, config.SettingsPath()) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("settings repeated in protocol context: %+v", info.Context.Config)
	}
}

func TestAgentArchiveWorktreeCloseRules(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	h := newHarness(t)
	w := inventoryAgent(t, h.id)
	repo, tree := t.TempDir(), filepath.Join(t.TempDir(), "worktree")
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.com")
	_ = os.WriteFile(filepath.Join(repo, "tracked"), []byte("base"), 0600)
	git("add", ".")
	git("commit", "-m", "base")
	base := git("rev-parse", "HEAD")
	branch := "atto/" + w.ID
	git("worktree", "add", "-b", branch, tree, base)
	st, _ := agentstate.Load(w.ID)
	st.Repo, st.Worktree, st.Branch, st.Base = repo, tree, branch, base
	if err := agentstate.Save(st); err != nil {
		t.Fatal(err)
	}
	dirty := filepath.Join(tree, "untracked")
	_ = os.WriteFile(dirty, []byte("dirty"), 0600)
	if err := h.c.Call(context.Background(), "thread/archive", map[string]any{"threadId": w.ID}, nil); err == nil {
		t.Fatal("dirty worktree silently removed")
	}
	if cur, _ := agentstate.Load(w.ID); cur.Lifecycle != agentstate.Open {
		t.Fatal("refusal closed agent")
	}
	_ = os.Remove(dirty)
	if err := h.c.Call(context.Background(), "thread/archive", map[string]any{"threadId": w.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tree); !os.IsNotExist(err) {
		t.Fatal("clean worktree remains", err)
	}
	git("rev-parse", "--verify", "refs/heads/"+branch)
}

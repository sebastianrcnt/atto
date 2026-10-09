package cli

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/session"
)

func spawnExternalAgent(t *testing.T, name string, flags ...string) (agentstate.State, string) {
	t.Helper()
	args := append([]string{"spawn", name, "work"}, flags...)
	out, err := runAgent(t, args...)
	if err != nil {
		t.Fatalf("spawn: %q %v", out, err)
	}
	parent := strings.TrimPrefix(strings.SplitN(out, "\n", 2)[0], "external parent created: ")
	st, err := agentstate.Load(parent, name)
	if err != nil {
		t.Fatalf("spawn state: %q %v", out, err)
	}
	t.Cleanup(func() { jobs.KillAll(st.Parent); jobs.KillAll(st.Session) })
	return st, out
}

func TestExternalSpawnsOwnFreshParentsAndLabelsAreProjectScoped(t *testing.T) {
	agentServer(t, func(int, string) string { return textAnswer("done") })
	project := t.TempDir()
	t.Chdir(project)
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	enableAgents(t, "")
	first, output := spawnExternalAgent(t, "panes")
	if !strings.Contains(output, "wait: atto agent wait @"+first.Session) {
		t.Fatalf("outside hints must use exact address: %q", output)
	}
	second, _ := spawnExternalAgent(t, "panes")
	if first.Parent == second.Parent || first.Session == second.Session {
		t.Fatal("outside spawns shared a namespace")
	}
	for _, st := range []agentstate.State{first, second} {
		if len(agentstate.List(st.Parent)) != 1 {
			t.Fatal("parent must own only this spawn")
		}
		if _, err := runAgent(t, "wait", "@"+st.Session, "-timeout", "30s"); err != nil {
			t.Fatal(err)
		}
	}
	first.Created, second.Created = time.Now().Add(-2*time.Minute), time.Now().Add(-time.Hour)
	for _, st := range []agentstate.State{first, second} {
		if err := agentstate.Save(st); err != nil {
			t.Fatal(err)
		}
	}
	for _, sub := range []string{"wait", "report", "send", "task", "close"} {
		args := []string{sub, "panes"}
		if sub == "send" || sub == "task" {
			args = append(args, "hello")
		}
		_, err := runAgent(t, args...)
		if err == nil || !strings.Contains(err.Error(), "2 agents named panes:") || !strings.Contains(err.Error(), "@"+first.Session+" (done, 2m)") || !strings.Contains(err.Error(), "@"+second.Session+" (done, 1h)") || !strings.Contains(err.Error(), "use @id") {
			t.Fatalf("%s ambiguity: %v", sub, err)
		}
	}
	nested := savedIDAgent(t, first.Session, "lint")
	if _, err := runAgent(t, "report", "panes/lint"); err == nil || !strings.Contains(err.Error(), "2 agents named panes") {
		t.Fatalf("first path segment must disambiguate first: %v", err)
	}
	if _, err := runAgent(t, "report", "missing"); !errors.Is(err, agentstate.ErrNotFound) || !strings.Contains(err.Error(), "atto agent list") {
		t.Fatalf("missing label: %v", err)
	}
	if _, err := runAgent(t, "report", "lint"); !errors.Is(err, agentstate.ErrNotFound) {
		t.Fatal("nested names are not outside-spawned labels:", err)
	}
	// A subdirectory resolves to the same git project and lists all parents.
	child := filepath.Join(project, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(child)
	out, err := runAgent(t, "list")
	if err != nil || !strings.Contains(out, "@"+first.Session) || !strings.Contains(out, "@"+second.Session) || !strings.Contains(out, "@"+nested.Session) {
		t.Fatalf("project list: %q %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(config.Dir(), "external_parents")); !os.IsNotExist(err) {
		t.Fatal("spawn created shared project mapping:", err)
	}
	// Explicit parents retain their exact names/paths despite project ambiguity.
	if out, err := runAgent(t, "report", "panes/lint", "-session", first.Parent); err != nil || !strings.Contains(out, "saved answer") {
		t.Fatalf("explicit path: %q %v", out, err)
	}
	// Another project's label lookup is isolated, but @id is directory independent.
	t.Chdir(t.TempDir())
	if _, err := runAgent(t, "report", "panes"); !errors.Is(err, agentstate.ErrNotFound) {
		t.Fatalf("other project found label: %v", err)
	}
	if out, err := runAgent(t, "list"); err != nil || out != "no agents\n" {
		t.Fatalf("other project list: %q %v", out, err)
	}
	if out, err := runAgent(t, "report", "@"+first.Session); err != nil || !strings.Contains(out, "done") {
		t.Fatalf("global ID: %q %v", out, err)
	}
	if out, err := runAgent(t, "list", "-all"); err != nil || !strings.Contains(out, "@"+first.Session) || !strings.Contains(out, "@"+second.Session) {
		t.Fatalf("all projects: %q %v", out, err)
	}
	// Close one tree by ID from elsewhere; archive only its own fresh parent.
	if _, err := runAgent(t, "close", "@"+first.Session); err != nil {
		t.Fatal(err)
	}
	if path, err := session.Find(first.Parent); err != nil || !isArchived(path) {
		t.Fatalf("closed parent's archive: %s %v", path, err)
	}
	if path, err := session.Find(second.Parent); err != nil || isArchived(path) {
		t.Fatalf("other parent archived: %s %v", path, err)
	}
	if _, err := runAgent(t, "report", "@"+nested.Session); !errors.Is(err, agentstate.ErrClosed) {
		t.Fatal("descendant not closed:", err)
	}
	t.Chdir(project)
	if out, err := runAgent(t, "report", "panes", "-json"); err != nil || !strings.Contains(out, `"session":"`+second.Session+`"`) {
		t.Fatalf("unique label after close: %q %v", out, err)
	}
	if _, err := runAgent(t, "send", "panes", "unique"); err != nil {
		t.Fatal(err)
	}
	if evs := events.Drain(second.Session); len(evs) != 1 {
		t.Fatalf("unique label send: %+v", evs)
	}
	if _, err := runAgent(t, "close", "panes"); err != nil {
		t.Fatal(err)
	}
	if path, err := session.Find(second.Parent); err != nil || !isArchived(path) {
		t.Fatalf("unique-label close parent: %s %v", path, err)
	}
}

func TestExternalUniqueLabelsAndPathsUseTheAgentTree(t *testing.T) {
	agentServer(t, func(int, string) string { return textAnswer("done") })
	t.Chdir(t.TempDir())
	enableAgents(t, "")
	st, _ := spawnExternalAgent(t, "panes")
	if _, err := runAgent(t, "wait", "panes", "-timeout", "30s"); err != nil {
		t.Fatal(err)
	}
	nested := savedIDAgent(t, st.Session, "lint")
	for _, addr := range []string{"panes/lint", "/root/panes/lint"} {
		if out, err := runAgent(t, "show", addr, "-json"); err != nil || !strings.Contains(out, `"session":"`+nested.Session+`"`) {
			t.Fatalf("unique path: %q %v", out, err)
		}
		if _, err := runAgent(t, "send", addr, "hello"); err != nil {
			t.Fatal(err)
		}
	}
	if evs := events.Drain(nested.Session); len(evs) != 2 {
		t.Fatalf("path send: %+v", evs)
	}
	if _, err := runAgent(t, "task", "panes", "follow-up"); err != nil {
		t.Fatal(err)
	}
	if _, err := runAgent(t, "wait", "panes", "-timeout", "30s"); err != nil {
		t.Fatal(err)
	}
	// Without a selected parent there is no arbitrary shared /root to address.
	for _, addr := range []string{"/root", ".."} {
		if _, err := runAgent(t, "send", addr, "hello"); err == nil || !strings.Contains(err.Error(), "pass -session PARENT") {
			t.Fatalf("implicit root: %v", err)
		}
	}
	if _, err := runAgent(t, "close", "panes"); err != nil {
		t.Fatal(err)
	}
}

func TestLegacySharedExternalParentRemainsAddressableWithoutMigration(t *testing.T) {
	agentServer(t, func(int, string) string { return textAnswer("done") })
	t.Chdir(t.TempDir())
	enableAgents(t, "")
	parent, err := newExternalParent(&strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	a := savedIDAgent(t, parent, "a")
	b := savedIDAgent(t, parent, "b")
	cwd, _ := os.Getwd()
	mapping := filepath.Join(config.Dir(), "external_parents", fmt.Sprintf("%x", sha256.Sum256([]byte(externalProject(cwd)))))
	if err := os.MkdirAll(filepath.Dir(mapping), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte(parent + "\n")
	if err := os.WriteFile(mapping, data, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, st := range []agentstate.State{a, b} {
		for _, addr := range []string{st.Name, "@" + st.Session} {
			if out, err := runAgent(t, "report", addr, "-json"); err != nil || !strings.Contains(out, `"session":"`+st.Session+`"`) {
				t.Fatalf("legacy address: %q %v", out, err)
			}
		}
	}
	if out, err := runAgent(t, "list"); err != nil || !strings.Contains(out, "@"+a.Session) || !strings.Contains(out, "@"+b.Session) {
		t.Fatalf("legacy list: %q %v", out, err)
	}
	fresh, _ := spawnExternalAgent(t, "a")
	if fresh.Parent == parent {
		t.Fatal("legacy mapping was reused")
	}
	if got, err := os.ReadFile(mapping); err != nil || string(got) != string(data) {
		t.Fatal("legacy mapping was migrated or modified:", err)
	}
	if _, err := runAgent(t, "report", "a"); err == nil || !strings.Contains(err.Error(), "2 agents named a") {
		t.Fatal("legacy and fresh labels not considered together:", err)
	}
	if _, err := runAgent(t, "close", "@"+a.Session); err != nil {
		t.Fatal(err)
	}
	if path, err := session.Find(parent); err != nil || isArchived(path) {
		t.Fatalf("shared legacy parent archived too soon: %s %v", path, err)
	}
	if _, err := runAgent(t, "close", "b"); err != nil {
		t.Fatal(err)
	}
	if path, err := session.Find(parent); err != nil || !isArchived(path) {
		t.Fatalf("legacy parent not archived: %s %v", path, err)
	}
	if _, err := os.Stat(mapping); !os.IsNotExist(err) {
		t.Fatal("legacy mapping not forgotten:", err)
	}
	if _, err := runAgent(t, "wait", "@"+fresh.Session, "-timeout", "30s"); err != nil {
		t.Fatal(err)
	}
	if _, err := runAgent(t, "close", "a"); err != nil {
		t.Fatal(err)
	}
}

func TestExternalProjectBulkCloseAndExplicitParentUniqueness(t *testing.T) {
	agentServer(t, func(int, string) string { return textAnswer("done") })
	project := t.TempDir()
	t.Chdir(project)
	enableAgents(t, "")
	var projectAgents []agentstate.State
	for range 2 {
		st, _ := spawnExternalAgent(t, "same")
		projectAgents = append(projectAgents, st)
		if _, err := runAgent(t, "wait", "@"+st.Session, "-timeout", "30s"); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(t.TempDir())
	other, _ := spawnExternalAgent(t, "same")
	if _, err := runAgent(t, "wait", "same", "-timeout", "30s"); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	if _, err := runAgent(t, "close", "-done"); err != nil {
		t.Fatal(err)
	}
	for _, st := range projectAgents {
		if _, err := runAgent(t, "report", "@"+st.Session); !errors.Is(err, agentstate.ErrClosed) {
			t.Fatal("project bulk close:", err)
		}
	}
	if _, err := runAgent(t, "report", "@"+other.Session); err != nil {
		t.Fatal("other project bulk-closed:", err)
	}
	if _, err := runAgent(t, "spawn", "same", "work", "-session", "explicit-root"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { jobs.KillAll("explicit-root") })
	if _, err := runAgent(t, "spawn", "same", "work", "-session", "explicit-root"); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatal("explicit parent no longer enforces names:", err)
	}
	t.Setenv("ATTO_SESSION_ID", "model-root")
	if _, err := runAgent(t, "spawn", "same", "work"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { jobs.KillAll("model-root") })
	if _, err := runAgent(t, "spawn", "same", "work"); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatal("model parent no longer enforces names:", err)
	}
	if out, err := runAgent(t, "list"); err != nil || !strings.Contains(out, "/root/same") || strings.Contains(out, other.Session) {
		t.Fatalf("model names/tree changed: %q %v", out, err)
	}
}

func TestExternalFailedSpawnArchivesEmptyFreshParent(t *testing.T) {
	agentServer(t, func(int, string) string { return "" })
	t.Chdir(t.TempDir())
	enableAgents(t, "")
	out, err := runAgent(t, "spawn", "Bad_Name", "work")
	if err == nil {
		t.Fatal("invalid spawn succeeded")
	}
	parent := strings.TrimPrefix(strings.SplitN(out, "\n", 2)[0], "external parent created: ")
	if path, err := session.Find(parent); err != nil || !isArchived(path) {
		t.Fatalf("failed spawn leaked parent: %s %v", path, err)
	}
}

func TestExternalWaitDefaultsToAnyProjectParent(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	agentServer(t, func(int, string) string { started <- struct{}{}; <-release; return textAnswer("done") })
	t.Cleanup(unblock)
	t.Chdir(t.TempDir())
	enableAgents(t, "")
	first, _ := spawnExternalAgent(t, "panes")
	second, _ := spawnExternalAgent(t, "panes")
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not call model")
	}
	timer := time.AfterFunc(100*time.Millisecond, unblock)
	defer timer.Stop()
	out, err := runAgent(t, "wait", "-json", "-timeout", "30s")
	if err != nil || (!strings.Contains(out, `"session":"`+first.Session+`"`) && !strings.Contains(out, `"session":"`+second.Session+`"`)) {
		t.Fatalf("wait any outside parent: %q %v", out, err)
	}
	if _, err := runAgent(t, "wait", "@"+first.Session, "-timeout", "30s"); err != nil {
		t.Fatal(err)
	}
	if _, err := runAgent(t, "wait", "@"+second.Session, "-timeout", "30s"); err != nil {
		t.Fatal(err)
	}
}

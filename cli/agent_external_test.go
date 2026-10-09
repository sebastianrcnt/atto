package cli

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
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

var spawnedID = regexp.MustCompile(`\(@([0-9a-f]+), session`)

// spawnExternalAgent starts an agent from a plain shell, as an outside
// orchestrator does, and returns its record and the command's output.
func spawnExternalAgent(t *testing.T, name string, flags ...string) (agentstate.State, string) {
	t.Helper()
	args := append([]string{"spawn", name, "work"}, flags...)
	out, err := runAgent(t, args...)
	if err != nil {
		t.Fatalf("spawn: %q %v", out, err)
	}
	m := spawnedID.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("spawn printed no @ID: %q", out)
	}
	st, err := agentstate.Load(m[1])
	if err != nil {
		t.Fatalf("spawn state: %q %v", out, err)
	}
	t.Cleanup(func() { jobs.KillAll(st.Session) })
	return st, out
}

func gitProject(t *testing.T) string {
	t.Helper()
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	return project
}

func TestOutsideSpawnsAreParentlessRootsAndLabelsAreProjectScoped(t *testing.T) {
	agentServer(t, func(int, string) string { return textAnswer("done") })
	project := gitProject(t)
	enableAgents(t, "")
	first, output := spawnExternalAgent(t, "panes")
	second, _ := spawnExternalAgent(t, "panes") // outside roots may share a name
	for _, want := range []string{"agent panes started (@" + first.Session, "role general", "project ", "job 1 of session " + first.Session,
		"poll it with atto agent wait @" + first.Session, "atto agent report @" + first.Session} {
		if !strings.Contains(strings.ReplaceAll(output, "Nothing sends its answer anywhere: ", ""), want) {
			t.Fatalf("outside spawn output lacks %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "external parent") || strings.Contains(output, "reaches you") {
		t.Fatalf("outside output speaks of a parent: %q", output)
	}
	// Parentless roots of their own trees, at /root, with the project recorded.
	for _, st := range []agentstate.State{first, second} {
		if st.Parent != "" || st.Root != st.Session || st.Depth != 0 || st.Path != "/root" || st.Origin != session.OriginExternal || st.Lifecycle != agentstate.Open {
			t.Fatalf("root record: %+v", st)
		}
		if st.Project != externalProject(project) || st.SpawnCwd != project || st.JobOwner != st.Session {
			t.Fatalf("project/job owner: %+v", st)
		}
		if st.SpawnedBy == nil || st.SpawnedBy.Origin != session.SpawnOutside || st.SpawnedBy.Session != nil || st.SpawnedBy.Cwd != project {
			t.Fatalf("spawnedBy: %+v", st.SpawnedBy)
		}
		path, err := session.Find(st.Session)
		if err != nil {
			t.Fatal(err)
		}
		h, _, err := session.Load(path)
		if err != nil || h.Agent == nil || !h.Agent.IsRoot() || h.Agent.Project != st.Project || h.External || h.AgentOf != "" || h.Agent.Origin != session.OriginExternal {
			t.Fatalf("header: %+v %v", h, err)
		}
		if _, err := runAgent(t, "wait", "@"+st.Session, "-timeout", "30s"); err != nil {
			t.Fatal(err)
		}
	}
	// No fake parent sessions, no mapping, no _up, _closed or parent directories.
	if _, err := os.Stat(filepath.Join(config.Dir(), "external_parents")); !os.IsNotExist(err) {
		t.Fatal("spawn created a shared project mapping:", err)
	}
	ents, _ := os.ReadDir(config.AgentStateDir())
	for _, e := range ents {
		if e.IsDir() && e.Name() != ".coord" {
			t.Fatalf("a directory in agent-state: %s", e.Name())
		}
	}
	// A turn of a root is a job of its own session; nothing was posted anywhere.
	if jl := jobs.List(first.Session); len(jl) != 1 || jl[0].Kind() != "agent" || jl[0].Label() != "agent panes" {
		t.Fatalf("root turn job: %+v", jl)
	}
	if evs := events.Drain(first.Session); len(evs) != 0 {
		t.Fatalf("a root hears about its own turn: %+v", evs)
	}
	list, _ := runAgent(t, "list")
	if strings.Count(list, "panes ") < 2 || strings.Contains(list, "/root") {
		t.Fatalf("outside list spells roots by label, not /root: %q", list)
	}
	// Duplicate labels list candidates with their IDs, status and age.
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
	if _, err := runAgent(t, "report", "panes/lint"); err == nil || !strings.Contains(err.Error(), "2 agents named panes") {
		t.Fatalf("first path segment must disambiguate first: %v", err)
	}
	if _, err := runAgent(t, "report", "missing"); !errors.Is(err, agentstate.ErrNotFound) || !strings.Contains(err.Error(), "atto agent list") {
		t.Fatalf("missing label: %v", err)
	}
	// /root/NAME is a compatibility spelling of the project selector; bare /root needs -session.
	if _, err := runAgent(t, "report", "/root/panes"); err == nil || !strings.Contains(err.Error(), "2 agents named panes") {
		t.Fatalf("/root/panes: %v", err)
	}
	if _, err := runAgent(t, "report", "/root"); err == nil || !strings.Contains(err.Error(), "-session") {
		t.Fatalf("/root: %v", err)
	}
	// A subdirectory is the same project; another project is isolated, @id is not.
	child := filepath.Join(project, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(child)
	out, err := runAgent(t, "list")
	if err != nil || !strings.Contains(out, "@"+first.Session) || !strings.Contains(out, "@"+second.Session) {
		t.Fatalf("project list: %q %v", out, err)
	}
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
	// Close one by ID from elsewhere; its session is archived, the other is untouched.
	if out, err := runAgent(t, "close", "@"+first.Session); err != nil || !strings.Contains(out, "closed agent panes") {
		t.Fatalf("close: %q %v", out, err)
	}
	if path, err := session.Find(first.Session); err != nil || !isArchived(path) {
		t.Fatalf("closed agent's archive: %s %v", path, err)
	}
	if path, err := session.Find(second.Session); err != nil || isArchived(path) {
		t.Fatalf("other agent archived: %s %v", path, err)
	}
	closed, err := agentstate.Load(first.Session)
	if err != nil || closed.Lifecycle != agentstate.Closed || closed.Archive == "" || closed.Closed.IsZero() {
		t.Fatalf("closed record: %+v %v", closed, err)
	}
	if _, err := runAgent(t, "report", "@"+first.Session); !errors.Is(err, agentstate.ErrClosed) {
		t.Fatal("closed ID:", err)
	}
	// The label is unique again.
	t.Chdir(project)
	if out, err := runAgent(t, "report", "panes", "-json"); err != nil || !strings.Contains(out, `"session":"`+second.Session+`"`) {
		t.Fatalf("unique label after close: %q %v", out, err)
	}
	if _, err := runAgent(t, "send", "panes", "unique"); err != nil {
		t.Fatal(err)
	}
	if evs := events.Drain(second.Session); len(evs) != 1 || !strings.Contains(evs[0].Text, "From: external\nTo: /root\n") {
		t.Fatalf("unique label send: %+v", evs)
	}
	if _, err := runAgent(t, "close", "panes"); err != nil {
		t.Fatal(err)
	}
}

func TestOutsideRootsSpawnOneChildLevelAndTheAnswerGoesOnlyToTheRecordedParent(t *testing.T) {
	agentServer(t, func(int, string) string { return textAnswer("done") })
	gitProject(t)
	enableAgents(t, "")
	root, _ := spawnExternalAgent(t, "panes")
	if _, err := runAgent(t, "wait", "@"+root.Session, "-timeout", "30s"); err != nil {
		t.Fatal(err)
	}
	// An outside caller can start a child of a real session with -session.
	out, err := runAgent(t, "spawn", "lint", "work", "-session", root.Session)
	if err != nil || !strings.Contains(out, "agent panes/lint started") {
		t.Fatalf("child of an outside root: %q %v", out, err)
	}
	lint, err := agentstate.LoadChild(root.Session, "lint")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { jobs.KillAll(lint.Session) })
	if lint.Parent != root.Session || lint.Root != root.Session || lint.Depth != 1 || lint.Path != "/root/lint" || lint.Origin != session.OriginExternal || lint.JobOwner != root.Session {
		t.Fatalf("child record: %+v", lint)
	}
	if lint.SpawnedBy == nil || lint.SpawnedBy.Origin != session.SpawnExplicitSession || lint.SpawnedBy.Session == nil || *lint.SpawnedBy.Session != root.Session {
		t.Fatalf("explicit-session spawn: %+v", lint.SpawnedBy)
	}
	// Its job is a job of the parent (the root), distinct from the root's own turn job.
	if lint.Job == 0 || lint.Job == root.Job {
		t.Fatalf("jobs: child %d root %d", lint.Job, root.Job)
	}
	if _, err := runAgent(t, "wait", "@"+lint.Session, "-timeout", "30s"); err != nil {
		t.Fatal(err)
	}
	// The final answer was waiting for the recorded parent and wait took it.
	if evs := events.Drain(root.Session); len(evs) != 0 {
		t.Fatalf("inbox after wait: %+v", evs)
	}
	// An outside follow-up task does not make the sender a parent: the answer
	// still goes to the recorded parent only.
	if _, err := runAgent(t, "task", "@"+lint.Session, "again"); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if !lint.Latest().Status.Active() && lint.Latest().N == 0 {
			break
		}
		cur, _ := agentstate.Load(lint.Session)
		if !cur.Latest().Status.Active() && cur.Turns == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("follow-up did not finish")
		}
	}
	evs := events.Drain(root.Session)
	if len(evs) != 1 || !strings.Contains(evs[0].Text, "Message Type: FINAL_ANSWER\nFrom: /root/lint\nTo: /root\n") {
		t.Fatalf("answer for the recorded parent: %+v", evs)
	}
	// A root has no recipient: it never gets an event for its own turn, and a
	// task from outside is addressed from "external".
	if _, err := runAgent(t, "task", "@"+root.Session, "more"); err != nil {
		t.Fatal(err)
	}
	if _, err := runAgent(t, "wait", "@"+root.Session, "-timeout", "30s"); err != nil {
		t.Fatal(err)
	}
	for _, e := range events.Drain(root.Session) {
		if strings.Contains(e.Text, "FINAL_ANSWER") && strings.Contains(e.Text, "From: /root\n") {
			t.Fatalf("a root sent itself its answer: %+v", e)
		}
	}
	// Nesting has no limit: the child starts a child of its own.
	t.Setenv("ATTO_SESSION_ID", lint.Session)
	if out, err := runAgent(t, "spawn", "deep", "work"); err != nil || !strings.Contains(out, "agent /root/lint/deep started") {
		t.Fatalf("grandchild: %q %v", out, err)
	}
	deep, err := agentstate.LoadChild(lint.Session, "deep")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { jobs.KillAll(deep.Session) })
	if deep.Depth != 2 || deep.Root != root.Session {
		t.Fatalf("grandchild record: %+v", deep)
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
	// A closed child's name is free again under the same parent.
	child, err := agentstate.LoadChild("model-root", "same")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runAgent(t, "wait", "same", "-timeout", "30s"); err != nil {
		t.Fatal(err)
	}
	if _, err := runAgent(t, "close", "same"); err != nil {
		t.Fatal(err)
	}
	if _, err := runAgent(t, "spawn", "same", "again"); err != nil {
		t.Fatal("name not freed by close:", err)
	}
	if again, err := agentstate.LoadChild("model-root", "same"); err != nil || again.Session == child.Session {
		t.Fatalf("a closed agent's session was reused: %+v %v", again, err)
	}
	t.Cleanup(func() { jobs.KillAll("model-root") })
}

func TestFailedOutsideSpawnLeavesNothingBehind(t *testing.T) {
	agentServer(t, func(int, string) string { return "" })
	t.Chdir(t.TempDir())
	enableAgents(t, "")
	if _, err := runAgent(t, "spawn", "Bad_Name", "work"); err == nil {
		t.Fatal("invalid spawn succeeded")
	}
	if _, err := runAgent(t, "spawn", "ok", "work", "-m", "nope/nope"); err == nil {
		t.Fatal("unknown model accepted")
	}
	if ents, _ := os.ReadDir(config.SessionsDir()); len(ents) != 0 {
		t.Fatalf("sessions left: %v", ents)
	}
	if got := agentstate.AllWithClosed(); len(got) != 0 {
		t.Fatalf("records left: %+v", got)
	}
	if out, err := runAgent(t, "list"); err != nil || out != "no agents\n" {
		t.Fatalf("list: %q %v", out, err)
	}
}

func TestExternalWaitDefaultsToAnyProjectRoot(t *testing.T) {
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
		t.Fatalf("wait any outside root: %q %v", out, err)
	}
	for _, st := range []agentstate.State{first, second} {
		if out, err := runAgent(t, "wait", "@"+st.Session, "-timeout", "30s", "-json"); err != nil || !strings.Contains(out, `"jobOwner":"`+st.Session+`"`) || !strings.Contains(out, `"origin":"external"`) {
			t.Fatalf("wait -json: %q %v", out, err)
		}
	}
}

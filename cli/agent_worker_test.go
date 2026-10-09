package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/daemon"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/session"
)

// useWorkers makes atto agent run turns in session workers of a daemon this
// test starts (the test binary serves both, see TestMain).
func useWorkers(t *testing.T) {
	t.Helper()
	t.Setenv("ATTO_NO_DAEMON", "")
	t.Setenv("ATTO_WORKER_RETENTION", "") // the default minute: tests that retire set their own
	t.Cleanup(func() {
		_ = daemon.Stop(true)
		time.Sleep(100 * time.Millisecond)
	})
	t.Cleanup(func() {
		if t.Failed() {
			b, _ := os.ReadFile(daemon.LogPath())
			t.Logf("daemon log:\n%s", b)
		}
	})
}

func workerOfSession(t *testing.T, id string) (daemon.Worker, bool) {
	t.Helper()
	ws, err := daemon.Workers()
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range ws {
		if w.Session == id {
			return w, true
		}
	}
	return daemon.Worker{}, false
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(30 * time.Millisecond) {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func TestAgentTurnsRunInTheSessionWorker(t *testing.T) {
	bodies := agentServer(t, func(n int, _ string) string { return textAnswer("answer " + string(rune('0'+n))) })
	useWorkers(t)
	t.Chdir(t.TempDir())
	enableAgents(t, "")
	const parent = "p1"

	out, err := runAgent(t, "spawn", "a", "work", "-session", parent)
	if err != nil {
		t.Fatalf("%q %v", out, err)
	}
	st, err := agentstate.LoadChild(parent, "a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { jobs.KillAll(parent); jobs.KillAll(st.Session) })
	// The agent has a worker of its own session; its turn is a job of the parent,
	// listed like any agent turn, and no _agent-turn process exists for it.
	w, ok := workerOfSession(t, st.Session)
	if !ok {
		t.Fatal("no worker for the agent's session")
	}
	j, err := jobs.Get(parent, st.Job)
	if err != nil || !j.InWorker() || j.Kind() != "agent" || j.Label() != "agent a" || j.SupervisorPID != w.PID {
		t.Fatalf("turn job: %+v %v", j, err)
	}
	var list strings.Builder
	if err := RunJob([]string{"list", "-session", parent}, &list); err != nil || !strings.Contains(list.String(), "agent") || !strings.Contains(list.String(), "agent a") {
		t.Fatalf("job list: %q %v", list.String(), err)
	}
	out, err = runAgent(t, "wait", "a", "-timeout", "30s", "-session", parent)
	if err != nil || !strings.Contains(out, "answer 1") || !strings.Contains(out, "turn 1 done") {
		t.Fatalf("wait: %q %v", out, err)
	}
	// The worker's runtime is the agent's: its prompt says what it is, with the
	// model and effort it was started with.
	if b := bodies(); len(b) != 1 || !strings.Contains(b[0], "You are agent /root/a (session "+st.Session+")") || !strings.Contains(b[0], `"model":"m"`) || !strings.Contains(b[0], "work") {
		t.Fatalf("request: %v", b)
	}
	// The job ended with the turn; the answer reached the parent once, and wait took it.
	waitUntil(t, "the turn job to end", func() bool { j, _ := jobs.Get(parent, st.Job); return !j.Active() })
	if j, _ := jobs.Get(parent, st.Job); j.Status != jobs.Exited {
		t.Fatalf("turn job: %+v", j)
	}
	if evs := events.Drain(parent); len(evs) != 0 {
		t.Fatalf("parent inbox after wait: %+v", evs)
	}
	// A second turn runs in the same worker, with the first one's conversation.
	if out, err := runAgent(t, "task", "a", "more", "-session", parent); err != nil || !strings.Contains(out, "turn 2 started") {
		t.Fatalf("task: %q %v", out, err)
	}
	if out, err := runAgent(t, "wait", "a", "-timeout", "30s", "-session", parent); err != nil || !strings.Contains(out, "answer 2") || !strings.Contains(out, "turn 2 done") {
		t.Fatalf("wait 2: %q %v", out, err)
	}
	if again, ok := workerOfSession(t, st.Session); !ok || again.PID != w.PID {
		t.Fatalf("a second worker was started: %+v", again)
	}
	// report -json fields are the ones of the job path.
	rep, err := runAgent(t, "report", "a", "-json", "-session", parent)
	var r struct {
		Name, Status, Session, Message string
		Turn                           int
		Tokens                         struct{ In, Cached, Out int }
		Duration                       float64
		Job                            int
		JobOwner                       string
	}
	if err != nil || json.Unmarshal([]byte(rep), &r) != nil || r.Name != "a" || r.Status != "done" || r.Turn != 2 || r.Message != "answer 2" || r.Tokens.In != 100 || r.Tokens.Out != 7 || r.Duration <= 0 || r.JobOwner != parent {
		t.Fatalf("report: %q %v", rep, err)
	}
	// Closing the agent closes its worker and archives the session.
	if _, err := runAgent(t, "close", "a", "-session", parent); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the worker to end with the agent", func() bool { _, ok := workerOfSession(t, st.Session); return !ok })
	if p, err := session.Find(st.Session); err != nil || !isArchived(p) {
		t.Fatalf("archive: %s %v", p, err)
	}
}

// The final answer of an agent whose turn ran in a worker goes to its parent
// when nobody waits.
func TestWorkerTurnDeliversFinalAnswerToTheParent(t *testing.T) {
	agentServer(t, func(int, string) string { return textAnswer("fine work") })
	useWorkers(t)
	t.Chdir(t.TempDir())
	enableAgents(t, "")
	if _, err := runAgent(t, "spawn", "a", "work", "-session", "p1"); err != nil {
		t.Fatal(err)
	}
	st, _ := agentstate.LoadChild("p1", "a")
	t.Cleanup(func() { jobs.KillAll("p1"); jobs.KillAll(st.Session) })
	var got []events.Event
	waitUntil(t, "the final answer", func() bool {
		got = append(got, events.Drain("p1")...)
		return len(got) > 0
	})
	if len(got) != 1 || got[0].Source != "agent" || !strings.Contains(got[0].Text, "Message Type: FINAL_ANSWER\nFrom: /root/a\nTo: /root\n") || !strings.Contains(got[0].Text, "fine work") {
		t.Fatalf("final answer: %+v", got)
	}
}

// A client attaches to the agent session while a turn runs, sees the live
// items and the turn's end, like any session: the worker is the writer.
func TestClientAttachesToARunningAgentTurn(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	agentServer(t, func(int, string) string { started <- struct{}{}; <-release; return textAnswer("attached answer") })
	useWorkers(t)
	t.Chdir(t.TempDir())
	enableAgents(t, "")
	if _, err := runAgent(t, "spawn", "a", "the live task", "-session", "p1"); err != nil {
		t.Fatal(err)
	}
	st, _ := agentstate.LoadChild("p1", "a")
	t.Cleanup(func() { jobs.KillAll("p1"); jobs.KillAll(st.Session) })
	select {
	case <-started:
	case <-time.After(20 * time.Second):
		t.Fatal("the model was never called")
	}
	w, ok := workerOfSession(t, st.Session)
	if !ok {
		t.Fatal("no worker")
	}
	nc, err := daemon.DialWorker(w)
	if err != nil {
		t.Fatal(err)
	}
	c := server.NewClient(nc)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.Call(ctx, "initialize", map[string]any{"protocolVersions": []int{server.ProtocolVersion}}, nil); err != nil {
		t.Fatal(err)
	}
	var info server.ThreadInfo
	if err := c.Call(ctx, "thread/attach", map[string]any{"threadId": st.Session}, &info); err != nil {
		t.Fatal(err)
	}
	snap, _ := json.Marshal(info.Items)
	if !info.Busy || info.ReadOnly != "" || !strings.Contains(string(snap), "the live task") {
		t.Fatalf("attached to a running turn: busy %v readOnly %q items %s", info.Busy, info.ReadOnly, snap)
	}
	close(release)
	var answer string
	for answer == "" {
		select {
		case n := <-c.Events():
			if n.Method == "item/completed" {
				var p struct{ Item server.Item }
				_ = json.Unmarshal(n.Params, &p)
				if p.Item.Type == server.ItemAgent {
					answer = p.Item.Text
				}
			}
		case <-ctx.Done():
			t.Fatal("the live answer never arrived")
		}
	}
	if answer != "attached answer" {
		t.Fatalf("answer %q", answer)
	}
	if _, err := runAgent(t, "wait", "a", "-timeout", "30s", "-session", "p1"); err != nil {
		t.Fatal(err)
	}
}

// Interrupting a turn in a worker stops it as a user interrupt: the running
// command becomes a job that survives, and the next turn goes on.
func TestWorkerTurnInterruptKeepsHostedCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the command is written for bash; Windows runs PowerShell")
	}
	marker := filepath.Join(t.TempDir(), "started")
	agentServer(t, func(n int, _ string) string {
		if n == 1 {
			return toolAnswer("echo started; touch '" + marker + "'; sleep 30")
		}
		return textAnswer("next turn")
	})
	useWorkers(t)
	t.Chdir(t.TempDir())
	enableAgents(t, "")
	if _, err := runAgent(t, "spawn", "a", "work", "-session", "root"); err != nil {
		t.Fatal(err)
	}
	st, _ := agentstate.LoadChild("root", "a")
	t.Cleanup(func() { jobs.KillAll("root"); jobs.KillAll(st.Session) })
	waitUntil(t, "the command to start", func() bool { _, err := os.Stat(marker); return err == nil })
	if out, err := runAgent(t, "interrupt", "@"+st.Session); err != nil || !strings.Contains(out, "interrupted") {
		t.Fatalf("interrupt: %q %v", out, err)
	}
	if got := st.Latest().Status; got != agentstate.Stopped {
		t.Fatalf("status %s", got)
	}
	j, err := jobs.Get(st.Session, 1)
	if err != nil || j.Status != jobs.Running || !j.QuietExit {
		t.Fatalf("the interrupted command did not survive: %+v %v", j, err)
	}
	// A stopped turn does not tell the parent.
	if evs := events.Drain("root"); len(evs) != 1 || !strings.Contains(evs[0].Text, "Turn 1 stopped") {
		t.Fatalf("parent events: %+v", evs)
	}
	// The old interrupt request must not cancel the successor.
	if _, err := runAgent(t, "task", "a", "continue", "-session", "root"); err != nil {
		t.Fatal(err)
	}
	if out, err := runAgent(t, "wait", "a", "-timeout", "30s", "-session", "root"); err != nil || !strings.Contains(out, "next turn") {
		t.Fatalf("next turn: %q %v", out, err)
	}
	if j, err := jobs.Get(st.Session, 1); err != nil || j.Status != jobs.Running {
		t.Fatalf("the command died with the next turn: %+v %v", j, err)
	}
}

// A task given while a turn runs is taken after its current step; one that
// arrives as the turn ends gets a successor turn; both in the same worker.
func TestWorkerQueuedTaskGetsASuccessorTurn(t *testing.T) {
	release := make(chan struct{})
	bodies := agentServer(t, func(n int, _ string) string {
		if n == 1 {
			<-release
		}
		return textAnswer("done " + string(rune('0'+n)))
	})
	useWorkers(t)
	t.Chdir(t.TempDir())
	enableAgents(t, "")
	if _, err := runAgent(t, "spawn", "a", "first", "-session", "p1"); err != nil {
		t.Fatal(err)
	}
	st, _ := agentstate.LoadChild("p1", "a")
	t.Cleanup(func() { jobs.KillAll("p1"); jobs.KillAll(st.Session) })
	waitUntil(t, "the turn to run", func() bool { return len(bodies()) == 1 })
	// While it runs: a message (no turn) and a task (taken after the step).
	if out, err := runAgent(t, "task", "a", "the second task", "-session", "p1"); err != nil || !strings.Contains(out, "takes the task after its current step") {
		t.Fatalf("task while running: %q %v", out, err)
	}
	close(release)
	waitUntil(t, "the successor turn", func() bool {
		cur, err := agentstate.Load(st.Session)
		return err == nil && cur.Turns >= 1 && !cur.Latest().Status.Active() && len(bodies()) >= 2
	})
	b := strings.Join(bodies(), "\n")
	if !strings.Contains(b, "the second task") || !strings.Contains(b, "NEW_TASK") {
		t.Fatalf("the task never reached the model:\n%s", b)
	}
	if cur, _ := agentstate.Load(st.Session); cur.Turns != 2 {
		t.Fatalf("the task did not become turn 2: %+v", cur)
	}
}

// Five agents' turns run at once, each in a worker of its own.
func TestWorkerTurnsRunConcurrently(t *testing.T) {
	var mu sync.Mutex
	inFlight := 0
	all := make(chan struct{})
	agentServer(t, func(int, string) string {
		mu.Lock()
		inFlight++
		if inFlight == 5 {
			close(all)
		}
		mu.Unlock()
		select {
		case <-all:
		case <-time.After(30 * time.Second):
		}
		return textAnswer("ok")
	})
	useWorkers(t)
	t.Chdir(t.TempDir())
	enableAgents(t, `,"maxConcurrent":1`)
	names := []string{"one", "two", "three", "four", "five"}
	for _, n := range names {
		if _, err := runAgent(t, "start", n, "general", "x", "-session", "p1"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { jobs.KillAll("p1") })
	select {
	case <-all:
	case <-time.After(30 * time.Second):
		t.Fatal("not all five turns ran at once")
	}
	for _, n := range names {
		if out, err := runAgent(t, "wait", n, "-timeout", "30s", "-session", "p1"); err != nil || !strings.Contains(out, "done") {
			t.Fatalf("%s: %q %v", n, out, err)
		}
	}
}

// An agent started from a shell has a worker of its own session, and its turn
// is a job of that session: no event wakes it, nothing is delivered anywhere.
func TestWorkerRootAgentFromAShell(t *testing.T) {
	agentServer(t, func(int, string) string { return textAnswer("root answer") })
	useWorkers(t)
	gitProject(t)
	enableAgents(t, "")
	st, out := spawnExternalAgent(t, "panes")
	if !strings.Contains(out, "job 1 of session "+st.Session) {
		t.Fatalf("spawn output: %q", out)
	}
	j, err := jobs.Get(st.Session, st.Job)
	if err != nil || !j.InWorker() || j.Session != st.Session || !j.Silent {
		t.Fatalf("root turn job: %+v %v", j, err)
	}
	if out, err := runAgent(t, "wait", "panes", "-timeout", "30s"); err != nil || !strings.Contains(out, "root answer") {
		t.Fatalf("wait: %q %v", out, err)
	}
	waitUntil(t, "the root's turn job to end", func() bool { j, _ := jobs.Get(st.Session, st.Job); return !j.Active() })
	if evs := events.Drain(st.Session); len(evs) != 0 {
		t.Fatalf("a root hears about its own turn: %+v", evs)
	}
}

// A worker killed mid-turn leaves a failed turn; the next task starts a new
// worker that resumes the agent's saved session.
func TestWorkerKilledMidTurnRecovers(t *testing.T) {
	release := make(chan struct{})
	var calls sync.Mutex
	n := 0
	bodies := agentServer(t, func(int, string) string {
		calls.Lock()
		n++
		first := n == 1
		calls.Unlock()
		if first {
			<-release
		}
		return textAnswer("after the crash")
	})
	useWorkers(t)
	t.Cleanup(func() { close(release) })
	t.Chdir(t.TempDir())
	enableAgents(t, "")
	if _, err := runAgent(t, "spawn", "a", "work", "-session", "p1"); err != nil {
		t.Fatal(err)
	}
	st, _ := agentstate.LoadChild("p1", "a")
	t.Cleanup(func() { jobs.KillAll("p1"); jobs.KillAll(st.Session) })
	var w daemon.Worker
	waitUntil(t, "the worker", func() bool { var ok bool; w, ok = workerOfSession(t, st.Session); return ok })
	waitUntil(t, "the model to be asked", func() bool { return len(bodies()) == 1 })
	if err := killProcess(w.PID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the lost turn to show", func() bool {
		cur, _ := agentstate.Load(st.Session)
		return cur.Latest().Status == agentstate.Failed
	})
	cur, _ := agentstate.Load(st.Session)
	if !strings.Contains(cur.Latest().Error, "lost") {
		t.Fatalf("how the turn failed: %+v", cur.Latest())
	}
	// A new task starts a new worker for the same session.
	waitUntil(t, "the daemon to forget the worker", func() bool { _, ok := workerOfSession(t, st.Session); return !ok })
	if out, err := runAgent(t, "task", "a", "again", "-session", "p1"); err != nil || !strings.Contains(out, "turn 2 started") {
		t.Fatalf("task: %q %v", out, err)
	}
	if out, err := runAgent(t, "wait", "a", "-timeout", "30s", "-session", "p1"); err != nil || !strings.Contains(out, "after the crash") {
		t.Fatalf("wait: %q %v", out, err)
	}
	if again, ok := workerOfSession(t, st.Session); !ok || again.PID == w.PID {
		t.Fatalf("worker after the crash: %+v %v", again, ok)
	}
}

// A worker running an agent turn is not retired; an idle one is, like others.
func TestWorkerOfAnAgentRetiresOnlyWhenIdle(t *testing.T) {
	release := make(chan struct{})
	agentServer(t, func(int, string) string { <-release; return textAnswer("done") })
	useWorkers(t)
	t.Setenv("ATTO_WORKER_RETENTION", "300ms")
	t.Chdir(t.TempDir())
	enableAgents(t, "")
	if _, err := runAgent(t, "spawn", "a", "work", "-session", "p1"); err != nil {
		t.Fatal(err)
	}
	st, _ := agentstate.LoadChild("p1", "a")
	t.Cleanup(func() { jobs.KillAll("p1"); jobs.KillAll(st.Session) })
	time.Sleep(1500 * time.Millisecond) // several retention periods, with no client
	if _, ok := workerOfSession(t, st.Session); !ok {
		t.Fatal("the worker of a running turn retired")
	}
	close(release)
	if _, err := runAgent(t, "wait", "a", "-timeout", "30s", "-session", "p1"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the idle worker to retire", func() bool { _, ok := workerOfSession(t, st.Session); return !ok })
	// The agent is still there and a task starts a worker again.
	if _, err := runAgent(t, "task", "a", "again", "-session", "p1"); err != nil {
		t.Fatal(err)
	}
	if _, err := runAgent(t, "wait", "a", "-timeout", "30s", "-session", "p1"); err != nil {
		t.Fatal(err)
	}
}

// Without the daemon the turn is the job process it always was.
func TestNoDaemonTurnIsAJobProcess(t *testing.T) {
	agentServer(t, func(int, string) string { return textAnswer("ok") })
	t.Setenv("ATTO_NO_DAEMON", "1")
	t.Chdir(t.TempDir())
	enableAgents(t, "")
	if _, err := runAgent(t, "spawn", "a", "work", "-session", "p1"); err != nil {
		t.Fatal(err)
	}
	st, _ := agentstate.LoadChild("p1", "a")
	t.Cleanup(func() { jobs.KillAll("p1"); jobs.KillAll(st.Session) })
	j, err := jobs.Get("p1", st.Job)
	if err != nil || j.InWorker() || j.Kind() != "agent" || j.Label() != "agent a" || len(j.Args) < 3 || j.Args[1] != "_agent-turn" || j.Args[2] != st.Session {
		t.Fatalf("job: %+v %v", j, err)
	}
	if _, err := runAgent(t, "wait", "a", "-timeout", "30s", "-session", "p1"); err != nil {
		t.Fatal(err)
	}
	if ws, _ := daemon.Workers(); len(ws) != 0 {
		t.Fatalf("a worker was started: %+v", ws)
	}
}

// An idle agent is not woken by what arrives in its inbox: as when an idle
// agent had no process, events wait for its next turn.
func TestIdleAgentWorkerIsNotWokenByEvents(t *testing.T) {
	bodies := agentServer(t, func(int, string) string { return textAnswer("ok") })
	useWorkers(t)
	t.Chdir(t.TempDir())
	enableAgents(t, "")
	if _, err := runAgent(t, "spawn", "a", "work", "-session", "p1"); err != nil {
		t.Fatal(err)
	}
	st, _ := agentstate.LoadChild("p1", "a")
	t.Cleanup(func() { jobs.KillAll("p1"); jobs.KillAll(st.Session) })
	if _, err := runAgent(t, "wait", "a", "-timeout", "30s", "-session", "p1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := workerOfSession(t, st.Session); !ok {
		t.Fatal("no worker to be woken")
	}
	for _, e := range []events.Event{
		{Source: "job", Text: "Background job 9 exited with code 0"},
		{Source: "agent", Text: agentstate.Envelope(agentstate.FinalAnswer, "/root/a/kid", "/root/a", "kid is done")},
	} {
		if err := events.Push(st.Session, e); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(1500 * time.Millisecond) // several inbox polls
	if n := len(bodies()); n != 1 {
		t.Fatalf("an idle agent started a turn by itself: %d requests", n)
	}
	if cur, _ := agentstate.Load(st.Session); cur.Turns != 1 {
		t.Fatalf("turns %d", cur.Turns)
	}
	// They are delivered with its next turn.
	if _, err := runAgent(t, "task", "a", "next", "-session", "p1"); err != nil {
		t.Fatal(err)
	}
	if _, err := runAgent(t, "wait", "a", "-timeout", "30s", "-session", "p1"); err != nil {
		t.Fatal(err)
	}
	b := bodies()
	if len(b) != 2 || !strings.Contains(b[1], "next") || !strings.Contains(b[1], "Background job 9 exited") || !strings.Contains(b[1], "kid is done") {
		t.Fatalf("the waiting events were not delivered with the next turn: %v", b[len(b)-1])
	}
}

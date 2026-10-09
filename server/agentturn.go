package server

import (
	"context"
	"errors"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/agentturn"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/provider"
)

// An agent's turns run in the worker of the agent's own session, the same
// runtime as every other session: atto agent asks it for a turn (agent/turn)
// and clients attach to the session like any other. A managed agent thread
// differs in four ways:
//
//   - A turn is a record in the agent's state (its number, prompt and the job
//     that stands for it in atto job list), not a run: it is one run, and
//     more while waking work arrives, until the thread goes idle.
//   - Nothing starts a run by itself while the agent is idle. Events wait in
//     the inbox for the next turn, as they did when an idle agent had no
//     process; a task or a message does not wake it, atto agent task does.
//   - It ends as a turn does: usage and status recorded, the answer pushed to
//     the parent (a root has none), the jobs of the turn stopped, a
//     successor started when work arrived as it ended.
//   - The worker is not retired while a turn runs, waits or is being recorded.

// managedAgent is the runtime's state for an agent session.
type managedAgent struct {
	t *thread
	// hold: no run is going, so the inbox is left alone. Read off the lane by
	// the inbox poll, written on it.
	hold atomic.Bool

	// Lane only.
	pending   []int // accepted turns not started
	active    int   // the turn being run (0: none)
	rec       agentstate.Turn
	usage     agentturn.Result
	finishing bool                       // the turn is being recorded
	watch     map[int]context.CancelFunc // interrupt watchers, by turn
	wg        sync.WaitGroup             // recording in progress
}

func newManagedAgent(t *thread) *managedAgent {
	m := &managedAgent{t: t, watch: map[int]context.CancelFunc{}}
	m.hold.Store(true)
	return m
}

// busy reports whether the agent has a turn waiting, running or being recorded.
func (m *managedAgent) busy() bool { return m.active != 0 || m.finishing || len(m.pending) > 0 }

// wait lets turns being recorded finish.
func (m *managedAgent) wait() { m.wg.Wait() }

// accept is agent/turn: the turn the agent's record names is to be run.
func (m *managedAgent) accept(turn int) (any, error) {
	t := m.t
	st, err := agentstate.Load(t.id)
	if err != nil {
		return nil, invalid("%v", err)
	}
	switch {
	case turn <= 0 || turn != st.Turns:
		return nil, invalid("turn %d is not the agent's latest (turn %d)", turn, st.Turns)
	case !st.Live():
		return nil, failure(ReasonBusy, "agent %s is %s", st.Path, st.Lifecycle)
	}
	if m.active != turn && !slices.Contains(m.pending, turn) {
		m.pending = append(m.pending, turn)
		m.watchInterrupt(turn)
		m.pump()
	}
	return map[string]any{"turn": turn, "status": "accepted"}, nil
}

// pump starts the next accepted turn when nothing else runs.
func (m *managedAgent) pump() {
	t := m.t
	for m.active == 0 && !m.finishing && len(m.pending) > 0 && !t.turns.Busy && !t.closing {
		turn := m.pending[0]
		m.pending = m.pending[1:]
		st, err := agentstate.Load(t.id)
		if err != nil || st.Turns != turn {
			m.stopWatching(turn) // stale: a newer turn replaced it
			continue
		}
		queued := time.Now()
		if prev, ok := agentstate.LoadTurn(t.id); ok && prev.N == turn && !prev.Queued.IsZero() {
			queued = prev.Queued
		}
		if agentstate.Interrupted(t.id, turn) {
			m.stopQueued(st, turn, queued)
			continue
		}
		m.active, m.usage = turn, agentturn.Result{}
		m.rec = agentstate.Turn{N: turn, Status: agentstate.Running, Queued: queued, Started: time.Now()}
		_ = agentstate.SaveTurn(t.id, m.rec)
		// What came to the agent's inbox before the turn goes with its prompt.
		text := st.Prompt
		reload, evs := events.SplitReload(core.Poll(t.id))
		if len(evs) > 0 {
			text += "\n\n" + events.Format(evs)
		}
		t.feed(transcript.Input{Text: text})
		t.recordSettings()
		t.start("turn", "Thinking", func(ctx context.Context, emit func(any)) error {
			return t.turns.Run(ctx, t.agent, core.TurnRequest{Text: text}, emit)
		})
		if reload {
			t.requestReload(true)
		}
		if !t.turns.Busy { // the run could not begin
			m.active = 0
			m.record(st, turn, agentturn.Result{Error: "the agent's session could not start the turn"})
		}
		return
	}
}

// step counts a model call of the turn.
func (m *managedAgent) step(u provider.Usage) {
	if m.active == 0 {
		return
	}
	m.usage.Prompt += u.PromptTokens
	m.usage.Cached += u.CachedTokens
	m.usage.Output += u.CompletionTokens
	m.usage.Cost += u.Cost
	m.usage.Steps++
}

// runEnded follows every run of the thread's end (after what waited for it
// has been settled): the inbox is held again once nothing runs, and the turn
// ends when no run goes on.
func (m *managedAgent) runEnded(err error) {
	t := m.t
	m.hold.Store(!t.turns.Busy)
	if m.active == 0 || t.turns.Busy {
		if m.active == 0 {
			m.pump()
		}
		return
	}
	turn := m.active
	st, lerr := agentstate.Load(t.id)
	if lerr != nil {
		st = agentstate.State{Session: t.id, Turns: turn}
	}
	res := m.usage
	switch {
	case errors.Is(err, context.Canceled):
		res.Stopped = true
	case err != nil:
		res.Error = err.Error()
	}
	m.active = 0
	m.record(st, turn, res)
}

// record ends turn on a goroutine of its own: it writes files and may wait
// for a lock, and the lane waits for nobody.
func (m *managedAgent) record(st agentstate.State, turn int, res agentturn.Result) {
	t := m.t
	rec := m.rec
	if rec.N != turn {
		rec = agentstate.Turn{N: turn, Queued: time.Now(), Started: time.Now()}
	}
	m.finishing = true
	m.wg.Go(func() {
		// The jobs of the turn end with it; the agent's own agents, and commands
		// a user interrupt detached, go on.
		core.LeaveKeepingAgents(t.id)
		if err := agentturn.Finish(st, rec, res, m.startSuccessor); err != nil {
			// The record could not be written: leave a failed turn behind if we can.
			rec.Ended, rec.Status, rec.Error = time.Now(), agentstate.Failed, err.Error()
			_ = agentstate.SaveTurn(t.id, rec)
		}
		owner, job := st.JobRef()
		if job > 0 {
			_ = jobs.EndWorkerTurn(owner, job, res.Stopped)
		}
		t.do(func() {
			m.finishing = false
			m.stopWatching(turn)
			m.pump()
			t.updated()
			t.maybeRetire()
		})
	})
}

// startSuccessor starts another turn of the agent in this worker, for work
// that arrived as a turn ended.
func (m *managedAgent) startSuccessor(st *agentstate.State, text string) error {
	release, err := agentstate.StartWork(st.Session)
	if err != nil {
		return err
	}
	defer release()
	cur, err := agentturn.Prepare(*st, text)
	if err != nil {
		return err
	}
	path, content := agentstate.InterruptRequestPath(cur.Session, cur.Turns)
	j, err := jobs.StartWorkerTurn(cur.JobOwner, cur.Cwd, "agent "+cur.Name, os.Getpid(), jobs.Control{File: path, Content: content}, cur.IsRoot())
	if err != nil {
		return err
	}
	cur.Job = j.ID
	if err := agentstate.Save(cur); err != nil {
		return err
	}
	*st = cur
	m.t.do(func() {
		if !slices.Contains(m.pending, cur.Turns) && m.active != cur.Turns {
			m.pending = append(m.pending, cur.Turns)
			m.watchInterrupt(cur.Turns)
		}
	})
	return nil
}

// watchInterrupt follows atto agent interrupt for turn: the request is a
// file, so it works for a turn that waits and for one that runs alike.
func (m *managedAgent) watchInterrupt(turn int) {
	t := m.t
	ctx, cancel := context.WithCancel(context.Background())
	m.watch[turn] = cancel
	go func() {
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			if agentstate.Interrupted(t.id, turn) {
				t.do(func() { m.interrupt(turn) })
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
}

func (m *managedAgent) stopWatching(turn int) {
	if cancel := m.watch[turn]; cancel != nil {
		cancel()
		delete(m.watch, turn)
	}
}

// interrupt stops turn: a running one as a user interrupt (a hosted command
// it runs becomes a job), a waiting one before it starts.
func (m *managedAgent) interrupt(turn int) {
	t := m.t
	switch {
	case m.active == turn:
		if t.turns.Busy {
			t.turns.Interrupt(true)
		}
	case slices.Contains(m.pending, turn):
		m.pending = slices.DeleteFunc(m.pending, func(n int) bool { return n == turn })
		if st, err := agentstate.Load(t.id); err == nil && st.Turns == turn {
			queued := time.Now()
			if prev, ok := agentstate.LoadTurn(t.id); ok && prev.N == turn && !prev.Queued.IsZero() {
				queued = prev.Queued
			}
			m.stopQueued(st, turn, queued)
		}
	}
}

// stopQueued ends a turn that never started: stopped, with no answer to deliver.
func (m *managedAgent) stopQueued(st agentstate.State, turn int, queued time.Time) {
	_ = agentstate.SaveTurn(st.Session, agentstate.Turn{N: turn, Status: agentstate.Stopped, Queued: queued, Ended: time.Now()})
	owner, job := st.JobRef()
	m.stopWatching(turn)
	if job > 0 {
		m.wg.Go(func() {
			_ = jobs.EndWorkerTurn(owner, job, true)
			m.t.do(func() { m.t.maybeRetire() })
		})
	}
}

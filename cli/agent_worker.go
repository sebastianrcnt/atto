package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/daemon"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/session"
)

// With the daemon, an agent's turns run in the worker of the agent's own
// session, the runtime every other session has: atto agent is a client that
// finds or starts that worker and asks it for the turn (agent/turn). Without
// the daemon (ATTO_NO_DAEMON=1, "daemon": false), or when the worker
// cannot be had (another process holds the session's writer lease, say a
// turn started before an upgrade that is still finishing), a turn is a job
// process, atto _agent-turn, as it always was.

// workerCallWait bounds the calls that ask a worker for a turn.
const workerCallWait = 15 * time.Second

// startTurnInWorker runs the prepared turn of st in its session's worker.
// handled is false when the turn must run as a job process instead.
func startTurnInWorker(st *agentstate.State) (handled bool, err error) {
	if !daemon.Usable() {
		return false, nil
	}
	w, readOnly, err := daemon.StartWorker(st.Session, st.Cwd, nil)
	if err != nil || readOnly != "" || w.PID == 0 {
		return false, nil
	}
	path, content := agentstate.InterruptRequestPath(st.Session, st.Turns)
	j, err := jobs.StartWorkerTurn(st.JobOwner, st.Cwd, "agent "+st.Name, w.PID, jobs.Control{File: path, Content: content}, st.IsRoot())
	if err != nil {
		return true, fmt.Errorf("starting agent %s: %w", st.Name, err)
	}
	st.Job = j.ID
	if err := agentstate.Save(*st); err != nil {
		_ = jobs.EndWorkerTurn(st.JobOwner, j.ID, true)
		return true, err
	}
	if err := askWorkerForTurn(w, st.Session, st.Turns); err != nil {
		_ = jobs.EndWorkerTurn(st.JobOwner, j.ID, true)
		return true, fmt.Errorf("starting agent %s: %w", st.Name, err)
	}
	return true, nil
}

// askWorkerForTurn sends agent/turn to the worker w of session id.
func askWorkerForTurn(w daemon.Worker, id string, turn int) error {
	nc, err := daemon.DialWorker(w)
	if err != nil {
		return err
	}
	c := server.NewClient(nc)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), workerCallWait)
	defer cancel()
	if err := c.Call(ctx, "initialize", map[string]any{"protocolVersions": []int{server.ProtocolVersion}, "clientInfo": map[string]string{"name": "atto-agent"}}, nil); err != nil {
		return err
	}
	return c.Call(ctx, "agent/turn", map[string]any{"threadId": id, "turn": turn}, nil)
}

// stopWorkerOf closes the worker of an agent session that is being closed,
// so that the session can be archived: an idle worker holds its writer lease
// for a while. It waits for the lease to go.
func stopWorkerOf(id string) {
	if !daemon.Usable() {
		return
	}
	ws, err := daemon.Workers()
	if err != nil {
		return
	}
	found := false
	for _, w := range ws {
		if w.Session == id {
			found = true
		}
	}
	if !found {
		return
	}
	_ = daemon.Kill(id)
	path, err := session.Find(id)
	if err != nil {
		return
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if _, busy := session.LockedBy(path); !busy {
			return
		}
	}
}

// forceStopWorkerTurn ends a turn its worker did not stop when asked, by
// closing the worker (and with it every turn of that agent).
func forceStopWorkerTurn(st agentstate.State) error {
	if err := daemon.Kill(st.Session); err != nil && !errors.Is(err, daemon.ErrUnavailable) {
		return err
	}
	return nil
}

package server

import (
	"context"
	"os"
	"time"

	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/jobs"
)

// retirable reports whether the thread has nothing going on and no
// client: it may close.
func (t *thread) retirable() bool {
	if t.mgd != nil && t.mgd.busy() {
		return false
	}
	if t.closing || t.login != nil || len(t.attached) > 0 || t.turns.Busy || t.shell != nil || len(t.turns.Queued) > 0 || len(t.turns.PendingEvents) > 0 || t.turns.SendNow != nil {
		return false
	}
	if t.goal.Active() && !t.goal.Held() {
		return false
	}
	if t.prompt != nil || len(t.prompts) > 0 || t.retryTimer != nil || t.jobCount > 0 || t.timerCount > 0 || jobs.ActiveCount(t.id) > 0 || len(events.Timers(t.id)) > 0 {
		return false
	}
	return true
}

// upgradeBlocker is why the thread cannot be closed for its worker to be
// replaced by one of another build ("" when it can): anything running or
// waiting, held only in memory, or an attached client that would not
// follow the session to the new worker. On the lane.
func (t *thread) upgradeBlocker() string {
	switch {
	case t.closing:
		return "closing"
	case t.sess == nil || !saved(t.sess.Path):
		return "unsaved" // a new session lives in memory until its first entry
	case t.mgd != nil && t.mgd.busy():
		return "agent turn"
	case t.turns.Busy || t.shell != nil || len(t.pendingShell) > 0 || t.summary != nil || t.pendingTree != "" || t.handoff.pending:
		return "busy"
	case len(t.turns.Queued) > 0 || len(t.turns.PendingEvents) > 0 || t.turns.SendNow != nil || len(t.steers) > 0:
		return "pending input"
	case t.goal.Active():
		return "goal"
	case t.prompt != nil || len(t.prompts) > 0 || len(t.gates) > 0 || t.login != nil:
		return "prompt"
	case t.retryTimer != nil:
		return "retry"
	case t.jobCount > 0 || t.timerCount > 0 || jobs.ActiveCount(t.id) > 0 || len(events.Timers(t.id)) > 0:
		return "jobs"
	}
	for c := range t.attached {
		if !t.s.reattaches(c) {
			return "client"
		}
	}
	return ""
}

func saved(path string) bool { _, err := os.Stat(path); return err == nil }

// retireResult is worker/retire's result.
type retireResult struct {
	Retired bool   `json:"retired"`
	Reason  string `json:"reason,omitempty"` // why not
}

// retireForUpgrade is worker/retire: the thread closes now if it is idle
// (upgradeBlocker), ending nothing (no SessionEnd, jobs and goal kept), so
// that a worker of the current build can open the session again; its
// clients get thread/closed with reason "upgrade". The check and the
// close are one step on the lane: input arriving after it is refused.
func (s *Server) retireForUpgrade(t *thread) retireResult {
	why := ""
	_ = t.call(func() error { why = t.upgradeBlocker(); return nil })
	if why != "" {
		return retireResult{Reason: why}
	}
	if !s.closeThread(t, closeMode{reason: "upgrade", upgrade: true}).Closed {
		return retireResult{Reason: "busy"}
	}
	return retireResult{Retired: true}
}

// maybeRetire closes the thread once it has been retirable for the
// retention period.
func (t *thread) maybeRetire() {
	if !t.s.Retire || !t.retirable() {
		t.cancelRetire()
		return
	}
	if t.retireTimer != nil {
		return
	}
	var tm *time.Timer
	tm = time.AfterFunc(t.s.Retention, func() {
		t.do(func() {
			if t.retireTimer != tm {
				return
			}
			t.retireTimer = nil
			if t.retirable() {
				go t.s.closeThread(t, closeMode{reason: t.leaveReasonOr("other"), retire: true})
			}
		})
	})
	t.retireTimer = tm
}

func (t *thread) cancelRetire() {
	if t.retireTimer != nil {
		t.retireTimer.Stop()
		t.retireTimer = nil
	}
}

func (t *thread) leaveReasonOr(def string) string {
	if t.leaveReason != "" {
		return t.leaveReason
	}
	return def
}

// closeMode says how a thread closes.
type closeMode struct {
	reason  string // for SessionEnd
	retire  bool   // recheck idle/unattended status on the lane
	handoff bool   // a background run took the session over: end nothing
	upgrade bool   // a worker of another build takes over: end nothing, recheck upgradeBlocker on the lane
}

var closeHandoff = closeMode{handoff: true}

// closeThread ends thread t: its run is interrupted and awaited, its
// prompt closed, SessionEnd hooks and extensions run, its background jobs
// stop (unless handed off), MCP servers end, and the session file and
// lease are let go. Clients are told (thread/closed). Safe to call twice.
func (s *Server) closeThread(t *thread, m closeMode) detachResult {
	var done, shellDone chan struct{}
	if t.call(func() error {
		if t.closing || m.retire && !t.retirable() || m.upgrade && t.upgradeBlocker() != "" {
			return errThreadClosed
		}
		t.closing = true
		if t.elements != nil {
			t.elements.Stop()
		}
		t.cancelRetire()
		t.cancelGoalRetry()
		t.cancelPrompt()
		if t.login != nil {
			t.login.cancel()
			t.login = nil
		}
		if t.shell != nil {
			shellDone = t.shell.done
		}
		t.dropShell()
		if t.turns.Cancel != nil {
			t.turns.Cancel(nil)
		}
		done = t.runDone
		return nil
	}) != nil {
		return detachResult{}
	}
	if done != nil {
		<-done
	}
	if shellDone != nil {
		<-shellDone
	}
	if t.mgd != nil {
		t.mgd.wait() // a turn being recorded finishes first
	}
	_ = t.call(func() error { return nil }) // the run's end reaches the lane first
	t.inboxOff.Store(true)
	var res detachResult
	if !m.handoff && !m.upgrade {
		if t.sess.ReadOnly() == "" && t.readOnly == "" {
			switch {
			case t.mgd != nil && m.retire:
				res.StoppedJobs = core.LeaveKeepingAgents(t.id) // idle: the agents it started go on
			case t.mgd != nil || t.sess.IsAgent():
				res.StoppedJobs = core.LeaveAgent(t.id)
			default:
				res.StoppedJobs = core.Leave(t.id)
			}
		}
		if t.hooks != nil {
			res.Notices = t.hooks.SessionEnd(context.Background(), m.reason)
		}
		if t.ext != nil {
			t.ext.SessionEnd(m.reason)
		}
	}
	if t.ext != nil {
		t.ext.Close()
	}
	if t.mcp != nil {
		_ = t.mcp.Close()
	}
	// Last: SessionEnd hooks and extensions may still write to the session.
	_ = t.call(func() error {
		t.sess.Close()
		if t.release != nil {
			t.release()
			t.release = nil
		}
		t.publish("thread/closed", map[string]any{"reason": m.reason, "handoff": m.handoff})
		return nil
	})
	t.stopLane()
	s.mu.Lock()
	if s.threads[t.id] == t {
		delete(s.threads, t.id)
	}
	s.mu.Unlock()
	res.Closed = true
	if s.OnThreadClosed != nil {
		s.OnThreadClosed(t.id)
	}
	return res
}

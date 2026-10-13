package server

import (
	"time"
)

// attach is thread/attach: client follows the thread from its snapshot.
func (t *thread) attach(client string) (ThreadInfo, error) {
	var info ThreadInfo
	err := t.call(func() error {
		t.attachClient(client)
		if len(t.items) == 0 {
			if err := t.loadDisplay(); err != nil {
				return err
			}
		}
		info = t.snapshot()
		loaded := t.loaded
		info.Context = &loaded
		return nil
	})
	return info, err
}

func (t *thread) attachClient(client string) {
	if client == "" {
		return
	}
	t.attached[client] = true
	t.lastActive = time.Now()
	t.cancelRetire()
}

// detachResult is thread/detach's and thread/close's result.
type detachResult struct {
	Closed      bool     `json:"closed"`
	StoppedJobs int      `json:"stoppedJobs,omitempty"`
	Notices     []string `json:"notices,omitempty"`
}

// detach is thread/detach: client stops following the thread, which goes
// on. With retention 0, a thread left idle closes now (reason: what the
// client did: clear, resume or exit).
func (s *Server) detach(t *thread, client, reason string) detachResult {
	now := false
	_ = t.call(func() error {
		delete(t.attached, client)
		if t.elements != nil {
			t.elements.DetachClient(client)
		}
		if len(t.attached) == 0 {
			t.dropDisplay()
		}
		delete(t.gates, client)
		if reason != "" {
			t.leaveReason = reason
		}
		now = s.Retire && s.Retention == 0 && t.retirable()
		if !now {
			t.maybeRetire()
		}
		return nil
	})
	if !now {
		return detachResult{}
	}
	return s.closeThread(t, closeMode{reason: t.leaveReasonOr("other"), retire: true})
}

// clientGone releases what a detached client held: its gates and its
// attachments. Nothing it started stops.
func (s *Server) clientGone(id string) {
	if s.Workers != nil {
		s.detachRoutes(id, false)
	}
	s.mu.Lock()
	threads := make([]*thread, 0, len(s.threads))
	for _, t := range s.threads {
		threads = append(threads, t)
	}
	s.mu.Unlock()
	for _, t := range threads {
		t.do(func() {
			if !t.attached[id] && t.gates[id] == 0 {
				return
			}
			delete(t.attached, id)
			if t.elements != nil {
				t.elements.DetachClient(id)
			}
			if len(t.attached) == 0 {
				t.dropDisplay()
			}
			delete(t.gates, id)
			t.withdrawClientPrompt(id, "")
			t.deliverEvents()
			t.maybeSendNextQueued()
			t.maybeRetire()
		})
	}
}

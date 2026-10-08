package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/jobs"
)

func TestInterruptAndShutdownCauses(t *testing.T) {
	for _, user := range []bool{true, false} {
		t.Run(map[bool]string{true: "interrupt", false: "shutdown"}[user], func(t *testing.T) {
			work := setup(t)
			s := New("test", work)
			defer s.Close()
			res, err := s.startThread(threadParams{})
			if err != nil {
				t.Fatal(err)
			}
			info := res.(ThreadInfo)
			th, err := s.thread(info.ID)
			if err != nil {
				t.Fatal(err)
			}
			causes := make(chan error, 1)
			if _, err := s.begin(th, func(ctx context.Context, emit func(any)) error {
				<-ctx.Done()
				causes <- context.Cause(ctx)
				return ctx.Err()
			}); err != nil {
				t.Fatal(err)
			}
			if user {
				params, _ := json.Marshal(map[string]any{"threadId": info.ID})
				if _, err := s.call(context.Background(), "turn/interrupt", params); err != nil {
					t.Fatal(err)
				}
			} else {
				s.Close()
			}
			select {
			case cause := <-causes:
				if errors.Is(cause, agent.ErrUserInterrupt) != user {
					t.Fatalf("cause %v, user %v", cause, user)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("turn did not cancel")
			}
		})
	}
}

func TestShutdownStopsLateInterruptDetach(t *testing.T) {
	work := setup(t)
	s := New("test", work)
	defer s.Close()
	res, err := s.startThread(threadParams{})
	if err != nil {
		t.Fatal(err)
	}
	th, _ := s.thread(res.(ThreadInfo).ID)
	release := make(chan struct{})
	if _, err := s.begin(th, func(ctx context.Context, emit func(any)) error {
		<-ctx.Done()
		<-release // a host still registering the interrupted command
		dir := filepath.Join(jobs.Root(th.id), "1")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		data, _ := json.Marshal(jobs.Job{ID: 1, Session: th.id, Status: jobs.Starting, Started: time.Now(), QuietExit: true})
		if err := os.WriteFile(filepath.Join(dir, "job.json"), data, 0o644); err != nil {
			return err
		}
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	th.mu.Lock()
	th.turns.Cancel(agent.ErrUserInterrupt)
	th.mu.Unlock()
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		th.mu.Lock()
		closing := th.closing
		th.mu.Unlock()
		if closing {
			break
		}
		if time.Now().After(deadline) {
			close(release)
			t.Fatal("shutdown did not cancel the turn")
		}
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown did not finish")
	}
	if j, err := jobs.Get(th.id, 1); err != nil || j.Status != jobs.Killed {
		t.Fatalf("late detached job survived shutdown: %+v %v", j, err)
	}
}

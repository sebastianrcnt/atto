package events

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func claimTimer(t *testing.T, timer Timer) string {
	t.Helper()
	claim := filepath.Join(timerDir("s"), "."+timer.ID+".claim")
	if err := os.Rename(filepath.Join(timerDir("s"), timer.ID+".json"), claim); err != nil {
		t.Fatal(err)
	}
	return claim
}

func TestCancelClaimedTimer(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	timer, err := AddRecurringTimer("s", time.Now(), time.Minute, 0, time.Time{}, "check")
	if err != nil {
		t.Fatal(err)
	}
	claim := claimTimer(t, timer)
	if err := CancelTimer("s", timer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(claim); !os.IsNotExist(err) {
		t.Fatalf("claim survived: %v", err)
	}
	if n := FireDue("s", timer.Due); n != 0 || len(Timers("s")) != 0 {
		t.Fatal("cancelled claim returned")
	}
}

func TestRecoverOrphanTimerClaim(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	timer, err := AddRecurringTimer("s", time.Now(), time.Minute, 0, time.Time{}, "check")
	if err != nil {
		t.Fatal(err)
	}
	claimTimer(t, timer)
	if pending := Timers("s"); len(pending) != 1 || pending[0].ID != timer.ID {
		t.Fatalf("orphan lost: %v", pending)
	}
	if n := FireDue("s", timer.Due); n != 1 {
		t.Fatalf("recovered timer fired %d times", n)
	}
	pending := Timers("s")
	if len(pending) != 1 || !pending[0].Due.Equal(timer.Due.Add(time.Minute)) {
		t.Fatalf("recovered schedule: %v", pending)
	}
	// A crash after writing the replacement must not restore the old due time.
	if err := os.WriteFile(filepath.Join(timerDir("s"), "."+timer.ID+".claim"), []byte("old claim"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n := FireDue("s", timer.Due); n != 0 {
		t.Fatalf("old claim refired: %d", n)
	}
}

func TestCancelConcurrentWithRecurringFire(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	for range 16 {
		timer, err := AddRecurringTimer("s", time.Now(), time.Minute, 0, time.Time{}, "check")
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Go(func() { FireDue("s", timer.Due) })
		wg.Go(func() {
			if err := CancelTimer("s", timer.ID); err != nil {
				t.Error(err)
			}
		})
		wg.Wait()
		if len(Timers("s")) != 0 {
			t.Fatal("cancelled recurring timer returned")
		}
	}
}

package jobs

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestReserveConcurrentAtLimit(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	const session = "reserve-limit"
	for range MaxRunning - 1 {
		if _, _, err := reserve(session); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, 32)
	start := make(chan struct{})
	for range cap(results) {
		wg.Go(func() {
			<-start
			_, _, err := reserve(session)
			results <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 || ActiveCount(session) != MaxRunning {
		t.Fatalf("%d successful reservations, %d active jobs", successes, ActiveCount(session))
	}
}

func TestReserveReclaimsStaleLock(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	const session = "reserve-stale"
	if err := os.MkdirAll(Root(session), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(Root(session), ".reserve.lock")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-2 * reservationLockTimeout)
	if err := os.Chtimes(path, stale, stale); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reserve(session); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("lock left: %v", err)
	}
}

func TestRemoveReservationLockKeepsReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".reserve.lock")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	checked, err := reservationLockInfo(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeReservationLock(path, checked); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "replacement" {
		t.Fatalf("replacement: %q %v", b, err)
	}
}

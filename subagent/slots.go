package subagent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Slots cap how many subagent turns of one session run at once. Each is a
// file a running turn holds locked (flock, LockFileEx); the lock goes
// with the process, however it ends, so a stopped or crashed turn frees
// its slot.

// slotPoll is how often a queued turn tries the slots again.
var slotPoll = 500 * time.Millisecond

// TryAcquire takes a free slot of the limit session parent has, without
// waiting. ok is false when all are taken.
func TryAcquire(parent string, limit int) (release func(), ok bool, err error) {
	dir := filepath.Join(Dir(parent), "slots")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, false, err
	}
	for i := range max(limit, 1) {
		f, err := os.OpenFile(filepath.Join(dir, fmt.Sprint(i)), os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			return nil, false, err
		}
		if tryLock(f) {
			return func() { unlock(f); f.Close() }, true, nil
		}
		f.Close()
	}
	return nil, false, nil
}

// Acquire takes a slot, waiting in line for one to free up. It reads the
// limit again each time, so a changed setting applies to waiting turns.
func Acquire(ctx context.Context, parent string, limit func() int) (release func(), err error) {
	for {
		release, ok, err := TryAcquire(parent, limit())
		if err != nil || ok {
			return release, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(slotPoll):
		}
	}
}

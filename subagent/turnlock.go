package subagent

import (
	"os"
	"path/filepath"
	"time"
)

// LockTurn serializes accepting tasks with retiring an agent's consumer.
// The file stays: unlinking a flock file would let waiters lock old inodes.
func LockTurn(parent, name string) (func(), error) {
	if err := ValidName(name); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(Dir(parent), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(Dir(parent), name+".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	for !tryLock(f) {
		time.Sleep(10 * time.Millisecond)
	}
	return func() { unlock(f); f.Close() }, nil
}

package core

import (
	"math"
	"os"
	"runtime/debug"
	"sync"
)

// DefaultMemoryLimit is a soft budget, not a cap: Go can exceed it when a model
// context requires more. It bounds one-off JSON/tree/replay allocation peaks in
// otherwise small workers; GOMEMLIMIT and embedded applications may override it.
const DefaultMemoryLimit int64 = 32 << 20

var memoryBudgetOnce sync.Once

func ConfigureMemoryBudget() {
	memoryBudgetOnce.Do(func() {
		if os.Getenv("GOMEMLIMIT") != "" || debug.SetMemoryLimit(-1) != math.MaxInt64 {
			return
		}
		debug.SetMemoryLimit(DefaultMemoryLimit)
	})
}

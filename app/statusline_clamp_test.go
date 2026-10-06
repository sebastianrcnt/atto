package app

import (
	"math"
	"testing"
	"time"
)

func TestStatusRefreshDurationDoesNotOverflow(t *testing.T) {
	for _, seconds := range []int{1, 60, math.MaxInt} {
		d := statusRefreshDuration(seconds)
		if d <= 0 {
			t.Fatalf("%d seconds overflowed: %s", seconds, d)
		}
		ticker := time.NewTicker(d)
		ticker.Stop()
	}
}

package tui

import (
	"testing"
	"time"
)

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		5 * time.Millisecond:          "5ms",
		800 * time.Millisecond:        "0.8s",
		3 * time.Second:               "3.0s",
		2900 * time.Millisecond:       "2.9s",
		59900 * time.Millisecond:      "59.9s",
		time.Minute:                   "1m 00s",
		2*time.Minute + 5*time.Second: "2m 05s",
		time.Hour + 5*time.Minute:     "1h 05m",
	} {
		if got := FormatDuration(d); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
}

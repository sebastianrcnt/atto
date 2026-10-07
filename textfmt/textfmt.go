// Package textfmt formats durations, token counts and one-line excerpts
// for atto's command-line output.
package textfmt

import (
	"fmt"
	"strings"
	"time"
)

// Duration is a short duration that keeps its width while it counts: 0.8s,
// 12.0s, 39.9s under a minute (always one decimal, so the number doesn't
// jump between "2.9s" and "3s"), then 2m 05s, then 1h 05m. Below 0.1s,
// where one decimal would read 0.0s, it's milliseconds: 5ms.
func Duration(d time.Duration) string {
	if d < 100*time.Millisecond {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	if d < time.Hour {
		m := int(d.Minutes())
		return fmt.Sprintf("%dm %02ds", m, int(d.Seconds())-60*m)
	}
	h := int(d.Hours())
	return fmt.Sprintf("%dh %02dm", h, int(d.Minutes())-60*h)
}

// Tokens is a token count: 950, 12.5k, 1.2M.
func Tokens(n int) string {
	switch {
	case n >= 1000000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

// FirstLine is s up to its first newline, with " …" if it went on.
func FirstLine(s string) string {
	if before, _, ok := strings.Cut(s, "\n"); ok {
		return before + " …"
	}
	return s
}

// Truncate cuts s to at most n runes, ending in tail when it cuts.
func Truncate(s string, n int, tail string) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	t := []rune(tail)
	if len(t) > n {
		t = nil
	}
	return string(r[:n-len(t)]) + string(t)
}

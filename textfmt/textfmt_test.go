package textfmt

import (
	"testing"
	"time"
)

func TestDuration(t *testing.T) {
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
		if got := Duration(d); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
}

func TestTokensFirstLineTruncate(t *testing.T) {
	if got := Tokens(950) + " " + Tokens(12500) + " " + Tokens(1200000); got != "950 12.5k 1.2M" {
		t.Errorf("tokens %q", got)
	}
	if got := FirstLine("a\nb"); got != "a …" {
		t.Errorf("first line %q", got)
	}
	if got := Truncate("héllo world", 6, "…"); got != "héllo…" {
		t.Errorf("truncate %q", got)
	}
	if got := Truncate("short", 6, "…"); got != "short" {
		t.Errorf("truncate %q", got)
	}
}

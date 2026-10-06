package session

import (
	"strings"
	"testing"
	"time"
)

func TestLinuxStartTime(t *testing.T) {
	stat := "123 (name with ) spaces) S " + strings.Repeat("0 ", 18) + "12345 0"
	start, ok := linuxStartTime(stat, "cpu 123\nbtime 1000000\n")
	want := time.Unix(1000123, 450000000)
	if !ok || !start.Equal(want) {
		t.Fatalf("start: %v %v, want %v", start, ok, want)
	}
	for _, stat := range []string{"", "123 (bad) S", "123 (bad) S " + strings.Repeat("0 ", 18) + "bad"} {
		if _, ok := linuxStartTime(stat, "btime 1000000"); ok {
			t.Fatalf("accepted %q", stat)
		}
	}
	if _, ok := linuxStartTime(stat, "cpu 123"); ok {
		t.Fatal("accepted missing boot time")
	}
}

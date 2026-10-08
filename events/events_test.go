package events

import (
	"fmt"

	"github.com/sebastianrcnt/atto/fsutil"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestInboxAndTimers(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	Push("s", Event{Source: "job", Text: "first"})
	Push("s", Event{Source: "job", Text: "second"})
	if !Pending("s") {
		t.Fatal("pending")
	}
	evs := Drain("s")
	if len(evs) != 2 || evs[0].Text != "first" || Pending("s") {
		t.Fatalf("drain %+v", evs)
	}
	if Format(evs) != Prefix+"first\n\n"+Prefix+"second" {
		t.Fatalf("format %q", Format(evs))
	}

	now := time.Now()
	soon, _ := AddTimer("s", now.Add(time.Second), "check CI")
	AddTimer("s", now.Add(time.Hour), "later")
	if n := FireDue("s", now); n != 0 {
		t.Fatalf("fired early: %d", n)
	}
	if n := FireDue("s", now.Add(2*time.Second)); n != 1 {
		t.Fatalf("fired %d", n)
	}
	evs = Drain("s")
	if len(evs) != 1 || !strings.Contains(evs[0].Text, "check CI") || len(Timers("s")) != 1 {
		t.Fatalf("timer event %+v", evs)
	}
	if CancelTimer("s", soon.ID) == nil {
		t.Fatal("fired timer should be gone")
	}
}

func TestRecurringTimerNoDrift(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	t0 := time.Date(2026, 10, 5, 12, 0, 0, 0, time.Local)
	tm, err := AddRecurringTimer("s", t0, 10*time.Minute, 0, time.Time{}, "poll")
	if err != nil || !tm.Due.Equal(t0.Add(10*time.Minute)) {
		t.Fatalf("add: %+v %v", tm, err)
	}
	if n := FireDue("s", t0.Add(9*time.Minute)); n != 0 {
		t.Fatal("fired early")
	}
	// Checked 3 minutes late: the next firing is still on the original grid.
	if n := FireDue("s", t0.Add(13*time.Minute)); n != 1 {
		t.Fatalf("fired %d", n)
	}
	ts := Timers("s")
	if len(ts) != 1 || !ts[0].Due.Equal(t0.Add(20*time.Minute)) {
		t.Fatalf("rescheduled %+v", ts)
	}
	evs := Drain("s")
	if len(evs) != 1 || strings.Contains(evs[0].Text, "skipped") || !strings.Contains(evs[0].Text, "poll") {
		t.Fatalf("event %+v", evs)
	}
}

func TestRecurringTimerMissedIntervals(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	t0 := time.Date(2026, 10, 5, 12, 0, 0, 0, time.Local)
	AddRecurringTimer("s", t0, 10*time.Minute, 0, time.Time{}, "poll")
	// Due 12:10; at 12:47 intervals 12:10 (fires), 12:20, 12:30, 12:40 are due.
	if n := FireDue("s", t0.Add(47*time.Minute)); n != 1 {
		t.Fatalf("burst: fired %d", n)
	}
	evs := Drain("s")
	if len(evs) != 1 || !strings.Contains(evs[0].Text, "skipped 3 missed") {
		t.Fatalf("event %+v", evs)
	}
	if ts := Timers("s"); len(ts) != 1 || !ts[0].Due.Equal(t0.Add(50*time.Minute)) {
		t.Fatalf("next %+v", ts)
	}
	if n := FireDue("s", t0.Add(47*time.Minute)); n != 0 {
		t.Fatal("fired twice")
	}
}

func TestRecurringTimerCount(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	t0 := time.Date(2026, 10, 5, 12, 0, 0, 0, time.Local)
	AddRecurringTimer("s", t0, time.Minute, 2, time.Time{}, "twice")
	FireDue("s", t0.Add(time.Minute))
	if ts := Timers("s"); len(ts) != 1 || ts[0].Left != 1 {
		t.Fatalf("after first %+v", ts)
	}
	FireDue("s", t0.Add(2*time.Minute))
	if len(Timers("s")) != 0 {
		t.Fatal("timer should be gone after its last firing")
	}
	if evs := Drain("s"); len(evs) != 2 || !strings.Contains(evs[1].Text, "finished") {
		t.Fatalf("events %+v", evs)
	}
	// Skipped intervals count against the limit.
	AddRecurringTimer("s", t0, time.Minute, 3, time.Time{}, "burst")
	FireDue("s", t0.Add(10*time.Minute))
	if len(Timers("s")) != 0 || len(Drain("s")) != 1 {
		t.Fatal("exhausted by skipped intervals: want one event and no timer")
	}
}

func TestRecurringTimerUntilAndCancel(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	t0 := time.Date(2026, 10, 5, 12, 0, 0, 0, time.Local)
	if _, err := AddRecurringTimer("s", t0, 10*time.Minute, 0, t0.Add(5*time.Minute), "x"); err == nil {
		t.Fatal("until before first firing should fail")
	}
	AddRecurringTimer("s", t0, 10*time.Minute, 0, t0.Add(25*time.Minute), "until")
	FireDue("s", t0.Add(10*time.Minute)) // next 12:20, within until
	if len(Timers("s")) != 1 {
		t.Fatal("should continue")
	}
	FireDue("s", t0.Add(20*time.Minute)) // next 12:30 > 12:25
	if len(Timers("s")) != 0 {
		t.Fatal("should stop at until")
	}
	if evs := Drain("s"); len(evs) != 2 {
		t.Fatalf("events %+v", evs)
	}
	// Overdue long after until: the 12:10 occurrence was inside the window,
	// so it fires once, then the timer ends.
	AddRecurringTimer("s", t0, 10*time.Minute, 0, t0.Add(15*time.Minute), "stale")
	if n := FireDue("s", t0.Add(3*time.Hour)); n != 1 {
		t.Fatalf("fired %d", n)
	}
	if len(Timers("s")) != 0 {
		t.Fatal("stale timer should be gone")
	}
	Drain("s")

	c, _ := AddRecurringTimer("s", t0, time.Minute, 0, time.Time{}, "cancel me")
	if err := CancelTimer("s", c.ID); err != nil || len(Timers("s")) != 0 {
		t.Fatal("cancel")
	}
	if FireDue("s", t0.Add(time.Hour)) != 0 {
		t.Fatal("canceled timer fired")
	}
}

func TestRecurringTimerMinimumAndLegacyFiles(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	if _, err := AddRecurringTimer("s", time.Now(), 30*time.Second, 0, time.Time{}, "x"); err == nil {
		t.Fatal("want minimum interval error")
	}
	// A one-shot file written before recurring timers existed still loads.
	old := `{"id":"abc123","due":"2026-10-05T12:00:00Z","message":"old","created":"2026-10-05T11:00:00Z"}`
	if err := writeAtomic(filepath.Join(timerDir("s"), "abc123.json"), []byte(old)); err != nil {
		t.Fatal(err)
	}
	ts := Timers("s")
	if len(ts) != 1 || ts[0].Recurring() || ts[0].Schedule() != "" {
		t.Fatalf("legacy %+v", ts)
	}
	if FireDue("s", time.Date(2026, 10, 5, 13, 0, 0, 0, time.UTC)) != 1 || len(Timers("s")) != 0 {
		t.Fatal("legacy timer should fire once and vanish")
	}
}

func TestParseWhen(t *testing.T) {
	now := time.Date(2026, 10, 5, 14, 0, 0, 0, time.Local)
	if d, _ := ParseWhen("10m", now); !d.Equal(now.Add(10 * time.Minute)) {
		t.Fatal(d)
	}
	if d, _ := ParseWhen("15:30", now); d.Hour() != 15 || d.Day() != 5 {
		t.Fatal(d)
	}
	if d, _ := ParseWhen("09:00", now); d.Day() != 6 {
		t.Fatal("past clock time should mean tomorrow", d)
	}
	if _, err := ParseWhen("soon", now); err == nil {
		t.Fatal("want error")
	}
}

func TestConcurrentDrainClaimsEachEventOnce(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	for round := range 20 {
		for i := range 40 {
			if err := Push("s", Event{Text: fmt.Sprint(i)}); err != nil {
				t.Fatal(err)
			}
		}
		start := make(chan struct{})
		results := make(chan []Event, 2)
		var wg sync.WaitGroup
		for range 2 {
			wg.Go(func() { <-start; results <- Drain("s") })
		}
		close(start)
		wg.Wait()
		close(results)
		seen := make(map[string]bool)
		for evs := range results {
			for _, e := range evs {
				if seen[e.Text] {
					t.Fatalf("round %d: duplicate %s", round, e.Text)
				}
				seen[e.Text] = true
			}
		}
		if len(seen) != 40 {
			t.Fatalf("round %d: got %d events", round, len(seen))
		}
	}
}

func TestDrainWaitsForConsumerLock(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	if err := Push("s", Event{Text: "first"}); err != nil {
		t.Fatal(err)
	}
	locked, release := make(chan struct{}), make(chan struct{})
	lockDone := make(chan error, 1)
	go func() {
		lockDone <- fsutil.WithFileLock(filepath.Join(Dir("s"), ".drain"), func() error {
			close(locked)
			<-release
			return nil
		})
	}()
	defer close(release)
	select {
	case <-locked:
	case err := <-lockDone:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("consumer lock not acquired")
	}
	drained := make(chan []Event, 1)
	go func() { drained <- Drain("s") }()
	select {
	case evs := <-drained:
		t.Fatalf("drained while locked: %+v", evs)
	case <-time.After(50 * time.Millisecond):
	}
	release <- struct{}{}
	select {
	case evs := <-drained:
		if len(evs) != 1 || evs[0].Text != "first" {
			t.Fatalf("drain: %+v", evs)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("drain did not finish")
	}
	if err := <-lockDone; err != nil {
		t.Fatal(err)
	}
}

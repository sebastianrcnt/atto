package core

import (
	"testing"
	"time"
)

type memoryTimer struct {
	delay   time.Duration
	fire    func()
	stopped bool
}

func (t *memoryTimer) Stop() bool {
	was := t.stopped
	t.stopped = true
	return !was
}

func memoryTest() (*idleMemory, *time.Time, *[]*memoryTimer, *int) {
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	var timers []*memoryTimer
	freed := 0
	m := &idleMemory{
		now: func() time.Time { return now },
		after: func(d time.Duration, f func()) idleTimer {
			t := &memoryTimer{delay: d, fire: f}
			timers = append(timers, t)
			return t
		},
		free: func() { freed++ },
	}
	return m, &now, &timers, &freed
}

func TestIdleMemoryAfterWork(t *testing.T) {
	m, now, timers, freed := memoryTest()
	m.Begin()
	m.Begin() // a second turn or input while the first turn runs
	*now = now.Add(time.Hour)
	m.End()
	if len(*timers) != 0 || *freed != 0 {
		t.Fatal("scheduled a release while work was active")
	}
	m.End()
	if len(*timers) != 1 || (*timers)[0].delay != idleMemoryDelay {
		t.Fatalf("timers: %+v", *timers)
	}
	*now = now.Add(idleMemoryDelay)
	(*timers)[0].fire()
	if *freed != 1 || m.timer != nil || len(*timers) != 1 {
		t.Fatal("idle release did not fire once and disarm")
	}
}

func TestIdleMemoryInputRestartsDelay(t *testing.T) {
	m, now, timers, freed := memoryTest()
	m.Begin()
	m.End()
	old := (*timers)[0]
	*now = now.Add(idleMemoryDelay - time.Second)
	m.Begin()
	old.fire() // Stop cannot prevent an already-started callback
	if !old.stopped || *freed != 0 {
		t.Fatal("new work did not cancel the pending release")
	}
	m.End()
	old.fire() // also stale after the new work ends
	if *freed != 0 || len(*timers) != 2 || (*timers)[1].delay != idleMemoryDelay {
		t.Fatal("input did not restart the idle delay")
	}
	*now = now.Add(idleMemoryDelay)
	(*timers)[1].fire()
	if *freed != 1 {
		t.Fatal("no release after input went idle")
	}
}

func TestIdleMemoryMinimumInterval(t *testing.T) {
	m, now, timers, freed := memoryTest()
	m.Begin()
	m.End()
	*now = now.Add(idleMemoryDelay)
	(*timers)[0].fire()
	last := *now
	for range 3 {
		*now = now.Add(time.Second)
		m.Begin()
		m.End()
		timer := (*timers)[len(*timers)-1]
		if want := last.Add(idleMemoryInterval).Sub(*now); timer.delay != want {
			t.Fatalf("delay %s, want %s", timer.delay, want)
		}
	}
	*now = last.Add(idleMemoryInterval)
	(*timers)[len(*timers)-1].fire()
	if *freed != 2 {
		t.Fatalf("freed %d times", *freed)
	}
	// Once the interval has elapsed, a fresh turn still gets the idle delay.
	*now = now.Add(idleMemoryInterval)
	m.Begin()
	m.End()
	if got := (*timers)[len(*timers)-1].delay; got != idleMemoryDelay {
		t.Fatalf("delay %s, want %s", got, idleMemoryDelay)
	}
}

func TestIdleMemoryClose(t *testing.T) {
	m, _, timers, freed := memoryTest()
	m.Begin()
	m.End()
	old := (*timers)[0]
	m.Close()
	m.Close()
	old.fire()
	m.Begin()
	m.End()
	if !old.stopped || *freed != 0 || len(*timers) != 1 {
		t.Fatal("closed reclaimer scheduled or released memory")
	}
}

func TestIdleMemoryReleaseExcludesWork(t *testing.T) {
	m, _, timers, _ := memoryTest()
	entered, finish, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	m.free = func() {
		close(entered)
		<-finish
	}
	m.Begin()
	m.End()
	go func() {
		(*timers)[0].fire()
		close(done)
	}()
	<-entered
	// Begin must take this same lock before any work can start.
	if m.mu.TryLock() {
		m.mu.Unlock()
		t.Error("release did not exclude new work")
	}
	close(finish)
	<-done
	m.Begin()
	if m.active != 1 || m.timer != nil {
		t.Fatal("work did not start after release")
	}
	m.End()
	m.Close()
}

package jobs

import (
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/events"
)

// collector gathers posted events thread-safely.
type collector struct {
	mu  sync.Mutex
	evs []events.Event
}

func (c *collector) post(e events.Event) { c.mu.Lock(); c.evs = append(c.evs, e); c.mu.Unlock() }
func (c *collector) all() []events.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]events.Event(nil), c.evs...)
}

func testNotifier(c *collector, pattern string, limit int, window time.Duration) *notifier {
	n := newNotifier(io.Discard, Job{ID: 7, Name: "logs", Notify: &Notify{Pattern: pattern, Limit: limit}}, c.post)
	n.window = window
	return n
}

func TestNotifierMatchesAndBatches(t *testing.T) {
	c := &collector{}
	n := testNotifier(c, "ERROR|panic", 0, 100*time.Millisecond)
	// Lines split across writes.
	n.Write([]byte("all fine\nERR"))
	n.Write([]byte("OR one\nnothing\npanic: two\n"))
	if len(c.all()) != 0 {
		t.Fatal("posted before the batch window closed")
	}
	time.Sleep(250 * time.Millisecond)
	evs := c.all()
	if len(evs) != 1 {
		t.Fatalf("want one batched event, got %+v", evs)
	}
	for _, want := range []string{"job 7", `"logs"`, "ERROR one", "panic: two"} {
		if !strings.Contains(evs[0].Text, want) {
			t.Fatalf("event lacks %q: %s", want, evs[0].Text)
		}
	}
	if strings.Contains(evs[0].Text, "all fine") || strings.Contains(evs[0].Text, "nothing") {
		t.Fatalf("non-matching line in event: %s", evs[0].Text)
	}
	// A later match is a new event; Close flushes an unterminated last line.
	n.Write([]byte("ERROR three"))
	n.Close()
	if evs = c.all(); len(evs) != 2 || !strings.Contains(evs[1].Text, "ERROR three") {
		t.Fatalf("after close: %+v", evs)
	}
}

func TestNotifierCap(t *testing.T) {
	c := &collector{}
	n := testNotifier(c, "hit", 3, 10*time.Millisecond)
	for i := 0; i < 6; i++ {
		n.Write([]byte("hit\n"))
		time.Sleep(40 * time.Millisecond)
	}
	n.Close()
	evs := c.all()
	if len(evs) != 3 {
		t.Fatalf("want 3 events, got %d", len(evs))
	}
	last := evs[2].Text
	if !strings.Contains(last, "capped at 3") || !strings.Contains(last, "atto job output 7") {
		t.Fatalf("final event lacks cap note: %s", last)
	}
	if strings.Contains(evs[1].Text, "capped") {
		t.Fatal("cap note too early")
	}
}

func TestNotifierTruncatesAndLimitsBatch(t *testing.T) {
	c := &collector{}
	n := testNotifier(c, "x", 0, time.Hour)
	n.Write([]byte(strings.Repeat("x", 5000) + "\n"))
	for i := 0; i < notifyLines+5; i++ {
		n.Write([]byte("x\n"))
	}
	n.Close()
	evs := c.all()
	if len(evs) != 1 || len(evs[0].Text) > 2000 || !strings.Contains(evs[0].Text, "6 more matching lines") {
		t.Fatalf("events %+v", evs)
	}
}

// A job outlives the command that started it, so atto view in it gets no
// directory to leave images in.
func TestJobsGetNoViewDir(t *testing.T) {
	t.Setenv(config.EnvView, "/from/atto")
	for _, env := range [][]string{nil, {"A=1", config.EnvView + "=/from/call"}} {
		for _, kv := range withoutView(env) {
			if strings.HasPrefix(kv, config.EnvView+"=") {
				t.Fatalf("%v kept %s", env, kv)
			}
		}
	}
}

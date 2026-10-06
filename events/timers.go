package events

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/fsutil"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Timer delivers Message to the session inbox at Due. Timers are files so
// the model can set them from a shell (`atto timer in 10m "..."`); the
// front end checks them while it runs, and overdue timers fire on resume.
type Timer struct {
	ID      string    `json:"id"`
	Due     time.Time `json:"due"`
	Message string    `json:"message"`
	Created time.Time `json:"created"`

	// Recurring timers (`atto timer every`). Old one-shot files have none
	// of these and load as before.
	Every time.Duration `json:"every,omitempty"` // interval; 0 = one-shot
	Left  int           `json:"left,omitempty"`  // firings remaining; 0 = unlimited
	Until *time.Time    `json:"until,omitempty"` // stop once the next firing would pass this
}

// MinEvery is the shortest recurring interval, so a model cannot create a
// busy loop that floods its own inbox.
const MinEvery = time.Minute

// Recurring reports whether the timer repeats.
func (t Timer) Recurring() bool { return t.Every > 0 }

// Schedule describes a recurring timer for listings ("" for one-shots).
func (t Timer) Schedule() string {
	if !t.Recurring() {
		return ""
	}
	s := "every " + t.Every.String()
	if t.Left > 0 {
		s += fmt.Sprintf(", %d left", t.Left)
	}
	if t.Until != nil {
		s += ", until " + t.Until.Format("15:04")
	}
	return s
}

func timerDir(session string) string { return filepath.Join(Dir(session), "timers") }

// AddTimer schedules a timer and returns it.
func AddTimer(session string, due time.Time, message string) (Timer, error) {
	if !validSession(session) {
		return Timer{}, fmt.Errorf("no session")
	}
	t := Timer{ID: randID()[:6], Due: due, Message: message, Created: time.Now()}
	data, _ := json.Marshal(t)
	return t, timerLock(session, func() error { return writeAtomic(filepath.Join(timerDir(session), t.ID+".json"), data) })
}

// AddRecurringTimer schedules a timer that fires every interval, first at
// now+every. count > 0 limits the number of firings; a non-zero until stops
// it once the next firing would fall after that time.
func AddRecurringTimer(session string, now time.Time, every time.Duration, count int, until time.Time, message string) (Timer, error) {
	if !validSession(session) {
		return Timer{}, fmt.Errorf("no session")
	}
	if every < MinEvery {
		return Timer{}, fmt.Errorf("interval %s is too short: the minimum is %s", every, MinEvery)
	}
	if count < 0 {
		return Timer{}, fmt.Errorf("count must be positive")
	}
	t := Timer{ID: randID()[:6], Due: now.Add(every), Message: message, Created: now, Every: every, Left: count}
	if !until.IsZero() {
		if until.Before(t.Due) {
			return Timer{}, fmt.Errorf("until %s is before the first firing at %s", until.Format("15:04"), t.Due.Format("15:04"))
		}
		t.Until = &until
	}
	data, _ := json.Marshal(t)
	return t, timerLock(session, func() error { return writeAtomic(filepath.Join(timerDir(session), t.ID+".json"), data) })
}

// Timers lists pending timers, soonest first.
func timerLock(session string, fn func() error) error {
	if !validSession(session) {
		return fmt.Errorf("invalid session id")
	}
	if err := fsutil.PrivateDirs(config.Dir(), timerDir(session)); err != nil {
		return err
	}
	return fsutil.WithFileLock(filepath.Join(timerDir(session), ".timers"), fn)
}

// recoverTimerClaims runs under the directory lock: no live consumer can
// still own a claim. A rescheduled JSON takes precedence over an old claim.
func recoverTimerClaims(session string) {
	dir := timerDir(session)
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		id := strings.TrimSuffix(strings.TrimPrefix(e.Name(), "."), ".claim")
		if e.Name() != "."+id+".claim" || !validTimerID(id) {
			continue
		}
		claim, path := filepath.Join(dir, e.Name()), filepath.Join(dir, id+".json")
		if _, err := os.Stat(path); err == nil {
			_ = os.Remove(claim)
		} else if os.IsNotExist(err) {
			_ = os.Rename(claim, path)
		}
	}
}

func Timers(session string) []Timer {
	var out []Timer
	_ = timerLock(session, func() error {
		recoverTimerClaims(session)
		out = timersLocked(session)
		return nil
	})
	return out
}

func timersLocked(session string) []Timer {
	ents, _ := os.ReadDir(timerDir(session))
	var out []Timer
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(timerDir(session), e.Name()))
		var t Timer
		if err == nil && json.Unmarshal(data, &t) == nil && validTimerID(t.ID) && e.Name() == t.ID+".json" {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Due.Before(out[j].Due) })
	return out
}

// CancelTimer removes a timer by ID.
func validTimerID(id string) bool {
	if len(id) != 6 || strings.ToLower(id) != id {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func CancelTimer(session, id string) error {
	if !validSession(session) || !validTimerID(id) {
		return fmt.Errorf("invalid timer or session id")
	}
	return timerLock(session, func() error { return cancelTimerLocked(session, id) })
}

func cancelTimerLocked(session, id string) error {
	found := false
	for _, name := range []string{id + ".json", "." + id + ".claim"} {
		err := os.Remove(filepath.Join(timerDir(session), name))
		if err == nil {
			found = true
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if !found {
		return fmt.Errorf("no timer %q", id)
	}
	return nil
}

// FireDue moves due timers into the inbox and returns how many fired.
//
// A recurring timer is rescheduled from its scheduled time, not from now,
// so it does not drift. If several intervals passed while nothing was
// running it fires once and reports how many it skipped; skipped intervals
// still count against -count, since they were scheduled firings.
func FireDue(session string, now time.Time) int {
	n := 0
	_ = timerLock(session, func() error {
		recoverTimerClaims(session)
		n = fireDueLocked(session, now)
		return nil
	})
	return n
}

func fireDueLocked(session string, now time.Time) int {
	n := 0
	for _, t := range timersLocked(session) {
		if t.Due.After(now) {
			continue
		}
		path := filepath.Join(timerDir(session), t.ID+".json")
		if !t.Recurring() {
			if cancelTimerLocked(session, t.ID) != nil {
				continue // another consumer took it
			}
			text := fmt.Sprintf("Timer %s fired: %s (set %s ago)", t.ID, t.Message, now.Sub(t.Created).Round(time.Second))
			_ = Push(session, Event{Source: "timer", Text: text, Title: "⏱ " + t.Message})
			n++
			continue
		}
		// Keep the old schedule in a claim until its replacement is durable.
		claim := filepath.Join(timerDir(session), "."+t.ID+".claim")
		if os.Rename(path, claim) != nil {
			continue
		}
		if t.Until != nil && t.Due.After(*t.Until) {
			os.Remove(claim) // expired while nothing was running
			continue
		}
		skipped := int(now.Sub(t.Due) / t.Every)
		next := t.Due.Add(time.Duration(skipped+1) * t.Every)
		done := t.Until != nil && next.After(*t.Until)
		if t.Left > 0 {
			t.Left -= 1 + skipped
			done = done || t.Left <= 0
		}
		text := fmt.Sprintf("Timer %s fired: %s (%s", t.ID, t.Message, t.Schedule())
		if skipped > 0 {
			text += fmt.Sprintf("; skipped %d missed", skipped)
		}
		if done {
			text += "; last firing, timer finished"
		} else {
			t.Due = next
		}
		text += ")"
		if done {
			os.Remove(claim)
		} else {
			data, _ := json.Marshal(t)
			if writeAtomic(path, data) == nil {
				os.Remove(claim)
			}
		}
		_ = Push(session, Event{Source: "timer", Text: text, Title: "⏱ " + t.Message})
		n++
	}
	return n
}

// ParseWhen turns "10m", "1h30m", "90s" or "15:04" (today, or tomorrow if
// past) into a due time.
func ParseWhen(s string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(s); err == nil {
		if d <= 0 {
			return time.Time{}, fmt.Errorf("duration must be positive")
		}
		return now.Add(d), nil
	}
	if t, err := time.ParseInLocation("15:04", s, now.Location()); err == nil {
		due := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
		if !due.After(now) {
			due = due.Add(24 * time.Hour)
		}
		return due, nil
	}
	return time.Time{}, fmt.Errorf("bad time %q: use a duration like 10m or a clock time like 15:04", s)
}

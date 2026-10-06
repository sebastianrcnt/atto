// Package events is a per-session inbox for things that should reach the
// agent without the user typing: background jobs finishing, timers firing,
// monitors matching. Producers are often separate processes (a job
// supervisor, `atto timer` run by the model), so the inbox is a directory:
// one JSON file per event, written atomically, removed when consumed.
//
// The front end (TUI, daemon) drains the inbox: when the agent is idle an
// event starts a turn; when it is busy the event is delivered like a steer,
// after the next tool call. Either way it is appended to the conversation,
// so the prompt prefix (and its cache) is untouched.
package events

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/fsutil"
)

// Prefix marks event text in the conversation.
const Prefix = "[atto event] "

type Event struct {
	Time   time.Time `json:"time"`
	Source string    `json:"source"` // "job", "timer", "monitor", "goal"
	Text   string    `json:"text"`   // what the model sees (after Prefix)
	Title  string    `json:"title"`  // one-line summary for the UI
	// Quiet events don't start a turn: an idle session keeps them until
	// its next turn, a running one takes them after its current step (an
	// agent's message sent with atto agent send).
	Quiet bool `json:"quiet,omitempty"`
}

// Wakes reports whether evs should start a turn: any of them not quiet.
func Wakes(evs []Event) bool {
	for _, e := range evs {
		if !e.Quiet {
			return true
		}
	}
	return false
}

// Requeue puts taken events back, in their order, for a later turn.
func Requeue(session string, evs []Event) {
	for _, e := range evs {
		_ = Push(session, e)
	}
}

// SourceReload marks a request to reload the session's configuration
// (`atto reload`). The front end applies it between steps instead of
// passing it to the model, and reports the result in an event of its own.
const SourceReload = "reload"

// RequestReload asks the front end running session to reload.
func RequestReload(session string) error {
	return Push(session, Event{Source: SourceReload, Title: "reload requested"})
}

// SplitReload separates reload requests from the other events.
func SplitReload(evs []Event) (reload bool, rest []Event) {
	for _, e := range evs {
		if e.Source == SourceReload {
			reload = true
		} else {
			rest = append(rest, e)
		}
	}
	return reload, rest
}

// Dir is the inbox for a session.
func validSession(session string) bool { return fsutil.ValidID(session) }

func Dir(session string) string {
	if !validSession(session) {
		session = ".invalid-session"
	}
	return filepath.Join(config.Dir(), "inbox", session)
}

func randID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// writeAtomic writes data to path via a temp file and rename, so readers
// never see a partial file.
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return fsutil.WriteAtomic(path, data, 0o644)
}

// Push adds an event to a session's inbox.
func Push(session string, e Event) error {
	if !validSession(session) {
		return fmt.Errorf("no session")
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	name := fmt.Sprintf("%020d-%s.json", e.Time.UnixNano(), randID())
	return writeAtomic(filepath.Join(Dir(session), name), data)
}

// Drain removes and returns pending events, oldest first.
func Drain(session string) []Event {
	ents, err := os.ReadDir(Dir(session))
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range ents {
		if n := e.Name(); strings.HasSuffix(n, ".json") && !strings.HasPrefix(n, ".") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	var out []Event
	for _, n := range names {
		p := filepath.Join(Dir(session), n)
		claimed := filepath.Join(Dir(session), "."+n+"-"+randID())
		if os.Rename(p, claimed) != nil {
			continue // another reader claimed it
		}
		data, err := os.ReadFile(claimed)
		_ = os.Remove(claimed)
		if err != nil {
			continue
		}
		var e Event
		if json.Unmarshal(data, &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// Pending reports whether the inbox has events (used by `atto sleep`).
func Pending(session string) bool {
	ents, _ := os.ReadDir(Dir(session))
	for _, e := range ents {
		if n := e.Name(); strings.HasSuffix(n, ".json") && !strings.HasPrefix(n, ".") {
			return true
		}
	}
	return false
}

// Wake signals waiters (`atto sleep`, `atto job wait`) that the user sent
// input, so they return early and the agent sees it sooner.
func Wake(session string) {
	if validSession(session) {
		_ = writeAtomic(filepath.Join(Dir(session), ".wake"), []byte(time.Now().Format(time.RFC3339Nano)))
	}
}

// WokenSince reports whether Wake was called after t.
func WokenSince(session string, t time.Time) bool {
	st, err := os.Stat(filepath.Join(Dir(session), ".wake"))
	return err == nil && st.ModTime().After(t)
}

// Format renders events as one message for the model.
func Format(evs []Event) string {
	var b strings.Builder
	for i, e := range evs {
		if i > 0 {
			b.WriteString("\n\n")
		}
		if !strings.HasPrefix(e.Text, "<atto_internal_context") { // an envelope says what it is
			b.WriteString(Prefix)
		}
		b.WriteString(e.Text)
	}
	return b.String()
}

// envelopeStart begins a message one agent sends another (see package
// subagent): an event of its own kind, not prefixed.
const envelopeStart = `<atto_internal_context source="agent">`

// IsEvent reports whether a message to the model is events: prefixed, or
// an agent's message.
func IsEvent(text string) bool {
	return strings.HasPrefix(text, Prefix) || strings.HasPrefix(text, envelopeStart)
}

// Split takes a message Format made apart into its events' texts.
func Split(text string) []string {
	var out []string
	rest := text
	for rest != "" {
		next := len(rest)
		for _, sep := range []string{"\n\n" + Prefix, "\n\n" + envelopeStart} {
			if i := strings.Index(rest[1:], sep); i >= 0 && i+1 < next {
				next = i + 1
			}
		}
		out = append(out, strings.TrimPrefix(rest[:next], Prefix))
		rest = strings.TrimPrefix(rest[next:], "\n\n")
	}
	return out
}

// TitleOf is a one-line title for an event's text: for an agent's message,
// its type and sender ("◆ FINAL_ANSWER from /root/tests"), else its first
// line.
func TitleOf(text string) string {
	if !strings.HasPrefix(text, envelopeStart) {
		first, _, _ := strings.Cut(text, "\n")
		return first
	}
	var kind, from string
	for line := range strings.SplitSeq(text, "\n") {
		if v, ok := strings.CutPrefix(line, "Message Type: "); ok {
			kind = v
		}
		if v, ok := strings.CutPrefix(line, "From: "); ok {
			from = v
			break
		}
	}
	return "◆ " + strings.ToLower(strings.ReplaceAll(kind, "_", " ")) + " from " + from
}

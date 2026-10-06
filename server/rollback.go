package server

import (
	"strings"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/session"
)

// restore loads the active branch of entries into the thread's agent and
// items. Call only while no turn runs.
func (t *thread) restore(entries []session.Entry) {
	branch := session.Active(entries)
	t.agent.Restore(branch)
	t.feed.Lock()
	defer t.feed.Unlock()
	items, bl := replayItems(&t.tr, t.id, branch)
	t.mu.Lock()
	t.ctxTokens = t.agent.ContextTokens()
	t.items, t.blocks = items, bl
	t.total = UsageOf(branch)
	t.steers = nil // Restore drops them
	t.mu.Unlock()
}

// rollback implements thread/rollback, named after codex's method: it goes
// back to before the numTurns-th last user message (default 1), the way
// picking that message in atto's /tree does. The session keeps the old
// branch; the result carries the message text as "input" for editing.
func (s *Server) rollback(p threadParams) (any, error) {
	t, err := s.thread(p.ThreadID)
	if err != nil {
		return nil, err
	}
	n := p.NumTurns
	if n == 0 {
		n = 1
	}
	if n < 0 {
		return nil, invalid("numTurns must be positive")
	}
	t.mu.Lock()
	if t.busy {
		t.mu.Unlock()
		return nil, &rpcError{Code: codeServer, Message: "a turn is running; turn/interrupt first"}
	}
	t.busy = true // no turn may start while the branch moves
	t.mu.Unlock()
	text, err := t.rollback(n)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.busy = false
	if err != nil {
		return nil, err
	}
	info := t.info()
	info.Items = append([]Item(nil), t.items...)
	return struct {
		ThreadInfo
		Input string `json:"input"`
	}{info, text}, nil
}

// UserMessages are the messages the user sent on a branch: not inbox
// events, compaction summaries, a Stop hook's reason for going on or goal
// messages, which atto sends in the user's role. Rolling back n turns goes
// back to before the n-th last.
func UserMessages(branch []session.Entry) []session.Entry {
	var users []session.Entry
	for _, e := range branch {
		if m := e.Message; e.Type == session.TypeMessage && m != nil && m.Role == "user" && !synthetic(m.Content) {
			users = append(users, e)
		}
	}
	return users
}

// synthetic reports whether a user-role message is atto's own. Each marker
// sits at the front of the message, so a goal note riding at the end of the
// user's message leaves it theirs.
func synthetic(text string) bool {
	return events.IsEvent(text) || goal.IsMessage(text) ||
		strings.HasPrefix(text, agent.SummaryPrefix) || strings.HasPrefix(text, agent.StopHookPrefix)
}

func (t *thread) rollback(n int) (string, error) {
	_, entries, err := session.Load(t.sess.Path)
	if err != nil {
		return "", err
	}
	users := UserMessages(session.Active(entries))
	if n > len(users) {
		return "", invalid("only %d user messages to roll back", len(users))
	}
	target := users[len(users)-n]
	leaf, text, _ := session.BranchPoint(entries, target.ID)
	t.sess.Branch(leaf)
	if err := t.sess.Err(); err != nil {
		return "", err
	}
	if _, entries, err = session.Load(t.sess.Path); err != nil {
		return "", err
	}
	t.restore(entries)
	return text, nil
}

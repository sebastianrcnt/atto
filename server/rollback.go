package server

import (
	"strings"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/session"
)

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

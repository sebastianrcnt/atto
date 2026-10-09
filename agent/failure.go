package agent

import (
	"fmt"
	"unicode/utf8"

	"github.com/sebastianrcnt/atto/ai"
	"github.com/sebastianrcnt/atto/session"
)

// failureText is the message a session keeps of a failed turn, cut to
// maxFailureText bytes on a rune boundary.
const maxFailureText = 2000

func failureText(err error) string {
	s := err.Error()
	if len(s) <= maxFailureText {
		return s
	}
	cut := maxFailureText
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return fmt.Sprintf("%s... [truncated %d bytes]", s[:cut], len(s)-cut)
}

// recordFailure keeps in the session why the turn ended, which the request
// log alone used to know: a display-only entry (session.TypeError) with
// the provider, the model, the HTTP status and the server's message.
func (a *Agent) recordFailure(err error) {
	if a.Record == nil || err == nil {
		return
	}
	model, _ := a.Current()
	a.Record(session.Entry{Type: session.TypeError, Provider: model.ProviderName, Model: model.Model.ID,
		HTTPStatus: ai.StatusOf(err), Error: failureText(err)})
}

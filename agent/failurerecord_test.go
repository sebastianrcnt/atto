package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

const visionDown = "the vision encoder could not start: [Errno 2] No such file or directory: '/opt/strata/strata-vision'; the next request restarts it"

func errorEntries(rec []session.Entry) []session.Entry {
	var out []session.Entry
	for _, e := range rec {
		if e.Type == session.TypeError {
			out = append(out, e)
		}
	}
	return out
}

// A turn that ends because a model request failed leaves an entry saying
// which provider and model, the HTTP status and the server's message.
func TestFailedTurnIsRecorded(t *testing.T) {
	noWait(t)
	srv, _ := scriptedServer(t, status(503, visionDown))
	a := newTestAgent(srv.URL)
	var rec []session.Entry
	a.Record = func(e session.Entry) { rec = append(rec, e) }
	err := a.Run(context.Background(), "go", func(any) {})
	if err == nil {
		t.Fatal("the turn should fail")
	}
	got := errorEntries(rec)
	if len(got) != 1 {
		t.Fatalf("%d error entries in %+v", len(got), rec)
	}
	e := got[0]
	if e.Provider != "t" || e.Model != "m" || e.HTTPStatus != 503 || !strings.Contains(e.Error, "strata-vision") || e.Error != err.Error() {
		t.Fatalf("entry %+v for error %q", e, err)
	}
	if last := rec[len(rec)-1]; last.Type != session.TypeError {
		t.Fatalf("the failure should be the last entry, got %q", last.Type)
	}
}

// A long server message is cut, on a rune boundary.
func TestFailureTextIsCut(t *testing.T) {
	long := strings.Repeat("é", maxFailureText)
	got := failureText(fmt.Errorf("503: %s", long))
	if len(got) > maxFailureText+64 || !strings.Contains(got, "truncated") || !strings.HasPrefix(got, "503: é") {
		t.Fatalf("%d bytes: %.40q...", len(got), got)
	}
	if strings.ContainsRune(got, '�') {
		t.Fatal("cut inside a rune")
	}
	if short := failureText(fmt.Errorf("boom")); short != "boom" {
		t.Fatalf("short text changed: %q", short)
	}
}

// An interrupted turn, or one that succeeds after a retry, records nothing.
func TestNoFailureEntryWithoutFailure(t *testing.T) {
	noWait(t)
	srv, _ := scriptedServer(t, status(500, "first"), okReply)
	a := newTestAgent(srv.URL)
	var rec []session.Entry
	a.Record = func(e session.Entry) { rec = append(rec, e) }
	if err := a.Run(context.Background(), "go", func(any) {}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = a.Run(ctx, "again", func(any) {})
	if got := errorEntries(rec); len(got) != 0 {
		t.Fatalf("unexpected %+v", got)
	}
}

// The entry is for the user: restoring the session gives the model the same
// conversation without it, and goal retries still see the error as transient.
func TestFailureEntryIsNotConversation(t *testing.T) {
	a := newTestAgent("http://unused")
	a.Restore([]session.Entry{
		{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "hello"}},
		{Type: session.TypeError, Provider: "t", Model: "m", HTTPStatus: 503, Error: "503: " + visionDown},
	})
	if got := a.Messages(); len(got) != 1 || got[0].Content != "hello" {
		t.Fatalf("conversation %+v", got)
	}
	_, req := a.request()
	for _, m := range req.Messages {
		if strings.Contains(m.Content, "strata-vision") {
			t.Fatalf("the failure reached the model: %+v", m)
		}
	}
}

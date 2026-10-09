package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

func TestScrollUpLoadsEarlierMessages(t *testing.T) {
	cwd, _ := testEnv(t)
	w := session.New(cwd)
	for i := range 350 {
		w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: fmt.Sprintf("answer-%03d", i)}})
	}
	w.Close()
	a := startApp(t, cwd, Options{Session: w.ID})
	if strings.Contains(shown(a), "answer-000") {
		t.Fatal("attach loaded entire history")
	}
	within(t, a, "paged snapshot", func() bool { return a.view.Info.HasMore })
	a.ui.Do(func() { a.ui.ScrollBy(1 << 20) })
	within(t, a, "earlier messages", func() bool { return strings.Contains(bodyText(a), "answer-000") && !a.pageLoading })
	count := strings.Count(shown(a), "answer-349")
	if count != 1 {
		t.Fatalf("page duplicated current item: count=%d", count)
	}
}

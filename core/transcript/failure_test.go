package transcript

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/session"
)

// A turn that a failed model request ended is kept in the session, and
// replaying the session shows the error notice the user saw, last.
func TestFailedTurnReplaysAsErrorNotice(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"the vision encoder could not start"}}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	a := agent.New(config.ModelRef{ProviderName: "p", Provider: config.Provider{BaseURL: srv.URL}, Model: config.Model{ID: "m"}}, "", os.TempDir())
	dir := t.TempDir()
	w := session.New(dir)
	t.Cleanup(w.Close) // Windows cannot remove a session file that is still open
	a.Record = w.Append
	runErr := a.Run(context.Background(), "hello", func(any) {})
	if runErr == nil {
		t.Fatal("the turn should fail")
	}

	path := w.Path
	if path == "" {
		t.Fatal("the session was not written")
	}
	active, err := session.ReadActive(path)
	if err != nil {
		t.Fatal(err)
	}
	items := FromEntries("t", active.Entries)
	if len(items) != 2 || items[0].Kind != User {
		t.Fatalf("items %+v", items)
	}
	n := items[1]
	if n.Kind != Notice || n.Level != "error" || n.Status != Completed ||
		n.Text != "Error: "+runErr.Error() || !strings.Contains(n.Text, "vision encoder") || !strings.Contains(n.Text, "401") {
		t.Fatalf("notice %+v for error %q", n, runErr)
	}

	// Resuming gives the model the conversation without the failure.
	b := agent.New(config.ModelRef{ProviderName: "p", Provider: config.Provider{BaseURL: srv.URL}, Model: config.Model{ID: "m"}}, "", os.TempDir())
	b.Restore(active.Entries)
	for _, m := range b.Messages() {
		if strings.Contains(m.Content, "vision encoder") {
			t.Fatalf("the failure reached the conversation: %+v", m)
		}
	}
}

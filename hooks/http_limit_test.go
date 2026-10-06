package hooks

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/config"
)

func TestHTTPHookResponseOverflow(t *testing.T) {
	body := `{"decision":"block","reason":"` + strings.Repeat("x", 1<<20) + `"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
	defer srv.Close()
	for _, event := range []string{"PreToolUse", "PostToolUse", "UserPromptSubmit", "Stop", "PreCompact", "SessionStart", "SessionEnd", "Notification"} {
		t.Run(event, func(t *testing.T) {
			r := New(map[string][]config.HookMatcher{event: {{Hooks: []config.HookSpec{{Type: "http", URL: srv.URL}}}}}, t.TempDir())
			o := fold(r.run(context.Background(), event, "", map[string]any{}), event)
			wantBlock := event == "PreToolUse" || event == "PostToolUse" || event == "UserPromptSubmit" || event == "Stop"
			if o.Block != wantBlock || len(o.Notices) != 1 || !strings.Contains(o.Notices[0], "exceeds 1 MiB") {
				t.Fatalf("overflow outcome: %+v", o)
			}
		})
	}
	// The exact limit is accepted; one extra byte is not silently discarded.
	body = `{"decision":"block"}` + strings.Repeat(" ", (1<<20)-len(`{"decision":"block"}`))
	r := New(map[string][]config.HookMatcher{"PreToolUse": {{Hooks: []config.HookSpec{{Type: "http", URL: srv.URL}}}}}, t.TempDir())
	o := fold(r.run(context.Background(), "PreToolUse", "", map[string]any{}), "PreToolUse")
	if !o.Block || len(o.Notices) != 0 {
		t.Fatalf("exact limit: %+v", o)
	}
}

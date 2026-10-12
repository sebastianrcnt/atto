package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/auth"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/ui"
)

// The status line of GPT-6.1 Sol on a fake Sign in with ChatGPT credential
// against a fake Responses server: the cost is an estimate (≈) without the
// 272k surcharge, and compaction waits for 450k (90% of the 500k
// subscription context). With an API key the tier still applies.
func TestSubscriptionStatusOverResponses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		login bool
		model string
		want  map[string]string
	}{
		{"sol login", true, "gpt-6.1-sol", map[string]string{"cost": "≈$0.610", "contextSize": "301k/922k ⇥450k"}},
		{"sol api key", false, "gpt-6.1-sol", map[string]string{"cost": "$1.215 ×2", "contextSize": "301k/922k ⇥245k"}},
		{"luna login", true, "gpt-6-luna", map[string]string{"cost": "≈$1.215 ×2", "contextSize": "301k/922k ⇥245k"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range []string{"ATTO_AGENT", config.EnvLegacyAgent, "ATTO_SESSION_ID", "ATTO_TOOL_CALL_ID", "OPENAI_API_KEY"} {
				t.Setenv(key, "")
			}
			var bearer string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				bearer = r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "text/event-stream")
				for _, l := range []string{
					`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"ok"}]}}`,
					`{"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":300000,"output_tokens":1000,"total_tokens":301000}}}`,
				} {
					fmt.Fprintf(w, "data: %s\n\n", l)
				}
			}))
			t.Cleanup(srv.Close)

			dir := t.TempDir()
			t.Setenv("ATTO_DIR", dir)
			t.Setenv("HOME", t.TempDir())
			os.MkdirAll(filepath.Join(dir, "cache"), 0o755)
			files := map[string]string{
				"cache/catalog.json": `{"openai":{"models":{
 "gpt-6.1-sol":{"id":"gpt-6.1-sol","name":"GPT-6.1 Sol","tool_call":true,"reasoning":true,"limit":{"context":1050000,"input":922000,"output":128000},
  "cost":{"input":2,"output":10,"tiers":[{"input":4,"output":15,"tier":{"type":"context","size":272000}}]}},
 "gpt-6-luna":{"id":"gpt-6-luna","name":"GPT-6 Luna","tool_call":true,"reasoning":true,"limit":{"context":1050000,"input":922000,"output":128000},
  "cost":{"input":2,"output":10,"tiers":[{"input":4,"output":15,"tier":{"type":"context","size":272000}}]}}}}}`,
				// The fake server stands in for api.openai.com.
				"models.json": `{"providers":{"openai":{"baseUrl":"` + srv.URL + `"}}}`,
			}
			for name, data := range files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.login {
				// A made-up credential, valid for an hour: never refreshed.
				if err := config.SetOAuth("openai", auth.Credential{Access: "fake-access", Refresh: "fake-refresh", Expires: time.Now().Add(time.Hour).UnixMilli()}); err != nil {
					t.Fatal(err)
				}
			} else {
				t.Setenv("OPENAI_API_KEY", "sk-fake")
			}

			s := New("test", t.TempDir())
			t.Cleanup(s.Close)
			h := &harness{t: t, s: s, ev: make(chan Notification, 10000)}
			h.c = h.connect()
			var th ThreadInfo
			if err := h.c.Call(context.Background(), "thread/start", map[string]any{"model": "openai/" + tc.model}, &th); err != nil {
				t.Fatal(err)
			}
			h.id = th.ID
			h.call("turn/start", map[string]any{"input": "hello"})
			h.completed()
			if want := map[bool]string{true: "Bearer fake-access", false: "Bearer sk-fake"}[tc.login]; bearer != want {
				t.Fatalf("authorization %q", bearer)
			}

			thread, _ := h.s.thread(h.id)
			if err := thread.call(func() error {
				for id, text := range tc.want {
					n := thread.statusTree(id)
					if n == nil || strings.TrimSpace(ui.PlainText(*n)) != text {
						got := ""
						if n != nil {
							got = ui.PlainText(*n)
						}
						t.Errorf("%s: %q, want %q", id, got, text)
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

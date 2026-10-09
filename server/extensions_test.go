//go:build !noext

package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A thread runs the extensions: its context lists them, their events
// fire, and their notices reach the client as extension/notify.
func TestThreadExtensions(t *testing.T) {
	var mu sync.Mutex
	var lasts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		lasts = append(lasts, body.Messages[len(body.Messages)-1].Content)
		mu.Unlock()
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	t.Setenv("ATTO_DIR", dir)
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	os.WriteFile(filepath.Join(dir, "models.json"), []byte(`{"providers":{"fake":{"baseUrl":"`+srv.URL+`","models":[{"id":"m","contextWindow":10000}]}}}`), 0o644)
	os.MkdirAll(filepath.Join(dir, "extensions"), 0o755)
	os.WriteFile(filepath.Join(dir, "extensions", "note.ts"), []byte(`export default (atto: any) => {
  atto.on("session_start", (e: any, ctx: any) => ctx.ui.notify("started " + e.reason + " " + ctx.hasUI));
  atto.on("user_prompt", () => "From the extension.");
}`), 0o644)
	s := New("test", t.TempDir())
	t.Cleanup(s.Close)
	notified := make(chan map[string]any, 4)
	s.Notify = func(method string, params map[string]any) {
		if method == "extension/notify" {
			notified <- params
		}
	}

	th := call(t, s, "thread/start", map[string]any{"model": "fake/m"})
	ctx, _ := th["context"].(map[string]any)
	exts, _ := ctx["extensions"].([]any)
	if len(exts) != 3 || exts[0].(map[string]any)["status"] != "loaded" || exts[2].(map[string]any)["name"] != "diff" {
		t.Fatalf("context %v", ctx["extensions"])
	}
	select {
	case n := <-notified:
		if n["extension"] != "note" || n["message"] != "started startup false" || n["threadId"] != th["threadId"] {
			t.Fatalf("%v", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no extension/notify")
	}
	call(t, s, "turn/start", map[string]any{"threadId": th["threadId"], "input": "hello"})
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		mu.Lock()
		n := len(lasts)
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no request")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(lasts[0], "hello\n\nFrom the extension.") {
		t.Fatalf("%q", lasts[0])
	}
}

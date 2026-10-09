//go:build !noext

package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// plain is v as JSON with keys in order and nothing escaped, to compare.
func plain(v any) string {
	raw, _ := json.Marshal(v)
	var m any
	json.Unmarshal(raw, &m)
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(m)
	return strings.TrimSpace(b.String())
}

// What extensions show reaches the clients as data: statuses and a
// replacement text on a reasoning item (item/display, and on the item in
// thread/read), a text block (an extText item), and status items and
// widgets (extension/ui, extensionUi). Blocks and text blocks are saved in
// the session, so a resumed thread has them without the extension.
func TestExtensionUI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `data: {"choices":[{"delta":{"reasoning_content":"plan"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"answer"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	t.Setenv("ATTO_DIR", dir)
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	os.WriteFile(filepath.Join(dir, "models.json"), []byte(`{"providers":{"fake":{"baseUrl":"`+srv.URL+`","models":[{"id":"m","contextWindow":10000}]}}}`), 0o644)
	os.MkdirAll(filepath.Join(dir, "extensions"), 0o755)
	os.WriteFile(filepath.Join(dir, "extensions", "show.ts"), []byte(`export default (atto: any) => {
  atto.on("session_start", (e: any, ctx: any) => {
    ctx.ui.setStatus("mode", "demo " + e.reason);
    ctx.ui.setWidget("w", ["<b>line 1</b>", "line 2"]);
  });
  atto.on("reasoning_end", (e: any, ctx: any) => {
    ctx.ui.setBlockStatus(e.blockId, "translating…");
    ctx.ui.setBlockDisplay(e.blockId, "PLAN");
    ctx.ui.setBlockStatus(e.blockId, "done");
    ctx.ui.setBlockStatus("nope", "ignored");
  });
  atto.on("message_end", (e: any, ctx: any) => ctx.ui.showText("report", "+a\n-b", {lang: "diff", preview: 1}));
}`), 0o644)

	type note struct {
		method string
		params map[string]any
	}
	var mu sync.Mutex
	var notes []note
	s := New("test", t.TempDir())
	s.Notify = func(method string, params map[string]any) {
		switch method {
		case "item/display", "extension/ui", "item/completed":
			var p map[string]any
			raw, _ := json.Marshal(params)
			json.Unmarshal(raw, &p)
			mu.Lock()
			notes = append(notes, note{method, p})
			mu.Unlock()
		}
	}
	th := call(t, s, "thread/start", map[string]any{"model": "fake/m"})
	id := th["threadId"].(string)
	call(t, s, "turn/start", map[string]any{"threadId": id, "input": "hello"})

	// what is shown, from a thread/read result
	type shown struct{ reasoning, display, ext, ui string }
	read := func(r map[string]any) shown {
		var out shown
		items, _ := r["items"].([]any)
		for _, x := range items {
			it := x.(map[string]any)
			switch it["type"] {
			case ItemReasoning:
				out.reasoning, out.display = fmt.Sprint(it["blockId"]), plain(it["display"])
			case ItemExtText:
				out.ext = fmt.Sprint(it["title"], "|", it["ext"], "|", it["text"], "|", it["lang"], "|", it["preview"])
			}
		}
		out.ui = plain(r["extensionUi"])
		return out
	}
	const (
		wantDisplay = `{"ext":"show","statuses":[{"ext":"show","text":"done"}],"text":"PLAN"}`
		wantExt     = "report|show|+a\n-b|diff|1"
		wantUI      = `{"status":[{"key":"show/mode","text":"demo startup"}],"widgets":[{"key":"show/w","lines":["<b>line 1</b>","line 2"]}]}`
	)
	var got shown
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		got = read(call(t, s, "thread/read", map[string]any{"threadId": id}))
		if got.display == wantDisplay && got.ext == wantExt && got.ui == wantUI {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("thread/read shows %q", []string{got.display, got.ext, got.ui})
		}
	}
	if !strings.HasPrefix(got.reasoning, id+".") || !strings.HasSuffix(got.reasoning, ":reasoning") {
		t.Errorf("blockId %q", got.reasoning)
	}

	// The notifications: the last item/display has it all; the statuses
	// came one at a time, and the unknown block was ignored.
	mu.Lock()
	var displays []string
	var ext, ui int
	for _, n := range notes {
		switch n.method {
		case "item/display":
			if n.params["itemId"] != id+"-i2" || n.params["blockId"] != got.reasoning {
				t.Errorf("item/display %v", n.params)
			}
			displays = append(displays, plain(n.params["display"]))
		case "item/completed":
			if it, _ := n.params["item"].(map[string]any); it["type"] == ItemExtText && it["title"] == "report" {
				ext++
			}
		case "extension/ui":
			if plain(n.params["ui"]) == wantUI {
				ui++
			}
		}
	}
	mu.Unlock()
	if len(displays) != 3 || displays[2] != wantDisplay || ext != 1 || ui != 1 {
		t.Errorf("displays %q, extText %d, ui %d", displays, ext, ui)
	}

	// Resumed by another server: the session has the display and the text.
	s.Close()
	s2 := New("test", t.TempDir())
	t.Cleanup(s2.Close)
	r := read(call(t, s2, "thread/resume", map[string]any{"threadId": id}))
	if r.display != wantDisplay || r.ext != wantExt || r.reasoning != got.reasoning {
		t.Errorf("resumed: %+v", r)
	}
}

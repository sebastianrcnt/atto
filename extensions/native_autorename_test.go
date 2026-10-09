package extensions

import (
	"encoding/json"
	"fmt"
	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestAutorenameDoesNotTriggerAutomatically(t *testing.T) {
	_, cwd := env(t)
	h := newHost(true)
	m := load(t, cwd, h)
	m.SetSession(savedSession(t, cwd))
	m.SessionStart("startup")
	m.TurnStart("hello")
	m.TurnEnd(nil)
	if len(h.snapshot().notices) != 0 {
		t.Fatal("autorename fired without a command")
	}
	if !m.RunCommand("autorename", "") {
		t.Fatal("no native command")
	}
	eventually(t, "no model warning", func() bool { return len(h.snapshot().notices) > 0 })
	if got := h.snapshot().notices[0]; got != "warning autorename: autorename: no model to ask" {
		t.Fatal(got)
	}
}
func TestCleanNameAndMessageClipping(t *testing.T) {
	for _, tc := range [][2]string{{"Title: **Fix parser crash.**\nnoise", "Fix parser crash"}, {" NAME: “한글 제목”. ", "한글 제목"}, {"### `Title.`", "Title"}, {"", ""}, {strings.Repeat("a", 90), strings.Repeat("a", 80)}} {
		if got := cleanName(tc[0]); got != tc[1] {
			t.Errorf("%q: %q", tc[0], got)
		}
	}
	if got := clipNameMessage(strings.Repeat("界", 601), 600); got != strings.Repeat("界", 600)+"…" {
		t.Fatal("user clip")
	}
	if got := clipNameMessage(strings.Repeat("😀", 151), 300); got != strings.Repeat("😀", 150)+"…" {
		t.Fatal("UTF-16 assistant clip")
	}
}

func TestAutorenameRespectsDisabledSettings(t *testing.T) {
	_, cwd := env(t)
	write(t, config.SettingsPath(), `{"extensions":{"disabled":["autorename"]}}`)
	m := load(t, cwd, newHost(true))
	if m.RunCommand("autorename", "") {
		t.Fatal("disabled native command ran")
	}
}

func TestAutorenameRetriesWithoutEffort(t *testing.T) {
	_, cwd := env(t)
	var requests []map[string]any
	var mu sync.Mutex
	side := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		requests = append(requests, body)
		mu.Unlock()
		if body["reasoning_effort"] != nil {
			http.Error(w, "effort not supported", 422)
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Title: **Fix   parser crash.**\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer side.Close()
	sideModels(t, side.URL)
	ag := agent.New(config.ModelRef{ProviderName: "s", Provider: config.Provider{BaseURL: side.URL}, Model: config.Model{ID: "m", ContextWindow: 100000}}, "", cwd)
	h := newHost(true)
	m := Load(Options{Cwd: cwd, Host: h, Agent: ag})
	defer m.Close()
	m.SetSession(savedSession(t, cwd))
	m.RunCommand("autorename", "")
	eventually(t, "fallback notification", func() bool { return len(h.snapshot().notices) > 0 })
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.names) != 1 || h.names[0] != "Fix parser crash" {
		t.Fatalf("names %q", h.names)
	}
	if !strings.Contains(h.notices[0], `Named this conversation "Fix   parser crash" (by s/m).`) {
		t.Fatal(h.notices)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 || requests[0]["reasoning_effort"] != "none" || requests[1]["reasoning_effort"] != nil {
		t.Fatalf("requests %+v", requests)
	}
	for _, req := range requests {
		if req["max_tokens"] != float64(64) {
			t.Errorf("budget %+v", req)
		}
	}
}

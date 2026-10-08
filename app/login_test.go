package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/auth"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/tui"
)

// A fresh catalog cache, so nothing downloads models.dev.
const loginCatalog = `{
"openai":{"models":{"gpt-5.2":{"id":"gpt-5.2","name":"GPT-5.2","tool_call":true,"reasoning":true,"limit":{"context":400000,"output":128000}}}},
"opencode-go":{"models":{"glm-5":{"id":"glm-5","name":"GLM-5","tool_call":true,"limit":{"context":1000,"output":100}}}}}`

// noModelApp is a first run: no credentials, no models.json.
func noModelApp(t *testing.T) *App {
	dir := t.TempDir()
	t.Setenv("ATTO_DIR", dir)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENCODE_API_KEY", "")
	os.MkdirAll(filepath.Join(dir, "cache"), 0o755)
	os.WriteFile(filepath.Join(dir, "cache", "catalog.json"), []byte(loginCatalog), 0o644)
	models, err := config.LoadModels()
	if err != nil || len(models.List()) != 0 {
		t.Fatalf("expected no models: %v %v", models.List(), err)
	}
	a := &App{ui: tui.New(nullTerm{}), models: models, agent: agent.New(config.ModelRef{}, "", t.TempDir()),
		tools: map[string]*toolBlock{}, quit: make(chan struct{})}
	a.login = loginHooks{
		openURL:  func(string) error { return nil },
		copyText: func(string) error { return nil },
		deviceID: func() (string, error) { return "00000000-0000-4000-8000-000000000000", nil },
	}
	a.build()
	return a
}

func screen(a *App) string {
	var out string
	a.ui.Do(func() {
		lines := a.ui.Body.Render(80)
		if a.modal != nil {
			lines = append(lines, a.modal.Render(80)...)
		}
		out = tui.StripEscapes(strings.Join(lines, "\n"))
	})
	return out
}

func typeText(a *App, s string) {
	for _, r := range s {
		a.ui.Do(func() { a.modal.HandleInput(string(r)) })
	}
}

func key(a *App, k string) { a.ui.Do(func() { a.modal.HandleInput(k) }) }

func TestFirstRunWithoutModels(t *testing.T) {
	a := noModelApp(t)
	if !strings.Contains(screen(a), "no model (/login)") {
		t.Fatalf("header: %s", screen(a))
	}
	a.submit("hello there", nil)
	if out := screen(a); !strings.Contains(out, "No models available. Use /login") || a.turns.Busy {
		t.Fatalf("hint not shown or turn started: %s", out)
	}
	if a.editor.Text() != "hello there" {
		t.Fatalf("prompt lost: %q", a.editor.Text())
	}
	a.editor.SetText("")
	a.submit("/model", nil)
	if a.modal != nil || !strings.Contains(screen(a), "No models available") {
		t.Fatal("/model without models should hint, not open an empty picker")
	}
}

func TestLoginWithAPIKey(t *testing.T) {
	a := noModelApp(t)
	a.submit("/login", nil)
	if out := screen(a); !strings.Contains(out, "Sign in with an account") || !strings.Contains(out, "Sign in with an API key") {
		t.Fatalf("auth method picker: %s", out)
	}
	key(a, "\x1b[B") // down
	key(a, "\r")
	if out := screen(a); !strings.Contains(out, "OpenCode Go API key") || strings.Contains(out, "ChatGPT") {
		t.Fatalf("api key providers: %s", out)
	}
	typeText(a, "go api")
	key(a, "\r")
	if out := screen(a); !strings.Contains(out, "Log in to OpenCode Go API key") || !strings.Contains(out, "API key:") {
		t.Fatalf("dialog: %s", out)
	}
	typeText(a, "sk-secret-123")
	if out := screen(a); strings.Contains(out, "sk-secret") || !strings.Contains(out, "•••••") {
		t.Fatalf("key not masked: %s", out)
	}
	key(a, "\r")
	stored, _ := config.LoadAuth()
	if e := stored["opencode-go"]; e.Type != "api_key" || e.Key != "sk-secret-123" {
		t.Fatalf("auth.json %+v", stored)
	}
	// No model before: the picker opens with the new provider's models.
	out := screen(a)
	if !strings.Contains(out, "Saved API key for OpenCode Go API key. 1 models available") || !strings.Contains(out, "GLM-5") {
		t.Fatalf("after login: %s", out)
	}
	key(a, "\r")
	if m := a.model(); m.ProviderName != "opencode-go" || m.Model.ID != "glm-5" || m.APIKey != "sk-secret-123" {
		t.Fatalf("model %+v", m)
	}

	// /logout removes it again.
	a.submit("/logout", nil)
	key(a, "\r")
	if stored, _ := config.LoadAuth(); len(stored) != 0 || !strings.Contains(screen(a), "Logged out of opencode-go") {
		t.Fatalf("logout: %v %s", stored, screen(a))
	}
}

func TestLoginWithChatGPT(t *testing.T) {
	a := noModelApp(t)
	var forms []url.Values
	var mu sync.Mutex
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		mu.Lock()
		forms = append(forms, r.PostForm)
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"access_token": "acc", "refresh_token": "ref", "expires_in": 3600,
			"id_token": "idt", "scope": "openid chatgpt.tokens.use.direct"})
	}))
	defer tokens.Close()
	old := auth.NewChatGPTClient
	auth.NewChatGPTClient = func(id string) *auth.ChatGPT {
		c := auth.New(id)
		c.AuthorizeURL, c.TokenURL, c.ListenAddr = "http://auth.invalid/authorize", tokens.URL, "127.0.0.1:0"
		return c
	}
	defer func() { auth.NewChatGPTClient = old }()
	urls := make(chan string, 1)
	a.login.openURL = func(u string) error { urls <- u; return nil }

	a.submit("/login openai", nil)
	var authURL string
	select {
	case authURL = <-urls:
	case <-time.After(5 * time.Second):
		t.Fatal("no authorization URL")
	}
	u, _ := url.Parse(authURL)
	q := u.Query()
	waitFor(t, func() bool { return strings.Contains(screen(a), "auth.invalid/authorize") })
	if out := screen(a); !strings.Contains(out, "paste the final") || !strings.Contains(out, "Redirect URL:") {
		t.Fatalf("dialog: %s", out)
	}
	// Paste the redirect, as when the browser runs on another machine.
	a.ui.Do(func() {
		a.modal.HandleInput(tui.PastePrefix + q.Get("redirect_uri") + "?code=the-code&client_id=issued&state=" + q.Get("state"))
	})
	key(a, "\r")
	waitFor(t, func() bool { return strings.Contains(screen(a), "Logged in to OpenAI") })
	stored, _ := config.LoadAuth()
	if e := stored["openai"]; e.Type != "oauth" || e.Access != "acc" || e.ClientID != "issued" {
		t.Fatalf("auth.json %+v", stored)
	}
	mu.Lock()
	if len(forms) != 1 || forms[0].Get("code") != "the-code" {
		t.Fatalf("token exchange %v", forms)
	}
	mu.Unlock()
	if out := screen(a); !strings.Contains(out, "GPT-5.2") {
		t.Fatalf("model picker after login: %s", out)
	}
}

func TestLoginCancel(t *testing.T) {
	a := noModelApp(t)
	old := auth.NewChatGPTClient
	auth.NewChatGPTClient = func(id string) *auth.ChatGPT {
		c := auth.New(id)
		c.AuthorizeURL, c.ListenAddr = "http://auth.invalid/authorize", "127.0.0.1:0"
		return c
	}
	defer func() { auth.NewChatGPTClient = old }()
	a.submit("/login openai", nil)
	waitFor(t, func() bool { return strings.Contains(screen(a), "auth.invalid") })
	key(a, "\x1b")
	if a.modal != nil || !strings.Contains(screen(a), "Login cancelled.") {
		t.Fatalf("esc: %s", screen(a))
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

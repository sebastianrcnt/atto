package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/auth"
	"github.com/sebastianrcnt/atto/config"
)

func TestAuthenticationRPC(t *testing.T) {
	h := newHarness(t)
	if _, err := h.try(h.c, "auth/login", map[string]any{"provider": "unknown", "apiKey": "key"}); err == nil {
		t.Fatal("unknown provider accepted")
	}
	if _, err := h.try(h.c, "auth/login", map[string]any{"provider": "fake", "apiKey": ""}); err == nil {
		t.Fatal("empty key accepted")
	}
	h.call("auth/login", map[string]any{"provider": "fake", "apiKey": "secret-api-key"})
	stored, err := config.LoadAuth()
	if err != nil || stored["fake"].Key != "secret-api-key" {
		t.Fatalf("stored key: %v / %v", stored, err)
	}
	out := h.call("auth/list", nil)
	data, _ := json.Marshal(out)
	if strings.Contains(string(data), "secret-api-key") {
		t.Fatal("auth/list exposed credentials")
	}
	if removed := h.call("auth/logout", map[string]any{"provider": "fake"})["removed"]; removed != true {
		t.Fatalf("logout: %v", removed)
	}
	stored, _ = config.LoadAuth()
	if _, ok := stored["fake"]; ok {
		t.Fatal("logout left the credential")
	}
}

func TestOAuthUsesRuntimePrompts(t *testing.T) {
	h := newHarness(t)
	old := loginProvider
	t.Cleanup(func() { loginProvider = old })
	loginProvider = func(id string) *auth.OAuthProvider {
		return &auth.OAuthProvider{ID: id, Login: func(ctx context.Context, ui auth.UI, device string) (auth.Credential, error) {
			ui.ShowURL("https://login.example.test/")
			text, err := ui.ReadPasted()
			if err != nil {
				return auth.Credential{}, err
			}
			return auth.Credential{Access: "secret-access", Refresh: text}, nil
		}}
	}
	h.call("auth/login", map[string]any{"provider": "openai", "oauth": true})
	prompt := h.wait("prompt/open", nil)["prompt"].(map[string]any)
	other := h.connect()
	if _, err := h.try(other, "prompt/answer", map[string]any{"id": prompt["id"], "text": "secret-refresh"}); err != nil {
		t.Fatal(err)
	}
	h.wait("auth/updated", func(p map[string]any) bool { return p["status"] == "completed" })
	stored, err := config.LoadAuth()
	if err != nil || stored["openai"].Access != "secret-access" || stored["openai"].Refresh != "secret-refresh" {
		t.Fatalf("OAuth credential: %v / %v", stored, err)
	}
	if _, err := h.try(h.c, "prompt/answer", map[string]any{"id": prompt["id"], "text": "second"}); err == nil {
		t.Fatal("another client's answer did not withdraw prompt")
	}
	h.call("auth/login", map[string]any{"provider": "openai", "oauth": true})
	h.wait("prompt/open", nil)
	h.call("auth/cancel", nil)
	h.wait("auth/updated", func(p map[string]any) bool { return p["status"] == "cancelled" })
	if p := h.call("thread/read", nil)["prompt"]; p != nil {
		t.Fatalf("cancelled login retained prompt: %v", p)
	}
}

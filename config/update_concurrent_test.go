package config

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/auth"
)

func writeConfigKeys(prefix string, count int) error {
	for i := range count {
		key := fmt.Sprintf("%s-%d", prefix, i)
		if err := SetAPIKey(key, key); err != nil {
			return err
		}
		if err := UpdateSettings(map[string]any{key: key}); err != nil {
			return err
		}
	}
	return nil
}

func checkConfigKeys(t *testing.T, count int) {
	t.Helper()
	entries, err := LoadAuth()
	if err != nil || len(entries) != count {
		t.Fatalf("auth: %d entries, %v", len(entries), err)
	}
	data, err := os.ReadFile(SettingsPath())
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil || len(raw) != count {
		t.Fatalf("settings: %d entries, %v", len(raw), err)
	}
}

func TestConfigUpdateGoroutines(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			if err := writeConfigKeys(fmt.Sprint(i), 4); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	checkConfigKeys(t, 128)
}

func TestConfigUpdateProcess(t *testing.T) {
	if prefix := os.Getenv("ATTO_TEST_CONFIG_WRITER"); prefix != "" {
		if err := writeConfigKeys(prefix, 8); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Setenv("ATTO_DIR", t.TempDir())
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			cmd := exec.Command(exe, "-test.run=^TestConfigUpdateProcess$")
			cmd.Env = append(os.Environ(), "ATTO_TEST_CONFIG_WRITER="+fmt.Sprint(i))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("child: %v\n%s", err, out)
			}
		})
	}
	wg.Wait()
	checkConfigKeys(t, 64)
}

func TestOAuthConcurrentRefresh(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("refresh_token") != "old" {
			t.Errorf("unexpected refresh token %q", r.Form.Get("refresh_token"))
		}
		time.Sleep(20 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "fresh", "refresh_token": "rotated", "expires_in": 3600, "scope": "openid chatgpt.tokens.use.direct"})
	}))
	defer srv.Close()
	old := auth.NewChatGPTClient
	auth.NewChatGPTClient = func(string) *auth.ChatGPT { return &auth.ChatGPT{TokenURL: srv.URL} }
	t.Cleanup(func() { auth.NewChatGPTClient = old })
	if err := SetOAuth("openai", auth.Credential{Access: "stale", Refresh: "old", Expires: 1, ClientID: "client"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			token, err := OAuthToken(context.Background(), "openai")
			if err != nil || token != "fresh" {
				t.Errorf("token: %q, %v", token, err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("refreshes: %d", calls.Load())
	}
	entries, err := LoadAuth()
	if err != nil || entries["openai"].Refresh != "rotated" {
		t.Fatalf("rotated token: %v, %v", entries, err)
	}
}

func TestOAuthCrossProcessRefresh(t *testing.T) {
	if url := os.Getenv("ATTO_TEST_REFRESH_URL"); url != "" {
		auth.NewChatGPTClient = func(string) *auth.ChatGPT { return &auth.ChatGPT{TokenURL: url} }
		token, err := OAuthToken(context.Background(), "openai")
		if err != nil || token != "fresh" {
			t.Fatalf("child token: %q, %v", token, err)
		}
		return
	}
	t.Setenv("ATTO_DIR", t.TempDir())
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if got := r.Form.Get("refresh_token"); got != "old" {
			t.Errorf("unexpected token %q", got)
		}
		time.Sleep(100 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "fresh", "refresh_token": "rotated", "expires_in": 3600, "scope": "openid chatgpt.tokens.use.direct"})
	}))
	defer srv.Close()
	if err := SetOAuth("openai", auth.Credential{Access: "stale", Refresh: "old", Expires: 1, ClientID: "client"}); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			cmd := exec.Command(exe, "-test.run=^TestOAuthCrossProcessRefresh$")
			cmd.Env = append(os.Environ(), "ATTO_TEST_REFRESH_URL="+srv.URL)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("child: %v\n%s", err, out)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("cross-process refreshes: %d", calls.Load())
	}
	entries, err := LoadAuth()
	if err != nil || entries["openai"].Refresh != "rotated" {
		t.Fatalf("rotated token: %v, %v", entries, err)
	}
}

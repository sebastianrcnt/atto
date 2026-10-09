package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/auth"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/session"
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

func TestDesktopDiagnostics(t *testing.T) {
	h := newHarness(t)
	if got := h.call("thread/statusLine", nil)["configured"]; got != false {
		t.Fatalf("unconfigured status: %v", got)
	}
	if err := config.UpdateSettings(map[string]any{"statusLine": config.StatusLine{Command: "echo desktop-status", RefreshInterval: 3}}); err != nil {
		t.Fatal(err)
	}
	out := h.call("thread/statusLine", nil)
	lines := out["lines"].([]any)
	if len(lines) != 1 || strings.TrimSpace(lines[0].(string)) != "desktop-status" || out["refreshInterval"] != float64(3) {
		t.Fatalf("custom status: %v", out)
	}
	profiles := h.call("thread/debug", nil)
	if heap, err := base64.StdEncoding.DecodeString(profiles["heap"].(string)); err != nil || len(heap) == 0 {
		t.Fatalf("heap profile: %v", err)
	}
	if profiles["goroutines"] == "" || profiles["memory"] == nil {
		t.Fatal("incomplete debug profiles")
	}
	writer := &boundedOutput{limit: 4}
	_, _ = writer.Write([]byte("abc"))
	if writer.truncated {
		t.Fatal("short status incorrectly marked truncated")
	}
	_, _ = writer.Write([]byte("defgh"))
	if writer.String() != "abcd" || !writer.truncated {
		t.Fatalf("bounded output: %q / %v", writer.String(), writer.truncated)
	}
}

func TestArchiveRPC(t *testing.T) {
	h := newHarness(t)
	h.call("input/submit", map[string]any{"input": "save before archiving"})
	h.completed()
	old, err := session.Find(h.id)
	if err != nil {
		t.Fatal(err)
	}
	out := h.call("thread/archive", nil)
	path := out["path"].(string)
	if !strings.HasPrefix(path, config.ArchivedDir()+string(filepath.Separator)) {
		t.Fatalf("archive path: %q", path)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("active file remains: %v", err)
	}
	if _, _, err := session.Load(path); err != nil {
		t.Fatal(err)
	}
	if h.s.Loaded(h.id) {
		t.Fatal("archive retained runtime/writer")
	}
}

func TestListIncludesLiveEmptySessions(t *testing.T) {
	h := newHarness(t)
	h.call("thread/setName", map[string]any{"name": "Empty workspace"})
	value, err := h.s.listThreads(threadParams{})
	if err != nil {
		t.Fatal(err)
	}
	rows := value.(map[string]any)["threads"].([]map[string]any)
	found := false
	for _, row := range rows {
		if row["threadId"] == h.id {
			found = true
			if row["name"] != "Empty workspace" || row["loaded"] != true || row["busy"] != false {
				t.Fatalf("live metadata: %#v", row)
			}
		}
	}
	if !found {
		t.Fatalf("empty live session missing: %#v", rows)
	}
	value, err = h.s.listThreads(threadParams{Cwd: t.TempDir()})
	if err != nil || len(value.(map[string]any)["threads"].([]map[string]any)) != 0 {
		t.Fatalf("cwd filter: %#v %v", value, err)
	}
}

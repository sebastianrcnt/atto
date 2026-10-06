package config

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/auth"
)

const openaiFixture = `{
"openai":{"models":{
 "gpt-5.2":{"id":"gpt-5.2","name":"GPT-5.2","tool_call":true,"reasoning":true,"limit":{"context":400000,"input":272000,"output":128000}},
 "gpt-4.1":{"id":"gpt-4.1","tool_call":true,"reasoning":false,"limit":{"context":1000000,"output":32768}},
 "text-embedding-3-small":{"id":"text-embedding-3-small","tool_call":false,"limit":{"context":8000,"output":1}}
}},
"opencode":{"models":{
 "gpt-5.1":{"id":"gpt-5.1","tool_call":true,"reasoning":true,"limit":{"context":400000,"output":128000},"provider":{"npm":"@ai-sdk/openai"}},
 "claude-x":{"id":"claude-x","tool_call":true,"reasoning":true,"limit":{"context":1000,"output":100},"provider":{"npm":"@ai-sdk/anthropic"}},
 "grok-4.7":{"id":"grok-4.7","tool_call":true,"reasoning":true,"limit":{"context":1000,"output":100},"provider":{"npm":"@ai-sdk/openai"}},
 "muse-spark-1.3":{"id":"muse-spark-1.3","tool_call":true,"reasoning":true,"limit":{"context":1000,"output":100},"provider":{"npm":"@ai-sdk/openai"},"reasoning_options":[{"type":"effort","values":["none","minimal","low","medium","high","xhigh"]}]},
 "glm-5":{"id":"glm-5","tool_call":true,"reasoning":false,"limit":{"context":1000,"output":100}}
}}}`

func setupDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ATTO_DIR", dir)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENCODE_API_KEY", "")
	os.MkdirAll(filepath.Join(dir, "cache"), 0o755)
	os.WriteFile(filepath.Join(dir, "cache", "catalog.json"), []byte(openaiFixture), 0o644)
}

func TestOpenAIPreset(t *testing.T) {
	setupDir(t)
	if m, _ := LoadModels(); len(m.Providers) != 0 {
		t.Fatalf("shown without credentials: %v", m.Providers)
	}

	// An API key from the environment shows the provider, no OAuth involved.
	t.Setenv("OPENAI_API_KEY", "sk-test")
	m, _ := LoadModels()
	ref, ok := m.Find("openai", "gpt-5.2")
	if !ok || ref.API() != "openai-responses" || ref.KeyFunc != nil || ref.APIKey != "sk-test" {
		t.Fatalf("api key ref %+v", ref)
	}
	if _, ok := m.Find("openai", "gpt-4.1"); !ok {
		t.Fatal("non-reasoning model missing")
	}
	if _, ok := m.Find("openai", "text-embedding-3-small"); ok {
		t.Fatal("tool-less model listed")
	}
	if got := strings.Join(ref.Model.Levels(), ","); got != "off,low,medium,high,xhigh" || *ref.Model.EffortMap["off"] != "none" {
		t.Fatalf("gpt-5.2 efforts %s %v", got, ref.Model.EffortMap)
	}
	if p := ref.Provider; p.BaseURL != "https://api.openai.com/v1" || len(p.Headers) != 0 {
		t.Fatalf("provider %+v", p)
	}
	if g, _ := m.Find("openai", "gpt-4.1"); len(g.Model.Levels()) != 0 {
		t.Fatalf("gpt-4.1 efforts %v", g.Model.Levels())
	}
}

func TestOpenAIPresetWithOAuth(t *testing.T) {
	setupDir(t)
	err := SetOAuth("openai", auth.Credential{Access: "acc", Refresh: "ref", Expires: time.Now().Add(time.Hour).UnixMilli(), ClientID: "cid"})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := LoadModels()
	ref, ok := m.Find("openai", "gpt-5.2")
	if !ok || ref.KeyFunc == nil {
		t.Fatalf("oauth ref %+v", ref)
	}
	tok, err := ref.KeyFunc(context.Background())
	if err != nil || tok != "acc" {
		t.Fatalf("valid token not reused: %q %v", tok, err)
	}
	// As in pi, the stored login outranks a models.json apiKey, which in
	// turn outranks the environment.
	t.Setenv("OPENAI_KEY_X", "sk-explicit")
	m.Providers["openai"] = func() Provider { p := m.Providers["openai"]; p.APIKey = "$OPENAI_KEY_X"; return p }()
	if r, _ := m.Find("openai", "gpt-5.2"); r.KeyFunc == nil {
		t.Fatalf("stored login lost to apiKey: %+v", r)
	}
	RemoveAuth("openai")
	m.auth, _ = LoadAuth()
	if r, _ := m.Find("openai", "gpt-5.2"); r.KeyFunc != nil || r.APIKey != "sk-explicit" {
		t.Fatalf("explicit key: %+v", r)
	}
}

func TestZenGPTUsesResponses(t *testing.T) {
	setupDir(t)
	t.Setenv("OPENCODE_API_KEY", "k")
	m, _ := LoadModels()
	gpt, ok := m.Find("opencode", "gpt-5.1")
	if !ok || gpt.API() != "openai-responses" || gpt.Provider.API != "openai-completions" || gpt.Provider.BaseURL != "https://opencode.ai/zen/v1" {
		t.Fatalf("zen gpt %+v", gpt)
	}
	if len(gpt.Provider.Headers) != 0 || gpt.Model.Compat == nil || gpt.Model.Compat.SessionAffinityFormat != "openai-nosession" || strings.Join(gpt.Model.Levels(), ",") != "off,low,medium,high" {
		t.Fatalf("zen gpt headers/levels %v %v", gpt.Provider.Headers, gpt.Model.Levels())
	}
	if glm, ok := m.Find("opencode", "glm-5"); !ok || glm.API() != "openai-completions" {
		t.Fatalf("glm %+v", glm)
	}
	if _, ok := m.Find("opencode", "claude-x"); ok {
		t.Error("claude-x (Anthropic's API) should be skipped")
	}
	// Other models marked as OpenAI's (Grok, Muse Spark) speak Responses too.
	if grok, ok := m.Find("opencode", "grok-4.7"); !ok || grok.API() != "openai-responses" {
		t.Errorf("grok-4.7: %v %+v", ok, grok)
	}
	// Levels of a model atto has no rules for come from models.dev.
	if muse, ok := m.Find("opencode", "muse-spark-1.3"); !ok || strings.Join(muse.Model.Levels(), ",") != "off,minimal,low,medium,high,xhigh" {
		t.Errorf("muse levels: %v", muse.Model.Levels())
	} else if v := muse.Model.EffortMap["off"]; v == nil || *v != "none" {
		t.Errorf("muse off: %v", v)
	}
}

func TestResponsesEfforts(t *testing.T) {
	for id, want := range map[string]string{
		"gpt-5":             "minimal,low,medium,high",
		"gpt-5-mini":        "minimal,low,medium,high",
		"gpt-5-pro":         "high",
		"gpt-5-codex":       "low,medium,high",
		"gpt-5.1":           "off,low,medium,high",
		"gpt-5.1-codex-max": "low,medium,high,xhigh",
		"gpt-5.2":           "off,low,medium,high,xhigh",
		"gpt-5.4-pro":       "medium,high,xhigh",
		"gpt-5.3-codex":     "low,medium,high,xhigh",
		"gpt-6-sol":         "off,low,medium,high,xhigh",
		"gpt-6.1-sol":       "low,medium,high,xhigh,max",
		"gpt-5.6-luna":      "off,low,medium,high,xhigh",
		"o3":                "low,medium,high",
		"gpt-4.1":           "",
	} {
		efforts, emap := responsesEfforts(id)
		m := Model{Efforts: efforts, EffortMap: emap}
		if got := strings.Join(m.Levels(), ","); got != want {
			t.Errorf("%s: %q, want %q", id, got, want)
		}
	}
	// models.json can still adjust one level.
	e, em := responsesEfforts("gpt-5.2")
	m := mergeModel(Model{Efforts: e, EffortMap: em}, Model{EffortMap: map[string]*string{"off": nil, "max": new("xhigh")}})
	if got := strings.Join(m.Levels(), ","); got != "low,medium,high,xhigh,max" {
		t.Fatalf("override: %s", got)
	}
}

func TestAuthFileMergeAndPermissions(t *testing.T) {
	setupDir(t)
	os.WriteFile(AuthPath(), []byte(`{"other":{"type":"api_key","key":"k1"},"future":{"type":"x","extra":1}}`), 0o644)
	if err := SetOAuth("openai", auth.Credential{Access: "a", Refresh: "r", Expires: 5, ClientID: "c", Scopes: []string{"s"}}); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(AuthPath()); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	raw := map[string]json.RawMessage{}
	data, _ := os.ReadFile(AuthPath())
	json.Unmarshal(data, &raw)
	if len(raw) != 3 || !strings.Contains(string(raw["future"]), `"extra"`) {
		t.Fatalf("entries lost: %s", data)
	}
	got, _ := LoadAuth()
	if e := got["openai"]; e.Type != "oauth" || e.Access != "a" || e.Refresh != "r" || e.Expires != 5 || e.ClientID != "c" || e.Secret() != "a" {
		t.Fatalf("entry %+v", e)
	}
	if got["other"].Secret() != "k1" {
		t.Fatalf("api key entry %+v", got["other"])
	}

	found, err := RemoveAuth("openai")
	if err != nil || !found {
		t.Fatal(found, err)
	}
	if found, _ = RemoveAuth("openai"); found {
		t.Fatal("removed twice")
	}
	if got, _ := LoadAuth(); len(got) != 1 { // "future" is kept on disk but not loaded
		t.Fatalf("after logout %v", got)
	}
	// No temp files left behind.
	if m, _ := filepath.Glob(filepath.Join(Dir(), ".auth*tmp*")); len(m) != 0 {
		t.Fatalf("leftovers %v", m)
	}
}

func TestOAuthTokenRefreshesAndPersists(t *testing.T) {
	setupDir(t)
	var gotForm map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotForm = r.PostForm
		json.NewEncoder(w).Encode(map[string]any{"access_token": "fresh", "refresh_token": "ref2", "expires_in": 3600,
			"scope": "openid chatgpt.tokens.use.direct"})
	}))
	defer srv.Close()
	old := auth.NewChatGPTClient
	auth.NewChatGPTClient = func(string) *auth.ChatGPT { return &auth.ChatGPT{TokenURL: srv.URL} }
	defer func() { auth.NewChatGPTClient = old }()

	SetOAuth("openai", auth.Credential{Access: "stale", Refresh: "ref1", Expires: time.Now().Add(-time.Minute).UnixMilli(), ClientID: "cid"})
	tok, err := OAuthToken(context.Background(), "openai")
	if err != nil || tok != "fresh" {
		t.Fatalf("%q %v", tok, err)
	}
	if gotForm["refresh_token"][0] != "ref1" || gotForm["client_id"][0] != "cid" {
		t.Fatalf("form %v", gotForm)
	}
	e := func() AuthEntry { a, _ := LoadAuth(); return a["openai"] }()
	if e.Access != "fresh" || e.Refresh != "ref2" || e.ClientID != "cid" || e.Expires <= time.Now().UnixMilli() {
		t.Fatalf("not persisted: %+v", e)
	}
	// Second call reuses the stored token without hitting the server.
	srv.Close()
	if tok, err := OAuthToken(context.Background(), "openai"); err != nil || tok != "fresh" {
		t.Fatalf("%q %v", tok, err)
	}
	RemoveAuth("openai")
	if _, err := OAuthToken(context.Background(), "openai"); err == nil || !strings.Contains(err.Error(), "atto login") {
		t.Fatalf("logged out: %v", err)
	}
}

func TestDeviceIDStable(t *testing.T) {
	setupDir(t)
	a, err := DeviceID()
	b, _ := DeviceID()
	if err != nil || len(a) != 36 || a != b {
		t.Fatalf("%q %q %v", a, b, err)
	}
}

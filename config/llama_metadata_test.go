package config

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/ai"
)

func loadMetadataModels(t *testing.T, p Provider) ModelsFile {
	t.Helper()
	data, err := json.Marshal(ModelsFile{Providers: map[string]Provider{"local": p}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ModelsPath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	models, err := LoadModels()
	if err != nil {
		t.Fatal(err)
	}
	return models
}

func TestLlamaMetadataLoaded(t *testing.T) {
	setupDir(t)
	const id = "org/model + &"
	var modelsCalls, propsCalls atomic.Int32
	bodies := make(chan map[string]any, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer local-key" || r.Header.Get("X-Team") != "core" || r.Header.Get("X-Session") != "" {
			t.Errorf("headers: %v", r.Header)
		}
		switch r.URL.Path {
		case "/proxy/models":
			modelsCalls.Add(1)
			fmt.Fprintf(w, `{"data":[{"id":"other","status":{"value":"loaded"},"meta":{"n_ctx":999999}},{"id":%q,"status":{"value":"loaded"},"meta":{"n_ctx":16384}}]}`, id)
		case "/proxy/props":
			propsCalls.Add(1)
			if r.URL.Query().Get("model") != id || r.URL.Query().Get("autoload") != "false" {
				t.Errorf("props query: %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"default_generation_settings":{"n_ctx":8192},"chat_template":"{% if enable_thinking %}...{% endif %}"}`)
		case "/proxy/v1/chat/completions":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			bodies <- body
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := Provider{BaseURL: srv.URL + "/proxy/v1/", LlamaCppMetadata: true, APIKey: "local-key",
		Headers: map[string]string{"X-Team": "core", "X-Session": "$session"},
		Models:  []Model{{ID: id, MaxTokens: 512}}}
	models := loadMetadataModels(t, p)
	for _, level := range []string{"off", "on"} {
		ref, ok := models.Find("local", id)
		if !ok || ref.Model.ContextWindow != 8192 || strings.Join(ref.Model.Levels(), ",") != "off,on" {
			t.Fatalf("runtime model: %+v", ref.Model)
		}
		am := ref.AIModel()
		compat := ai.GetCompletionsCompat(&am)
		if am.ContextWindow != 8192 || !am.Reasoning || compat.ThinkingFormat != "chat-template" || compat.SupportsReasoningEffort || compat.SupportsDeveloperRole || compat.SupportsStore || compat.MaxTokensField != "max_tokens" {
			t.Fatalf("runtime compat: %+v", compat)
		}
		stream := ai.StreamSimple(&am, ai.Context{Messages: []ai.Message{&ai.UserMessage{Role: "user", Text: "hi"}}},
			&ai.SimpleStreamOptions{APIKey: ref.APIKey, Headers: map[string]string{"X-Team": "core"}, Reasoning: level})
		for range stream.All() {
		}
		if result := stream.Result(); result.StopReason != ai.StopStop {
			t.Fatalf("stream: %+v", result)
		}
		body := <-bodies
		kwargs, ok := body["chat_template_kwargs"].(map[string]any)
		if !ok || kwargs["enable_thinking"] != (level == "on") || body["reasoning_effort"] != nil {
			t.Fatalf("thinking request: %v", body)
		}
	}
	if modelsCalls.Load() != 1 || propsCalls.Load() != 1 || len(models.MetadataDiagnostics()) != 0 {
		t.Fatalf("probe not cached: models=%d props=%d notes=%v", modelsCalls.Load(), propsCalls.Load(), models.MetadataDiagnostics())
	}
	// Side calls reload the config, but share the short-lived probe cache.
	loadMetadataModels(t, p)
	if modelsCalls.Load() != 1 || propsCalls.Load() != 1 {
		t.Fatal("config reload bypassed the probe cache")
	}
}

func TestLlamaMetadataNotLoaded(t *testing.T) {
	for _, status := range []string{"sleeping", "unloaded", "loading", "unknown", ""} {
		t.Run(status, func(t *testing.T) {
			setupDir(t)
			var modelsCalls, propsCalls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/models" {
					propsCalls.Add(1)
					http.NotFound(w, r)
					return
				}
				modelsCalls.Add(1)
				fmt.Fprintf(w, `{"data":[{"id":"m","status":{"value":%q},"meta":{"n_ctx":4096}},{"id":"no-runtime","status":{"value":%q}}]}`, status, status)
			}))
			defer srv.Close()
			models := loadMetadataModels(t, Provider{BaseURL: srv.URL + "/v1", LlamaCppMetadata: true,
				Compat: &ai.Compat{}, Models: []Model{{ID: "m"}, {ID: "no-runtime"}}})
			ref, _ := models.Find("local", "m")
			fallback, _ := models.Find("local", "no-runtime")
			if ref.Model.ContextWindow != 4096 || len(ref.Model.Levels()) != 0 || fallback.Model.ContextWindow != 128000 {
				t.Fatalf("cached metadata/fallback: %+v %+v", ref.Model, fallback.Model)
			}
			if modelsCalls.Load() != 1 || propsCalls.Load() != 0 {
				t.Fatalf("inspection woke model: models=%d props=%d", modelsCalls.Load(), propsCalls.Load())
			}
		})
	}
}

func TestLlamaMetadataFallback(t *testing.T) {
	for _, failure := range []string{"models-http", "models-json", "timeout", "props-http", "props-json", "missing-model", "invalid-context"} {
		t.Run(failure, func(t *testing.T) {
			setupDir(t)
			var propsCalls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/models" {
					switch failure {
					case "models-http":
						http.Error(w, "unavailable", 503)
					case "models-json":
						fmt.Fprint(w, "invalid json")
					case "timeout":
						<-r.Context().Done()
					case "missing-model":
						fmt.Fprint(w, `{"data":[]}`)
					case "invalid-context":
						fmt.Fprint(w, `{"data":[{"id":"m","status":"loaded","meta":{"n_ctx":-1,"n_ctx_train":999999}}]}`)
					default:
						fmt.Fprint(w, `{"data":[{"id":"m","status":"loaded","meta":{"n_ctx":4096}}]}`)
					}
					return
				}
				propsCalls.Add(1)
				switch failure {
				case "props-json":
					fmt.Fprint(w, "invalid json")
				case "invalid-context":
					fmt.Fprint(w, `{"default_generation_settings":{"n_ctx":0},"chat_template":"no thinking control"}`)
				default:
					http.Error(w, "unavailable", 503)
				}
			}))
			defer srv.Close()
			start := time.Now()
			models := loadMetadataModels(t, Provider{BaseURL: srv.URL + "/v1", LlamaCppMetadata: true,
				Compat: &ai.Compat{}, Models: []Model{{ID: "m", Efforts: []string{"off", "high"}, MaxTokens: 1234}}})
			if time.Since(start) > 4*time.Second {
				t.Fatal("probe exceeded short timeout")
			}
			ref, _ := models.Find("local", "m")
			wantContext, wantProps, wantNotes := 128000, int32(0), 1
			if strings.HasPrefix(failure, "props-") {
				wantContext, wantProps = 4096, 1
			}
			if failure == "missing-model" || failure == "invalid-context" {
				wantNotes = 0
			}
			if failure == "invalid-context" {
				wantProps = 1
			}
			if ref.Model.ContextWindow != wantContext || ref.Model.MaxTokens != 1234 || strings.Join(ref.Model.Levels(), ",") != "off,high" || ref.AIModel().Compat.ThinkingFormat != "" {
				t.Fatalf("fallback: %+v", ref.Model)
			}
			if propsCalls.Load() != wantProps || len(models.MetadataDiagnostics()) != wantNotes {
				t.Fatalf("props=%d notes=%v", propsCalls.Load(), models.MetadataDiagnostics())
			}
		})
	}
}

func TestLlamaMetadataExplicitOverrides(t *testing.T) {
	for _, source := range []string{"model", "override", "catalog-default", "catalog-override"} {
		t.Run(source, func(t *testing.T) {
			setupDir(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/models" {
					fmt.Fprint(w, `{"data":[{"id":"m","status":"loaded","meta":{"n_ctx":4096}},{"id":"gpt-5.2","status":"loaded","meta":{"n_ctx":4096}}]}`)
				} else {
					fmt.Fprint(w, `{"chat_template":"enable_thinking","default_generation_settings":{"n_ctx":4096}}`)
				}
			}))
			defer srv.Close()
			p := Provider{BaseURL: srv.URL + "/v1", LlamaCppMetadata: true, Models: []Model{{ID: "m", Reasoning: new(false)}}}
			want := 2048
			switch source {
			case "model":
				want = 128000
				p.Models[0].ContextWindow = want
			case "override":
				p.Models[0].ContextWindow = 8192
				p.ModelOverrides = map[string]ModelOverride{"m": {ContextWindow: want}}
			default:
				// Catalog context is a fallback, not an explicit user limit.
				p.API = "openai-completions"
				p.Models = []Model{{ID: "gpt-5.2", Reasoning: new(false)}}
				if source == "catalog-default" {
					want = 4096
				} else {
					p.ModelOverrides = map[string]ModelOverride{"gpt-5.2": {ContextWindow: want}}
				}
			}
			var models ModelsFile
			if strings.HasPrefix(source, "catalog-") {
				data, _ := json.Marshal(ModelsFile{Providers: map[string]Provider{"openai": p}})
				if err := os.WriteFile(ModelsPath(), data, 0o600); err != nil {
					t.Fatal(err)
				}
				var err error
				models, err = LoadModels()
				if err != nil {
					t.Fatal(err)
				}
			} else {
				models = loadMetadataModels(t, p)
			}
			ref, ok := models.Find("", p.Models[0].ID)
			if !ok || ref.Model.ContextWindow != want || ref.AIModel().ContextWindow != want || ref.AIModel().Reasoning || ref.AIModel().Compat.ThinkingFormat != "" {
				t.Fatalf("explicit overrides lost: %+v", ref)
			}
		})
	}
}

func TestLlamaMetadataThinkingOverrides(t *testing.T) {
	setupDir(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			fmt.Fprint(w, `{"data":[{"id":"m","status":"loaded"}]}`)
		} else {
			fmt.Fprint(w, `{"chat_template_tool_use":"enable_thinking"}`)
		}
	}))
	defer srv.Close()
	for _, format := range []string{"", "qwen"} {
		compat := &ai.Compat{ThinkingFormat: format, ChatTemplateKwargs: map[string]any{"enable_thinking": false, "custom": "value"}}
		models := loadMetadataModels(t, Provider{BaseURL: srv.URL + "/v1", LlamaCppMetadata: true, Compat: compat,
			Models: []Model{{ID: "m", Reasoning: new(true), Efforts: []string{"off", "high"}}}})
		ref, _ := models.Find("local", "m")
		am := ref.AIModel()
		wantFormat := format
		if format == "" {
			wantFormat = "chat-template"
		}
		if am.Compat.ThinkingFormat != wantFormat || !reflect.DeepEqual(am.Compat.ChatTemplateKwargs, compat.ChatTemplateKwargs) || strings.Join(ref.Model.Levels(), ",") != "off,high" {
			t.Fatalf("thinking overrides lost: %+v", am)
		}
	}
}

func TestLlamaMetadataDisabled(t *testing.T) {
	setupDir(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "must not be requested", 500)
	}))
	defer srv.Close()
	for _, enabled := range []bool{false, true} {
		p := Provider{BaseURL: srv.URL + "/v1", LlamaCppMetadata: enabled,
			Models: []Model{{ID: "m", ContextWindow: 12345, MaxTokens: 512}}}
		if enabled {
			// Even opt-in providers do not inspect non-chat-completions models.
			p.API = "openai-responses"
		}
		models := loadMetadataModels(t, p)
		for range 3 {
			ref, _ := models.Find("local", "m")
			if ref.Model.ContextWindow != 12345 || ref.AIModel().Reasoning || len(models.MetadataDiagnostics()) != 0 {
				t.Fatalf("disabled probe changed model: %+v", ref.Model)
			}
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("disabled probe made %d requests", calls.Load())
	}
}

func TestLlamaMetadataCache(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if fail {
					http.Error(w, "unavailable", 503)
				} else {
					fmt.Fprint(w, `{"n_ctx":4096}`)
				}
			}))
			defer srv.Close()
			endpoint := srv.URL + "/models"
			results := make(chan error, 8)
			for range 8 {
				go func() {
					var out map[string]any
					results <- llamaMetadataGET(srv.Client(), endpoint, nil, &out)
				}()
			}
			for range 8 {
				if err := <-results; (err != nil) != fail {
					t.Errorf("cached error: %v", err)
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("concurrent probes: %d", calls.Load())
			}
			// Credentials form part of the key; a changed key must not reuse
			// another login's success (or its authorization failure).
			var out map[string]any
			llamaMetadataGET(srv.Client(), endpoint, map[string]string{"Authorization": "Bearer other"}, &out)
			if calls.Load() != 2 {
				t.Fatal("credentials reused another probe result")
			}
			llamaMetadataCache.Lock()
			llamaMetadataCache.entries[endpoint+"\nnull"].expires = time.Now().Add(-time.Second)
			llamaMetadataCache.Unlock()
			llamaMetadataGET(srv.Client(), endpoint, nil, &out)
			if calls.Load() != 3 {
				t.Fatal("expired probe was not refreshed")
			}
		})
	}
}

func TestLlamaMetadataModelEndpoint(t *testing.T) {
	setupDir(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/prefix/models" || r.Header.Get("X-Team") != "model" || r.Header.Get("Authorization") != "custom" {
			t.Errorf("model endpoint/headers: %s %v", r.URL, r.Header)
		}
		fmt.Fprint(w, `{"data":[{"id":"m","status":"sleeping","meta":{"n_ctx":4096}}]}`)
	}))
	defer srv.Close()
	models := loadMetadataModels(t, Provider{BaseURL: srv.URL + "/wrong/v1", APIKey: "key", LlamaCppMetadata: true,
		Headers: map[string]string{"X-Team": "provider"},
		Models:  []Model{{ID: "m", BaseURL: srv.URL + "/prefix/v1", Headers: map[string]string{"x-team": "model", "authorization": "custom"}}}})
	ref, _ := models.Find("local", "m")
	if calls.Load() != 1 || ref.Model.ContextWindow != 4096 {
		t.Fatalf("model-specific metadata: %+v (%d calls)", ref.Model, calls.Load())
	}
}

package model

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestCompleteRequest(t *testing.T) {
	want := `{"model":"test-model","messages":[{"role":"user","content":"input"}],"tools":[{"type":"function","function":{"name":"lua","description":"compute","parameters":{"type":"object"}}}]}`
	var expected map[string]any
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("headers: %v", r.Header)
		}
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		if !reflect.DeepEqual(got, expected) {
			t.Errorf("body: %#v", got)
		}
		if _, err := fmt.Fprint(w, `{"choices":[{"message":{"role":"ignored","content":"answer","reasoning_content":"thinking"}}],"usage":{"prompt_tokens":12,"completion_tokens":3}}`); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	client := Client{BaseURL: server.URL + "/v1/", Model: "test-model", APIKey: "test-key", HTTP: server.Client()}
	tools := []Tool{{Name: "lua", Description: "compute", Parameters: map[string]any{"type": "object"}}}
	message, usage, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "input"}}, tools)
	if err != nil || message.Role != "assistant" || message.Content != "answer" || message.Reasoning != "thinking" || usage != (Usage{12, 3}) {
		t.Fatalf("reply: %+v %+v %v", message, usage, err)
	}
}

func TestCompleteErrors(t *testing.T) {
	for _, test := range []struct {
		name, body, want string
		status           int
	}{
		{"status", "  rejected\n", "model: 503 Service Unavailable: rejected", http.StatusServiceUnavailable},
		{"json", "invalid", "model: invalid character", http.StatusOK},
		{"choices", `{"choices":[]}`, `model: no choices in {"choices":[]}`, http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				if _, err := fmt.Fprint(w, test.body); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			client := Client{BaseURL: server.URL}
			message, usage, err := client.Complete(context.Background(), nil, nil)
			if err == nil || !strings.HasPrefix(err.Error(), test.want) || message.Role != "" || usage != (Usage{}) {
				t.Fatalf("reply: %+v %+v %v", message, usage, err)
			}
		})
	}
}

func TestRequestWithoutToolsOrKey(t *testing.T) {
	client := Client{BaseURL: "http://example.invalid/v1"}
	request, err := client.request(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer request.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["tools"]; ok || request.Header.Get("Authorization") != "" {
		t.Fatalf("body: %v; headers: %v", body, request.Header)
	}
}

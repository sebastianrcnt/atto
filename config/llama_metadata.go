package config

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	llamaMetadataTimeout = 2 * time.Second
	llamaMetadataTTL     = 5 * time.Minute
)

type llamaMetadataResult struct {
	once    sync.Once
	expires time.Time
	body    []byte
	err     error
}

var llamaMetadataCache = struct {
	sync.Mutex
	entries map[string]*llamaMetadataResult
}{entries: map[string]*llamaMetadataResult{}}

// MetadataDiagnostics reports non-fatal probe failures for /debug. Results,
// including failures, stay in ModelsFile until models.json is loaded again.
func (m ModelsFile) MetadataDiagnostics() []string {
	return slices.Clone(m.metadataDiagnostics)
}

type llamaModelMetadata struct {
	ID     string          `json:"id"`
	Status json.RawMessage `json:"status"`
	NCtx   int             `json:"n_ctx"`
	Meta   struct {
		NCtx int `json:"n_ctx"`
	} `json:"meta"`
}

func (m llamaModelMetadata) loaded() bool {
	var status string
	if json.Unmarshal(m.Status, &status) == nil {
		return status == "loaded"
	}
	var object struct {
		Value string `json:"value"`
	}
	return json.Unmarshal(m.Status, &object) == nil && object.Value == "loaded"
}

type llamaProps struct {
	ChatTemplate              string `json:"chat_template"`
	ChatTemplateToolUse       string `json:"chat_template_tool_use"`
	DefaultGenerationSettings struct {
		NCtx int `json:"n_ctx"`
	} `json:"default_generation_settings"`
}

// probeLlamaCpp resolves metadata once at config load, so context accounting
// and the wire model see the same limits. Only positive runtime limits replace
// defaults; an explicit models.json contextWindow always wins.
func (m ModelsFile) probeLlamaCpp(name string, p, user Provider) (Provider, []string) {
	var notes []string
	client := &http.Client{
		Timeout: llamaMetadataTimeout,
		// Do not let a redirected inspection turn into a different request.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	key, _ := p.resolveKey(name, m.auth)
	for i, model := range p.Models {
		ref := ModelRef{ProviderName: name, Provider: p, Model: model}
		if ref.API() != "openai-completions" {
			continue
		}
		base := model.BaseURL
		if base == "" {
			base = p.BaseURL
		}
		base = strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1")
		headers := map[string]string{}
		if key != "" {
			headers["Authorization"] = "Bearer " + key
		}
		for _, set := range []map[string]string{ref.RequestHeaders(), resolveHeaders(model.Headers)} {
			for k, v := range set {
				if v != "$session" {
					headers[http.CanonicalHeaderKey(k)] = v
				}
			}
		}
		var listing struct {
			Data []llamaModelMetadata `json:"data"`
		}
		err := llamaMetadataGET(client, base+"/models", headers, &listing)
		note := func(endpoint string) {
			notes = append(notes, fmt.Sprintf("llama.cpp metadata %q/%q: %s probe failed; using configured values for unavailable metadata", name, model.ID, endpoint))
		}
		if err != nil {
			note("/models")
			continue
		}
		for _, runtime := range listing.Data {
			if runtime.ID != model.ID {
				continue
			}
			nctx := runtime.Meta.NCtx
			if nctx <= 0 {
				nctx = runtime.NCtx
			}
			if runtime.loaded() {
				var props llamaProps
				query := url.Values{"model": {model.ID}, "autoload": {"false"}}
				if err := llamaMetadataGET(client, base+"/props?"+query.Encode(), headers, &props); err != nil {
					note("/props")
				} else {
					if props.DefaultGenerationSettings.NCtx > 0 {
						nctx = props.DefaultGenerationSettings.NCtx
					}
					model.runtimeThinking = strings.Contains(props.ChatTemplate, "enable_thinking") || strings.Contains(props.ChatTemplateToolUse, "enable_thinking")
				}
			}
			explicitContext := user.ModelOverrides[model.ID].ContextWindow > 0
			for _, configured := range user.Models {
				if configured.ID == model.ID && configured.ContextWindow > 0 {
					explicitContext = true
				}
			}
			if nctx > 0 && !explicitContext {
				model.ContextWindow = nctx
			}
			p.Models[i] = model
			break
		}
	}
	return p, notes
}

// llamaMetadataGET shares both successes and failures across config loads
// (extension side calls load models.json too). Concurrent loads share one probe.
func llamaMetadataGET(client *http.Client, endpoint string, headers map[string]string, out any) error {
	hb, _ := json.Marshal(headers)
	cacheKey := endpoint + "\n" + string(hb)
	llamaMetadataCache.Lock()
	now := time.Now()
	for key, result := range llamaMetadataCache.entries {
		if now.After(result.expires) {
			delete(llamaMetadataCache.entries, key)
		}
	}
	result := llamaMetadataCache.entries[cacheKey]
	if result == nil {
		result = &llamaMetadataResult{expires: now.Add(llamaMetadataTTL)}
		llamaMetadataCache.entries[cacheKey] = result
	}
	llamaMetadataCache.Unlock()
	result.once.Do(func() { result.body, result.err = fetchLlamaMetadata(client, endpoint, headers) })
	if result.err != nil {
		return result.err
	}
	return json.Unmarshal(result.body, out)
}

func fetchLlamaMetadata(client *http.Client, endpoint string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		if v != "$session" {
			req.Header.Set(k, v)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	const maxBody = 4 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if len(body) > maxBody {
		return nil, fmt.Errorf("metadata exceeds %d bytes", maxBody)
	}
	return body, err
}

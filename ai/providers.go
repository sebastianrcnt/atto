package ai

// Ported from pi (https://github.com/earendil-works/pi), Copyright (c) 2025
// Mario Zechner, MIT License; see THIRD_PARTY_NOTICES.

import (
	"maps"
	"strings"
)

// Ports of the provider factories atto uses from src/providers/:
// openai.ts, openai-codex.ts, opencode.ts, opencode-go.ts and
// opencode-headers.ts. pi's factories also carry generated model catalogs
// and auth methods; in atto the catalog comes from models.dev
// (config/catalog.go) and auth from config/auth.go, so what remains here
// is each provider's identity and its request wrappers.

// BuiltinProvider describes a provider pi ships.
type BuiltinProvider struct {
	ID      string
	Name    string
	BaseURL string
	// EnvVars hold an API key; OAuth names the login (auth package).
	EnvVars []string
	OAuth   string
	Apis    []Api
}

var BuiltinProviders = []BuiltinProvider{
	{ID: "openai", Name: "OpenAI", BaseURL: "https://api.openai.com/v1", EnvVars: []string{"OPENAI_API_KEY"}, OAuth: "openai-chatgpt", Apis: []Api{ApiOpenAIResponses}},
	{ID: "openai-codex", Name: "OpenAI Codex (legacy)", BaseURL: "https://chatgpt.com/backend-api", OAuth: "openai-codex", Apis: []Api{ApiOpenAICodexResponses}},
	{ID: "opencode", Name: "OpenCode Zen", BaseURL: "https://opencode.ai/zen/v1", EnvVars: []string{"OPENCODE_API_KEY"}, Apis: []Api{ApiOpenAICompletions, ApiOpenAIResponses}},
	{ID: "opencode-go", Name: "OpenCode Go", BaseURL: "https://opencode.ai/zen/go/v1", EnvVars: []string{"OPENCODE_API_KEY"}, Apis: []Api{ApiOpenAICompletions, ApiOpenAIResponses}},
}

// GetBuiltinProvider looks a built-in provider up by id.
func GetBuiltinProvider(id string) *BuiltinProvider {
	for i := range BuiltinProviders {
		if BuiltinProviders[i].ID == id {
			return &BuiltinProviders[i]
		}
	}
	return nil
}

// builtinProviderWrappers wrap API streams per provider, as pi's provider
// factories do (withOpenCodeSessionHeader).
var builtinProviderWrappers = map[string]func(ProviderStreams) ProviderStreams{
	"opencode":    WithOpenCodeSessionHeader,
	"opencode-go": WithOpenCodeSessionHeader,
}

const openCodeSessionHeader = "x-opencode-session"

func hasHeaderName(headers map[string]string, name string) bool {
	for k := range headers {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}

func withSessionHeader(o *StreamOptions) {
	if o == nil || o.SessionID == "" || hasHeaderName(o.Headers, openCodeSessionHeader) {
		return
	}
	h := make(map[string]string, len(o.Headers)+1)
	maps.Copy(h, o.Headers)
	h[openCodeSessionHeader] = o.SessionID
	o.Headers = h
}

// WithOpenCodeSessionHeader adds OpenCode's per-conversation routing header.
func WithOpenCodeSessionHeader(s ProviderStreams) ProviderStreams {
	return ProviderStreams{
		Stream: func(model *Model, context TranscriptContext, options any) *AssistantMessageEventStream {
			if so := streamOptionsOf(options); so != nil {
				withSessionHeader(so)
			}
			return s.Stream(model, context, options)
		},
		StreamSimple: func(model *Model, context TranscriptContext, options *SimpleStreamOptions) *AssistantMessageEventStream {
			if options != nil {
				withSessionHeader(&options.StreamOptions)
			}
			return s.StreamSimple(model, context, options)
		},
	}
}

// --- src/api/openai-prompt-cache.ts ---

const OpenAIPromptCacheKeyMaxLength = 64

// ClampOpenAIPromptCacheKey keeps at most 64 characters (code points).
func ClampOpenAIPromptCacheKey(key string) string {
	r := []rune(key)
	if len(r) <= OpenAIPromptCacheKeyMaxLength {
		return key
	}
	return string(r[:OpenAIPromptCacheKeyMaxLength])
}

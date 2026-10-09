package config

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/ai"
	"github.com/sebastianrcnt/atto/fsutil"
)

// Built-in providers come from the models.dev catalog (as pi does). Only
// the providers listed here are kept; the subset is cached in
// ~/.atto/cache/catalog.json and refreshed daily.

const (
	catalogURL = "https://models.dev/api.json"
	catalogTTL = 24 * time.Hour
)

// catalogProvider describes how to turn a models.dev provider into ours.
type catalogProvider struct {
	name    string
	display string
	baseURL string
	api     string // wire API of the provider's models ("" = chat completions)
	env     []string
	headers map[string]string
	// source is the models.dev provider to read ("" = name).
	source string
	// only keeps models whose ID has this prefix.
	only string
	// subscription marks a flat-rate plan: models.dev's per-token prices
	// then only estimate what the usage would cost over the API.
	subscription bool
}

func (cp catalogProvider) sourceName() string {
	if cp.source != "" {
		return cp.source
	}
	return cp.name
}

// OpenCode Zen and Go: OpenAI-compatible chat completions with API key auth
// (pi: providers/opencode.ts, opencode-go.ts); package ai adds their
// per-conversation routing header. Zen's GPT models are served over the
// Responses API instead.
//
// OpenAI itself uses the Responses API with an API key or Sign in with
// ChatGPT (/login); package ai sends the session_id routing headers that
// keep turns of one conversation on the same cache.
var catalogProviders = []catalogProvider{
	{
		name: "openai", display: "OpenAI", baseURL: "https://api.openai.com/v1", api: "openai-responses",
		env: []string{"OPENAI_API_KEY"},
	},
	{
		// pi: providers/openai-codex.ts. The ChatGPT backend serves the GPT-5
		// family to Plus/Pro subscribers logged in with /login (no API key).
		name: "openai-codex", display: "OpenAI Codex (ChatGPT Plus/Pro)", baseURL: "https://chatgpt.com/backend-api",
		api: "openai-codex-responses", env: []string{}, source: "openai", only: "gpt-5",
		subscription: true,
	},
	{
		name: "opencode", display: "OpenCode Zen", baseURL: "https://opencode.ai/zen/v1",
		env: []string{"OPENCODE_API_KEY"},
	},
	{
		name: "opencode-go", display: "OpenCode Go", baseURL: "https://opencode.ai/zen/go/v1",
		env: []string{"OPENCODE_API_KEY"}, subscription: true,
	},
}

// modelsDevModel is the subset of a models.dev model entry we use.
type modelsDevModel struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ToolCall  bool   `json:"tool_call"`
	Reasoning bool   `json:"reasoning"`
	Status    string `json:"status"`
	// Modalities lists e.g. input ["text","image","pdf"].
	Modalities struct {
		Input []string `json:"input"`
	} `json:"modalities"`
	Limit struct {
		Context int `json:"context"`
		Input   int `json:"input"`
		Output  int `json:"output"`
	} `json:"limit"`
	Provider *struct {
		NPM string `json:"npm"`
	} `json:"provider"`
	// ReasoningOptions lists the reasoning controls a model takes, e.g.
	// {"type":"effort","values":["minimal","low","medium","high","xhigh"]}.
	ReasoningOptions []struct {
		Type   string   `json:"type"`
		Values []string `json:"values"`
	} `json:"reasoning_options"`
	Cost *modelsDevCost `json:"cost"`
}

// modelsDevCost is a model's price in US dollars per million tokens, as
// ai.ModelCost has it. Tiers apply above a context size; entries without
// them may list only context_over_200k.
type modelsDevCost struct {
	modelsDevRates
	Tiers []struct {
		modelsDevRates
		Tier struct {
			Type string `json:"type"`
			Size int    `json:"size"`
		} `json:"tier"`
	} `json:"tiers"`
	Over200k *modelsDevRates `json:"context_over_200k"`
}

type modelsDevRates struct {
	Input      *float64 `json:"input"`
	Output     *float64 `json:"output"`
	CacheRead  *float64 `json:"cache_read"`
	CacheWrite *float64 `json:"cache_write"`
}

// over returns base with the prices r lists.
func (r modelsDevRates) over(base ai.ModelCostRates) ai.ModelCostRates {
	set := func(dst *float64, v *float64) {
		if v != nil {
			*dst = *v
		}
	}
	set(&base.Input, r.Input)
	set(&base.Output, r.Output)
	set(&base.CacheRead, r.CacheRead)
	set(&base.CacheWrite, r.CacheWrite)
	return base
}

// modelCost converts the prices; a tier keeps the base prices it does not
// list.
func (c *modelsDevCost) modelCost() *ai.ModelCost {
	if c == nil {
		return nil
	}
	out := &ai.ModelCost{ModelCostRates: c.over(ai.ModelCostRates{})}
	for _, t := range c.Tiers {
		if t.Tier.Type == "context" && t.Tier.Size > 0 {
			out.Tiers = append(out.Tiers, ai.ModelCostTier{InputTokensAbove: t.Tier.Size, ModelCostRates: t.over(out.ModelCostRates)})
		}
	}
	if len(c.Tiers) == 0 && c.Over200k != nil {
		out.Tiers = []ai.ModelCostTier{{InputTokensAbove: 200000, ModelCostRates: c.Over200k.over(out.ModelCostRates)}}
	}
	return out
}

type modelsDevProvider struct {
	Models map[string]modelsDevModel `json:"models"`
}

func catalogPath() string { return filepath.Join(Dir(), "cache", "catalog.json") }

// CatalogStale reports whether the cached catalog is missing or old.
func CatalogStale() bool {
	st, err := os.Stat(catalogPath())
	return err != nil || time.Since(st.ModTime()) > catalogTTL
}

// RefreshCatalog downloads models.dev and caches the providers we use.
func RefreshCatalog(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", catalogURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("models.dev: %s", resp.Status)
	}
	var all map[string]json.RawMessage
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&all); err != nil {
		return err
	}
	subset := map[string]json.RawMessage{}
	for _, cp := range catalogProviders {
		if raw, ok := all[cp.sourceName()]; ok {
			subset[cp.sourceName()] = raw
		}
	}
	data, err := json.Marshal(subset)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(catalogPath()), 0o755); err != nil {
		return err
	}
	return fsutil.WriteAtomic(catalogPath(), data, 0o644)
}

// CatalogProviders builds providers from the cached catalog (none if the
// cache is missing).
func CatalogProviders() map[string]Provider {
	out := map[string]Provider{}
	data, err := fsutil.ReadFile(catalogPath())
	if err != nil {
		return out
	}
	var cached map[string]modelsDevProvider
	if json.Unmarshal(data, &cached) != nil {
		return out
	}
	for _, cp := range catalogProviders {
		src, ok := cached[cp.sourceName()]
		if !ok {
			continue
		}
		p := Provider{
			Name: cp.display, BaseURL: cp.baseURL, API: cp.api,
			Env: cp.env, Headers: cp.headers, MaxTokensField: "max_tokens",
			Subscription: cp.subscription,
		}
		if p.API == "" {
			p.API = "openai-completions"
		}
		for id, m := range src.Models {
			if !strings.HasPrefix(id, cp.only) {
				continue
			}
			if mod, ok := catalogModel(cp, id, m); ok {
				p.Models = append(p.Models, mod)
			}
		}
		sort.Slice(p.Models, func(i, j int) bool { return p.Models[i].ID < p.Models[j].ID })
		out[cp.name] = p
	}
	return out
}

// PriceTierNotice reports missing price-tier information only for a priced
// model absent from the cached catalog, or when that catalog cannot be read.
// Catalogued flat-price models and explicit cost.tiers need no warning.
func PriceTierNotice(m ModelRef) string {
	c := m.Model.Cost
	if c == nil || len(c.Tiers) > 0 || c.Input <= 0 && c.Output <= 0 && c.CacheRead <= 0 && c.CacheWrite <= 0 {
		return ""
	}
	data, err := fsutil.ReadFile(catalogPath())
	var cached map[string]modelsDevProvider
	reason := "the model is not in the cached models.dev catalog"
	if err != nil || json.Unmarshal(data, &cached) != nil || cached == nil {
		reason = "the models.dev catalog could not be loaded"
	} else {
		source := m.ProviderName
		for _, cp := range catalogProviders {
			if cp.name == source {
				source = cp.sourceName()
				break
			}
		}
		if _, ok := cached[source].Models[m.Model.ID]; ok {
			return ""
		}
	}
	return fmt.Sprintf("No price-tier cap for %s/%s: %s; no tier data known (set cost.tiers in models.json if needed).", m.ProviderName, m.Model.ID, reason)
}

// Default output cap: models.dev lists maximums (up to 512k) that would
// leave little room for context; most turns need far less.
const catalogMaxTokens = 32768

// catalogModel converts a models.dev entry. Models are kept when served over
// chat completions or the Responses API; Claude and Gemini models on
// OpenCode use the Anthropic and Google APIs, which atto does not speak.
func catalogModel(cp catalogProvider, id string, m modelsDevModel) (Model, bool) {
	provider := cp.name
	if !m.ToolCall || m.Status == "deprecated" {
		return Model{}, false
	}
	responses := cp.api == "openai-responses" || cp.api == "openai-codex-responses"
	for _, no := range []string{"realtime", "audio", "image", "transcribe", "tts"} {
		if responses && strings.Contains(id, no) {
			return Model{}, false // not usable through text Responses requests
		}
	}
	if m.Provider != nil {
		switch npm := m.Provider.NPM; {
		case npm == "" || npm == "@ai-sdk/openai-compatible":
		case npm == "@ai-sdk/openai":
			// A gateway's models marked as OpenAI's speak Responses while
			// the rest use chat completions: its GPT models, and others
			// such as OpenCode Go's Muse Spark.
			responses = true
		default:
			return Model{}, false
		}
	}
	ctx := m.Limit.Context
	if m.Limit.Input > 0 {
		ctx = m.Limit.Input
	}
	mod := Model{
		ID: id, Name: m.Name, ContextWindow: ctx,
		MaxTokens: min(max(m.Limit.Output, 0), catalogMaxTokens),
		Cost:      m.Cost.modelCost(),
	}
	for _, in := range m.Modalities.Input {
		if in == "text" || in == "image" { // the ones atto can send
			mod.Input = append(mod.Input, in)
		}
	}
	if mod.MaxTokens == 0 {
		mod.MaxTokens = catalogMaxTokens
	}
	if responses && cp.api == "" {
		mod.API = "openai-responses"
		// pi: OpenCode's Responses proxy takes x-client-request-id only.
		mod.Compat = &ai.Compat{SessionAffinityFormat: "openai-nosession"}
	}
	if !m.Reasoning {
		return mod, true
	}
	if responses {
		mod.Efforts, mod.EffortMap = responsesEfforts(id)
		if mod.Efforts == nil {
			// Not an OpenAI model atto knows the levels of (Muse Spark,
			// Grok on OpenCode): the levels models.dev lists.
			mod.Efforts, mod.EffortMap = listedEfforts(m)
		}
		return mod, true
	}
	// Default: OpenAI-style reasoning_effort. Reasoning models on OpenCode
	// think by default, so "off" must be sent explicitly as "none"
	// (omitting the field still thinks).
	mod.Efforts = []string{"off", "low", "medium", "high"}
	mod.EffortMap = map[string]*string{"off": new("none")}
	mod.ExtraBody = map[string]any{"reasoning_effort": "$effort"}
	if strings.HasPrefix(id, "kimi-k2.6") {
		// Kimi K2.6 takes Anthropic-style thinking objects and rejects
		// reasoning_effort; thinking is only on/off.
		mod.Efforts = []string{"off", "on"}
		mod.EffortMap = map[string]*string{}
		mod.ExtraBody = map[string]any{"reasoning_effort": nil, "thinking": map[string]any{"type": "$thinkingType"}}
	}
	for _, q := range catalogEffortMaps {
		match := strings.HasPrefix(id, q.prefix)
		if exact, ok := strings.CutSuffix(q.prefix, "!"); ok {
			match = id == exact
		}
		if (q.provider == "" || q.provider == provider) && match {
			maps.Copy(mod.EffortMap, q.levels)
		}
	}
	return mod, true
}

// catalogEffortMaps are built-in effort mappings for catalog models, in
// the same form users write in models.json (null = level unsupported);
// models.json entries override them per level. A trailing "!" on the
// prefix means an exact ID match.
var catalogEffortMaps = []struct {
	provider, prefix string
	levels           map[string]*string
}{
	// pi: DeepSeek V4 exposes high and max (Flash also low).
	{"", "deepseek-v4", map[string]*string{"medium": nil, "max": new("max")}},
	{"", "deepseek-v4-pro", map[string]*string{"low": nil}},
	// pi: OpenCode Go GLM-5.2 takes only high and max.
	{"opencode-go", "glm-5.2!", map[string]*string{"off": nil, "low": nil, "medium": nil, "max": new("max")}},
	// Verified on OpenCode Go 2026-10-05: these reject or ignore "none"
	// (thinking-only), so "off" is unavailable. GLM-5.3 Flash can turn it off.
	{"", "glm-5.3!", map[string]*string{"off": nil}},
	{"", "kimi-k2.7-code", map[string]*string{"off": nil}},
	{"", "longcat-", map[string]*string{"off": nil}},
}

var gptVersion = regexp.MustCompile(`^gpt-(\d+)(?:\.(\d+))?`)

// responsesEfforts derives reasoning levels for a Responses-API model from
// its ID, since models.dev does not list them. The rules follow OpenAI's
// documented ranges: gpt-5 has minimal..high; 5.1 added "none" (sent for
// "off"); 5.2 added xhigh; codex models never take "none"; pro models only
// the top levels. Unknown models get nothing, so no reasoning field is sent.
// Override per model with effortMap in models.json.
// listedEfforts is the effort levels models.dev lists for a model, nil if
// none. "none" becomes atto's "off", sent as "none".
func listedEfforts(m modelsDevModel) ([]string, map[string]*string) {
	for _, o := range m.ReasoningOptions {
		if o.Type != "effort" || len(o.Values) == 0 {
			continue
		}
		var levels []string
		var effortMap map[string]*string
		for _, v := range o.Values {
			if v == "none" {
				v, effortMap = "off", map[string]*string{"off": new("none")}
			}
			levels = append(levels, v)
		}
		return levels, effortMap
	}
	return nil, nil
}

func responsesEfforts(id string) ([]string, map[string]*string) {
	if len(id) > 1 && id[0] == 'o' && id[1] >= '0' && id[1] <= '9' {
		return []string{"low", "medium", "high"}, nil
	}
	v := gptVersion.FindStringSubmatch(id)
	if v == nil {
		return nil, nil
	}
	major, _ := strconv.Atoi(v[1])
	minor := -1
	if v[2] != "" {
		minor, _ = strconv.Atoi(v[2])
	}
	if major < 5 {
		return nil, nil
	}
	xhigh := major > 5 || minor >= 2 || strings.Contains(id, "-max")
	var levels []string
	switch {
	case strings.Contains(id, "-pro"):
		levels = []string{"high"}
		if minor >= 2 || major > 5 {
			levels = []string{"medium", "high", "xhigh"}
		}
		return levels, nil
	case strings.Contains(id, "codex"):
		levels = []string{"low", "medium", "high"}
	case major == 5 && minor < 0:
		levels = []string{"minimal", "low", "medium", "high"}
	case strings.Contains(id, "-sol") && (major > 6 || minor >= 1):
		// GPT-6.1 Sol rejects "none": it always thinks, up to max.
		return []string{"low", "medium", "high", "xhigh", "max"}, nil
	default:
		levels = []string{"off", "low", "medium", "high"}
	}
	if xhigh && (major > 5 || minor >= 0) {
		levels = append(levels, "xhigh")
	}
	if levels[0] == "off" {
		return levels, map[string]*string{"off": new("none")}
	}
	return levels, nil
}

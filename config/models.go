package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/sebastianrcnt/atto/ai"
)

// ModelsFile is the contents of models.json (schema ported from pi, MIT,
// Copyright (c) 2025 Mario Zechner; see THIRD_PARTY_NOTICES). The schema is pi's
// (packages/coding-agent/src/core/model-config.ts), so ~/.pi/agent/models.json
// can be copied to ~/.atto/models.json, plus atto's own fields:
//
//	{
//	  "providers": {
//	    "llama-cpp": {
//	      "baseUrl": "http://host:8081/v1",
//	      "api": "openai-completions",
//	      "apiKey": "$ENV_VAR, !command or literal (optional)",
//	      "compat": {"supportsDeveloperRole": false},                     // pi
//	      "extraBody": {"chat_template_kwargs": {"reasoning_effort": "$effort"}}, // atto
//	      "models": [{"id": "orca-local", "reasoning": true, "thinkingLevelMap": {"off": null},
//	                  "efforts": ["low","high"], "contextWindow": 262144, "maxTokens": 32768}]
//	    }
//	  }
//	}
//
// atto-only fields: provider "env", "maxTokensField", "extraBody",
// "subscription"; model
// "efforts", "extraBody". "effortMap" is read as pi's "thinkingLevelMap".
// JSON comments are allowed, as in pi.
type ModelsFile struct {
	Providers map[string]Provider `json:"providers"`
	auth      map[string]AuthEntry
	// names caches which display names more than one provider has (see
	// DisplayName); nil in a ModelsFile not made by LoadModels, which then
	// works the names out each time.
	names *sharedNames
}

// sharedNames is the set of display names that models of more than one
// provider have, worked out on first use.
type sharedNames struct {
	once sync.Once
	set  map[string]bool
}

type Provider struct {
	Name    string `json:"name,omitempty"`
	BaseURL string `json:"baseUrl,omitempty"`
	API     string `json:"api,omitempty"`
	APIKey  string `json:"apiKey,omitempty"` // config value: "$ENV_VAR", "!command" or literal
	// Headers are sent with every request. Values are config values;
	// "$session" (atto) is replaced with the session ID.
	Headers map[string]string `json:"headers,omitempty"`
	Compat  *ai.Compat        `json:"compat,omitempty"`
	// AuthHeader (pi) adds "Authorization: Bearer <key>"; the OpenAI APIs
	// always send it, so it changes nothing there.
	AuthHeader     *bool                    `json:"authHeader,omitempty"`
	OAuth          string                   `json:"oauth,omitempty"`
	Models         []Model                  `json:"models,omitempty"`
	ModelOverrides map[string]ModelOverride `json:"modelOverrides,omitempty"`

	// atto extensions.
	Env            []string       `json:"env,omitempty"` // env vars to read the key from
	MaxTokensField string         `json:"maxTokensField,omitempty"`
	ExtraBody      map[string]any `json:"extraBody,omitempty"`
	// Subscription marks a flat-rate plan (ChatGPT login, OpenCode Go):
	// the models' prices then only estimate the usage at API rates.
	Subscription bool `json:"subscription,omitempty"`
}

type Model struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// API and BaseURL override the provider's (e.g. GPT models on a
	// gateway that serves the rest over chat completions).
	API       string `json:"api,omitempty"`
	BaseURL   string `json:"baseUrl,omitempty"`
	Reasoning *bool  `json:"reasoning,omitempty"`
	// EffortMap is pi's thinkingLevelMap: each level maps to the value
	// sent, null marks a level unsupported (removed from the levels), and
	// unmapped levels are sent as-is. Read from "thinkingLevelMap" or
	// atto's older "effortMap".
	EffortMap     map[string]*string `json:"thinkingLevelMap,omitempty"`
	ContextWindow int                `json:"contextWindow,omitempty"`
	MaxTokens     int                `json:"maxTokens,omitempty"`
	// Input lists the input modalities: "text", "image". Unset means text.
	Input                         []string                  `json:"input,omitempty"`
	Cost                          *ai.ModelCost             `json:"cost,omitempty"`
	Headers                       map[string]string         `json:"headers,omitempty"`
	Compat                        *ai.Compat                `json:"compat,omitempty"`
	SamplingParams                map[string]any            `json:"samplingParams,omitempty"`
	SamplingParamsByThinkingLevel map[string]map[string]any `json:"samplingParamsByThinkingLevel,omitempty"`

	// atto extensions.

	// Efforts lists the reasoning levels in order; with it, levels are
	// exactly these (minus null-mapped ones, plus mapped extras).
	Efforts []string `json:"efforts,omitempty"`
	// ExtraBody is merged over the provider's; a null value removes a key.
	ExtraBody map[string]any `json:"extraBody,omitempty"`
}

func (m *Model) UnmarshalJSON(b []byte) error {
	type plain Model
	var v struct {
		plain
		LegacyEffortMap map[string]*string `json:"effortMap"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*m = Model(v.plain)
	if len(v.LegacyEffortMap) > 0 {
		merged := maps.Clone(v.LegacyEffortMap)
		maps.Copy(merged, m.EffortMap)
		m.EffortMap = merged
	}
	return nil
}

// ModelOverride is pi's modelOverrides entry: fields applied to a model
// the provider already has (built-in or defined above).
type ModelOverride struct {
	Name             string             `json:"name,omitempty"`
	Reasoning        *bool              `json:"reasoning,omitempty"`
	ThinkingLevelMap map[string]*string `json:"thinkingLevelMap,omitempty"`
	Input            []string           `json:"input,omitempty"`
	Cost             *struct {
		Input      *float64           `json:"input,omitempty"`
		Output     *float64           `json:"output,omitempty"`
		CacheRead  *float64           `json:"cacheRead,omitempty"`
		CacheWrite *float64           `json:"cacheWrite,omitempty"`
		Tiers      []ai.ModelCostTier `json:"tiers,omitempty"`
	} `json:"cost,omitempty"`
	ContextWindow  int               `json:"contextWindow,omitempty"`
	MaxTokens      int               `json:"maxTokens,omitempty"`
	SamplingParams map[string]any    `json:"samplingParams,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	Compat         *ai.Compat        `json:"compat,omitempty"`
	Efforts        []string          `json:"efforts,omitempty"` // atto
}

// Images reports whether the model accepts image input.
func (m Model) Images() bool { return slices.Contains(m.Input, "image") }

func (m Model) DisplayName() string {
	if m.Name != "" {
		return m.Name
	}
	return m.ID
}

// effortOrder is the canonical order for levels added through EffortMap.
var effortOrder = []string{"off", "none", "minimal", "low", "medium", "high", "xhigh", "max", "on"}

// Levels returns the available effort levels. With Efforts: those, minus
// levels mapped to null, plus mapped levels not listed. Otherwise a pi
// reasoning model gets pi's levels (off..high, plus xhigh/max when
// mapped); a model with neither has only its mapped levels, if any.
func (m Model) Levels() []string {
	if len(m.Efforts) == 0 && m.Reasoning != nil {
		if !*m.Reasoning {
			return nil
		}
		return ai.GetSupportedThinkingLevels(&ai.Model{Reasoning: true, ThinkingLevelMap: m.EffortMap})
	}
	var out []string
	seen := map[string]bool{}
	for _, l := range m.Efforts {
		if v, ok := m.EffortMap[l]; ok && v == nil {
			continue
		}
		out = append(out, l)
		seen[l] = true
	}
	for _, l := range effortOrder {
		if v, ok := m.EffortMap[l]; ok && v != nil && !seen[l] {
			out = insertOrdered(out, l)
			seen[l] = true
		}
	}
	return out
}

func insertOrdered(levels []string, l string) []string {
	rank := func(x string) int {
		if i := slices.Index(effortOrder, x); i >= 0 {
			return i
		}
		return len(effortOrder)
	}
	for i, x := range levels {
		if rank(x) > rank(l) {
			return slices.Insert(levels, i, l)
		}
	}
	return append(levels, l)
}

// piFormat reports whether the model is configured with pi's fields; atto
// then uses pi's defaults for everything left unset.
func (r ModelRef) piFormat() bool {
	return r.Model.Reasoning != nil || r.Model.Compat != nil || r.Provider.Compat != nil
}

// RequestBody merges the provider's and the model's extra body fields.
func (r ModelRef) RequestBody() map[string]any {
	out := map[string]any{}
	maps.Copy(out, r.Provider.ExtraBody)
	for k, v := range r.Model.ExtraBody {
		if v == nil {
			delete(out, k)
		} else {
			out[k] = v
		}
	}
	return out
}

// ModelRef is a model together with the provider that serves it.
type ModelRef struct {
	ProviderName string
	Provider     Provider
	Model        Model
	APIKey       string
	// KeyFunc, set for OAuth logins, returns a fresh access token per
	// request; APIKey then only marks that credentials exist.
	KeyFunc func(context.Context) (string, error)
}

// API returns the wire API for the model: its own, else the provider's,
// else chat completions.
func (r ModelRef) API() string {
	if r.Model.API != "" {
		return r.Model.API
	}
	if r.Provider.API != "" {
		return r.Provider.API
	}
	return "openai-completions"
}

// RequestHeaders are the provider's headers with config values resolved;
// "$session" stays for the client to fill in per request.
func (r ModelRef) RequestHeaders() map[string]string {
	return resolveHeaders(r.Provider.Headers)
}

func resolveHeaders(h map[string]string) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range h {
		if v == "$session" {
			out[k] = v
			continue
		}
		if rv, ok := ResolveConfigValueUncached(v, nil); ok && rv != "" {
			out[k] = rv
		}
	}
	return out
}

// AIModel converts the model to package ai's Model.
//
// atto's own models.json format predates pi's compat settings. A model
// configured without any of pi's fields (reasoning, compat) keeps the
// requests atto always sent: the system role, max_tokens, no "store", and
// effort only through extraBody placeholders. pi-format models get pi's
// auto-detected behaviour.
func (r ModelRef) AIModel() ai.Model {
	m := r.Model
	levels := m.Levels()
	am := ai.Model{
		ID: m.ID, Name: m.DisplayName(), Api: r.API(), Provider: r.ProviderName,
		BaseURL: m.BaseURL, Input: m.Input, Headers: resolveHeaders(m.Headers),
		Reasoning: len(levels) > 0, ContextWindow: m.ContextWindow, MaxTokens: m.MaxTokens,
		SamplingParams: m.SamplingParams, ExtraBody: r.RequestBody(),
	}
	if am.BaseURL == "" {
		am.BaseURL = r.Provider.BaseURL
	}
	if len(am.Input) == 0 {
		am.Input = []string{"text"}
	}
	if m.Reasoning != nil {
		am.Reasoning = *m.Reasoning
	}
	if len(m.EffortMap) > 0 {
		am.ThinkingLevelMap = ai.ThinkingLevelMap(maps.Clone(m.EffortMap))
	}
	if m.Cost != nil {
		am.Cost = *m.Cost
	}
	if len(m.SamplingParamsByThinkingLevel) > 0 {
		am.SamplingParamsByThinkingLevel = ai.SamplingParamsByThinkingLevel{}
		maps.Copy(am.SamplingParamsByThinkingLevel, m.SamplingParamsByThinkingLevel)
	}
	if len(m.Efforts) > 0 || (m.Reasoning == nil && len(levels) > 0) {
		am.Efforts = levels
	}
	am.Compat = mergeCompat(r.Provider.Compat, m.Compat)
	if am.Api == ai.ApiOpenAICompletions {
		if !r.piFormat() {
			f := false
			am.Compat = &ai.Compat{SupportsStore: &f, SupportsDeveloperRole: &f, SupportsReasoningEffort: &f}
		}
		if am.Compat == nil {
			am.Compat = &ai.Compat{}
		}
		if am.Compat.MaxTokensField == "" {
			switch {
			case r.Provider.MaxTokensField != "":
				am.Compat.MaxTokensField = r.Provider.MaxTokensField
			case !r.piFormat():
				am.Compat.MaxTokensField = "max_tokens"
			}
		}
	}
	return am
}

// mergeCompat overlays over onto base (pi: mergeCompat; nested routing and
// template maps merge per key).
func mergeCompat(base, over *ai.Compat) *ai.Compat {
	if base == nil && over == nil {
		return nil
	}
	var out ai.Compat
	if base != nil {
		out = *base
	}
	if over == nil {
		return &out
	}
	bb, _ := json.Marshal(out)
	ob, _ := json.Marshal(over)
	var bm, om map[string]any
	_ = json.Unmarshal(bb, &bm)
	_ = json.Unmarshal(ob, &om)
	for k, v := range om {
		if sub, ok := v.(map[string]any); ok && (k == "openRouterRouting" || k == "vercelGatewayRouting" || k == "chatTemplateKwargs" || k == "chatTemplateArgs") {
			if prev, ok := bm[k].(map[string]any); ok {
				maps.Copy(prev, sub)
				continue
			}
		}
		bm[k] = v
	}
	mb, _ := json.Marshal(bm)
	var merged ai.Compat
	_ = json.Unmarshal(mb, &merged)
	return &merged
}

// stripJSONComments removes // and /* */ comments outside strings (pi
// allows them in models.json).
func stripJSONComments(b []byte) []byte {
	var out bytes.Buffer
	inString, escaped := false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inString {
			out.WriteByte(c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out.WriteByte(c)
			continue
		}
		if c == '/' && i+1 < len(b) && b[i+1] == '/' {
			for i < len(b) && b[i] != '\n' {
				i++
			}
			if i < len(b) {
				out.WriteByte('\n')
			}
			continue
		}
		if c == '/' && i+1 < len(b) && b[i+1] == '*' {
			end := bytes.Index(b[i+2:], []byte("*/"))
			if end < 0 {
				break
			}
			i += end + 3
			continue
		}
		out.WriteByte(c)
	}
	return out.Bytes()
}

// LoadModels returns the configured providers: built-in catalog providers
// (OpenAI, OpenCode) that have credentials, overlaid with models.json.
// A models.json provider with the same name overrides the preset's fields,
// and its models are merged over preset models with the same ID.
func LoadModels() (ModelsFile, error) {
	var user ModelsFile
	data, err := os.ReadFile(ModelsPath())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return user, err
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(stripJSONComments(data), &user); err != nil {
			return user, err
		}
	}
	auth, err := LoadAuth()
	if err != nil {
		return user, err
	}
	out := ModelsFile{Providers: map[string]Provider{}, auth: auth, names: &sharedNames{}}
	for name, p := range CatalogProviders() {
		if _, configured := user.Providers[name]; configured || out.hasKey(name, p) {
			out.Providers[name] = p
		}
	}
	for name, up := range user.Providers {
		base, builtin := out.Providers[name]
		for i, m := range up.Models {
			// pi's defaults for models it defines from models.json.
			if builtin && slices.ContainsFunc(base.Models, func(b Model) bool { return b.ID == m.ID }) {
				continue
			}
			if (ModelRef{Provider: up, Model: m}).piFormat() {
				if m.ContextWindow == 0 {
					up.Models[i].ContextWindow = 128000
				}
				if m.MaxTokens == 0 {
					up.Models[i].MaxTokens = 16384
				}
			}
		}
		p := mergeProvider(base, up)
		for i, m := range p.Models {
			if o, ok := up.ModelOverrides[m.ID]; ok {
				p.Models[i] = applyModelOverride(m, o)
			}
		}
		out.Providers[name] = p
	}
	return out, nil
}

func (m ModelsFile) hasKey(name string, p Provider) bool {
	key, _ := p.resolveKey(name, m.auth)
	return key != ""
}

func mergeProvider(base, over Provider) Provider {
	if base.BaseURL == "" {
		return over
	}
	if over.Name != "" {
		base.Name = over.Name
	}
	if over.BaseURL != "" {
		base.BaseURL = over.BaseURL
	}
	if over.API != "" {
		base.API = over.API
	}
	if over.APIKey != "" {
		base.APIKey = over.APIKey
	}
	if len(over.Env) > 0 {
		base.Env = over.Env
	}
	if over.MaxTokensField != "" {
		base.MaxTokensField = over.MaxTokensField
	}
	if over.Compat != nil {
		base.Compat = mergeCompat(base.Compat, over.Compat)
	}
	if over.AuthHeader != nil {
		base.AuthHeader = over.AuthHeader
	}
	if over.Subscription {
		base.Subscription = true
	}
	for k, v := range over.Headers {
		if base.Headers == nil {
			base.Headers = map[string]string{}
		}
		base.Headers[k] = v
	}
	for k, v := range over.ExtraBody {
		if base.ExtraBody == nil {
			base.ExtraBody = map[string]any{}
		}
		base.ExtraBody[k] = v
	}
	models := slices.Clone(base.Models)
	for _, m := range over.Models {
		replaced := false
		for i := range models {
			if models[i].ID == m.ID {
				models[i], replaced = mergeModel(models[i], m), true
			}
		}
		if !replaced {
			models = append(models, m)
		}
	}
	base.Models = models
	base.ModelOverrides = over.ModelOverrides
	return base
}

// mergeModel overlays the fields set in over onto base. Maps merge per key,
// so models.json can adjust a single effort mapping of a catalog model.
// (pi replaces a built-in model defined again in models.json; atto merges,
// because its catalog carries verified effort maps.)
func mergeModel(base, over Model) Model {
	if over.Name != "" {
		base.Name = over.Name
	}
	if over.API != "" {
		base.API = over.API
	}
	if over.BaseURL != "" {
		base.BaseURL = over.BaseURL
	}
	if over.Reasoning != nil {
		base.Reasoning = over.Reasoning
	}
	if len(over.Efforts) > 0 {
		base.Efforts = over.Efforts
	}
	if over.ContextWindow > 0 {
		base.ContextWindow = over.ContextWindow
	}
	if over.MaxTokens > 0 {
		base.MaxTokens = over.MaxTokens
	}
	if len(over.Input) > 0 {
		base.Input = over.Input
	}
	if over.Cost != nil {
		base.Cost = over.Cost
	}
	if over.Compat != nil {
		base.Compat = mergeCompat(base.Compat, over.Compat)
	}
	base.EffortMap = mergeMap(base.EffortMap, over.EffortMap)
	base.ExtraBody = mergeMap(base.ExtraBody, over.ExtraBody)
	base.Headers = mergeMap(base.Headers, over.Headers)
	base.SamplingParams = mergeMap(base.SamplingParams, over.SamplingParams)
	if len(over.SamplingParamsByThinkingLevel) > 0 {
		levels := maps.Clone(base.SamplingParamsByThinkingLevel)
		if levels == nil {
			levels = map[string]map[string]any{}
		}
		for level, params := range over.SamplingParamsByThinkingLevel {
			levels[level] = mergeMap(levels[level], params)
		}
		base.SamplingParamsByThinkingLevel = levels
	}
	return base
}

func mergeMap[V any](base, over map[string]V) map[string]V {
	if len(over) == 0 {
		return base
	}
	out := maps.Clone(base)
	if out == nil {
		out = map[string]V{}
	}
	maps.Copy(out, over)
	return out
}

// applyModelOverride is pi's applyModelOverride.
func applyModelOverride(m Model, o ModelOverride) Model {
	if o.Name != "" {
		m.Name = o.Name
	}
	if o.Reasoning != nil {
		m.Reasoning = o.Reasoning
	}
	m.EffortMap = mergeMap(m.EffortMap, o.ThinkingLevelMap)
	if len(o.Input) > 0 {
		m.Input = o.Input
	}
	if o.Cost != nil {
		c := ai.ModelCost{}
		if m.Cost != nil {
			c = *m.Cost
		}
		set := func(dst *float64, v *float64) {
			if v != nil {
				*dst = *v
			}
		}
		set(&c.Input, o.Cost.Input)
		set(&c.Output, o.Cost.Output)
		set(&c.CacheRead, o.Cost.CacheRead)
		set(&c.CacheWrite, o.Cost.CacheWrite)
		if o.Cost.Tiers != nil {
			c.Tiers = o.Cost.Tiers
		}
		m.Cost = &c
	}
	if o.ContextWindow > 0 {
		m.ContextWindow = o.ContextWindow
	}
	if o.MaxTokens > 0 {
		m.MaxTokens = o.MaxTokens
	}
	m.SamplingParams = mergeMap(m.SamplingParams, o.SamplingParams)
	m.Headers = mergeMap(m.Headers, o.Headers)
	if o.Compat != nil {
		m.Compat = mergeCompat(m.Compat, o.Compat)
	}
	if len(o.Efforts) > 0 {
		m.Efforts = o.Efforts
	}
	return m
}

// ResolveAPIKey finds the key for provider name (see resolveKey).
func (p Provider) ResolveAPIKey(name string, auth map[string]AuthEntry) string {
	key, _ := p.resolveKey(name, auth)
	return key
}

// resolveKey finds provider's key in pi's order: the auth.json entry, then
// models.json "apiKey" (a config value), then environment variables (the
// provider's "env" list, or pi's variable for a built-in provider). oauth
// reports an OAuth access token, which expires and must be fetched through
// OAuthToken.
func (p Provider) resolveKey(name string, auth map[string]AuthEntry) (key string, oauth bool) {
	if e, ok := auth[name]; ok {
		if k := ResolvedKey(e); k != "" {
			return k, e.Type == "oauth"
		}
	}
	if p.APIKey != "" {
		if v, ok := ResolveConfigValue(p.APIKey, nil); ok && v != "" {
			return v, false
		}
	}
	envs := p.Env
	if len(envs) == 0 {
		envs = ai.ApiKeyEnvVars(name)
	}
	for _, env := range envs {
		if v := os.Getenv(env); v != "" {
			return v, false
		}
	}
	return "", false
}

// HasKey reports whether provider name has credentials configured.
func (m ModelsFile) HasKey(name string) bool {
	p, ok := m.Providers[name]
	return ok && m.hasKey(name, p)
}

// List returns every configured model, ordered by provider name.
func (m ModelsFile) List() []ModelRef {
	names := make([]string, 0, len(m.Providers))
	for n := range m.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []ModelRef
	for _, n := range names {
		p := m.Providers[n]
		key, oauth := p.resolveKey(n, m.auth)
		for _, mod := range p.Models {
			ref := ModelRef{ProviderName: n, Provider: p, Model: mod, APIKey: key}
			if oauth {
				ref.KeyFunc = func(ctx context.Context) (string, error) { return OAuthToken(ctx, n) }
			}
			out = append(out, ref)
		}
	}
	return out
}

// DisplayName is r's name as shown next to other models: its own name, and
// when a model of that name is also offered by another provider, the provider
// (its ID, as in /model's detail column), "GPT-6 Luna · opencode-go".
func (m ModelsFile) DisplayName(r ModelRef) string {
	name := r.Model.DisplayName()
	if m.sharedNames()[name] {
		return name + " · " + r.ProviderName
	}
	return name
}

func (m ModelsFile) sharedNames() map[string]bool {
	if m.names == nil {
		return m.findSharedNames()
	}
	m.names.once.Do(func() { m.names.set = m.findSharedNames() })
	return m.names.set
}

func (m ModelsFile) findSharedNames() map[string]bool {
	first := map[string]string{} // display name → the first provider with it
	shared := map[string]bool{}
	for provider, p := range m.Providers {
		for _, mod := range p.Models {
			name := mod.DisplayName()
			if other, ok := first[name]; ok && other != provider {
				shared[name] = true
			} else if !ok {
				first[name] = provider
			}
		}
	}
	return shared
}

// Find looks a model up by id, optionally qualified as "provider/id".
// An empty provider matches any.
func (m ModelsFile) Find(provider, id string) (ModelRef, bool) {
	if provider == "" {
		if p, rest, ok := strings.Cut(id, "/"); ok {
			if _, exists := m.Providers[p]; exists {
				provider, id = p, rest
			}
		}
	}
	for _, r := range m.List() {
		if r.Model.ID == id && (provider == "" || r.ProviderName == provider) {
			return r, true
		}
	}
	return ModelRef{}, false
}

// String names the ref as "provider/id".
func (r ModelRef) String() string { return fmt.Sprintf("%s/%s", r.ProviderName, r.Model.ID) }

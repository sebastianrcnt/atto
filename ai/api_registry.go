package ai

// Ported from pi (https://github.com/earendil-works/pi), Copyright (c) 2025
// Mario Zechner, MIT License; see THIRD_PARTY_NOTICES.

import (
	"fmt"
	"strings"
	"sync"
)

// Port of the api registry and global entry points of src/compat.ts:
// registerApiProvider, getApiProvider, stream, complete, streamSimple and
// completeSimple. Built-in APIs register themselves in init.

// ApiProvider is one registered API implementation.
type ApiProvider struct {
	Api          Api
	Stream       StreamFunction
	StreamSimple SimpleStreamFunction
}

type registeredApiProvider struct {
	provider ApiProvider
	sourceID string
}

var (
	registryMu          sync.RWMutex
	apiProviderRegistry = map[Api]registeredApiProvider{}
	builtinApis         = map[Api]ProviderStreams{}
)

func wrapStream(api Api, stream StreamFunction) StreamFunction {
	return func(model *Model, context TranscriptContext, options any) *AssistantMessageEventStream {
		if model.Api != api {
			panic(fmt.Sprintf("Mismatched api: %s expected %s", model.Api, api))
		}
		return stream(model, context, options)
	}
}

func wrapStreamSimple(api Api, stream SimpleStreamFunction) SimpleStreamFunction {
	return func(model *Model, context TranscriptContext, options *SimpleStreamOptions) *AssistantMessageEventStream {
		if model.Api != api {
			panic(fmt.Sprintf("Mismatched api: %s expected %s", model.Api, api))
		}
		return stream(model, context, options)
	}
}

// RegisterApiProvider registers (or replaces) the implementation of an API.
// sourceID groups registrations for UnregisterApiProviders.
func RegisterApiProvider(p ApiProvider, sourceID string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	apiProviderRegistry[p.Api] = registeredApiProvider{
		provider: ApiProvider{Api: p.Api, Stream: wrapStream(p.Api, p.Stream), StreamSimple: wrapStreamSimple(p.Api, p.StreamSimple)},
		sourceID: sourceID,
	}
}

// GetApiProvider returns the implementation registered for api.
func GetApiProvider(api Api) *ApiProvider {
	registryMu.RLock()
	defer registryMu.RUnlock()
	e, ok := apiProviderRegistry[api]
	if !ok {
		return nil
	}
	p := e.provider
	return &p
}

// GetApiProviders lists the registered implementations.
func GetApiProviders() []ApiProvider {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]ApiProvider, 0, len(apiProviderRegistry))
	for _, e := range apiProviderRegistry {
		out = append(out, e.provider)
	}
	return out
}

// UnregisterApiProviders removes every registration made with sourceID.
func UnregisterApiProviders(sourceID string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	for api, e := range apiProviderRegistry {
		if e.sourceID == sourceID {
			delete(apiProviderRegistry, api)
		}
	}
}

// registerBuiltinApi records a built-in API (called from init).
func registerBuiltinApi(api Api, streams ProviderStreams) {
	builtinApis[api] = streams
	if GetApiProvider(api) == nil {
		RegisterApiProvider(ApiProvider{Api: api, Stream: streams.Stream, StreamSimple: streams.StreamSimple}, "")
	}
}

// RegisterBuiltInApiProviders registers the built-in APIs without
// replacing an existing registration (a test may have overridden one).
func RegisterBuiltInApiProviders() {
	for api, s := range builtinApis {
		if GetApiProvider(api) == nil {
			RegisterApiProvider(ApiProvider{Api: api, Stream: s.Stream, StreamSimple: s.StreamSimple}, "")
		}
	}
}

// ResetApiProviders drops all registrations and registers the built-ins.
func ResetApiProviders() {
	registryMu.Lock()
	apiProviderRegistry = map[Api]registeredApiProvider{}
	registryMu.Unlock()
	RegisterBuiltInApiProviders()
}

func init() { RegisterBuiltInApiProviders() }

func resolveApiProvider(api Api) (*ApiProvider, error) {
	p := GetApiProvider(api)
	if p == nil {
		return nil, fmt.Errorf("No API provider registered for api: %s", api)
	}
	return p, nil
}

// withEnvApiKey fills in the provider's environment API key when none was
// given.
func withEnvApiKey(model *Model, options *StreamOptions) {
	if strings.TrimSpace(options.APIKey) != "" {
		return
	}
	if key := GetEnvApiKey(model.Provider, options.Env); key != "" {
		options.APIKey = key
	}
}

// errorStream returns a stream that fails immediately (setup errors).
func errorStream(model *Model, err error) *AssistantMessageEventStream {
	s := NewAssistantMessageEventStream()
	out := newAssistantOutput(model, model.Api)
	out.StopReason = StopError
	out.ErrorMessage = err.Error()
	out.ErrorCause = err
	s.Push(AssistantMessageEvent{Type: EventError, Reason: StopError, Error: out})
	s.End()
	return s
}

// streamOptionsOf returns the StreamOptions embedded in an API options
// value, so the registry can add the environment API key.
func streamOptionsOf(options any) *StreamOptions {
	switch o := options.(type) {
	case *StreamOptions:
		return o
	case *OpenAICompletionsOptions:
		return &o.StreamOptions
	case *OpenAIResponsesOptions:
		return &o.StreamOptions
	case *OpenAICodexResponsesOptions:
		return &o.StreamOptions
	}
	return nil
}

// Stream sends context to model with API-specific options.
func Stream(model *Model, context Context, options any) *AssistantMessageEventStream {
	transcript := NormalizeContext(context)
	if options == nil {
		options = &StreamOptions{}
	}
	if so := streamOptionsOf(options); so != nil {
		withEnvApiKey(model, so)
	}
	streams, err := providerStreamsFor(model)
	if err != nil {
		return errorStream(model, err)
	}
	return streams.Stream(model, transcript, options)
}

// Complete is Stream, waiting for the final message.
func Complete(model *Model, context Context, options any) *AssistantMessage {
	return Stream(model, context, options).Result()
}

// StreamSimple sends context with provider-neutral options.
func StreamSimple(model *Model, context Context, options *SimpleStreamOptions) *AssistantMessageEventStream {
	transcript := NormalizeContext(context)
	if options == nil {
		options = &SimpleStreamOptions{}
	}
	withEnvApiKey(model, &options.StreamOptions)
	streams, err := providerStreamsFor(model)
	if err != nil {
		return errorStream(model, err)
	}
	return streams.StreamSimple(model, transcript, options)
}

// CompleteSimple is StreamSimple, waiting for the final message.
func CompleteSimple(model *Model, context Context, options *SimpleStreamOptions) *AssistantMessage {
	return StreamSimple(model, context, options).Result()
}

// providerStreamsFor returns the registered API, wrapped by the model's
// built-in provider (e.g. OpenCode's session header) unless the API was
// overridden.
func providerStreamsFor(model *Model) (ProviderStreams, error) {
	p, err := resolveApiProvider(model.Api)
	if err != nil {
		return ProviderStreams{}, err
	}
	streams := ProviderStreams{Stream: p.Stream, StreamSimple: p.StreamSimple}
	if wrap := builtinProviderWrappers[model.Provider]; wrap != nil && isBuiltinRegistration(model.Api) {
		streams = wrap(streams)
	}
	return streams, nil
}

func isBuiltinRegistration(api Api) bool {
	registryMu.RLock()
	defer registryMu.RUnlock()
	e, ok := apiProviderRegistry[api]
	return ok && e.sourceID == ""
}

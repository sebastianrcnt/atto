package ai

// Ported from pi (https://github.com/earendil-works/pi), Copyright (c) 2025
// Mario Zechner, MIT License; see THIRD_PARTY_NOTICES.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Port of src/api/openai-codex-responses.ts: the ChatGPT backend's Codex
// Responses endpoint, authenticated with an openai-codex OAuth token.
//
// Only the SSE transport is ported. pi prefers a cached WebSocket
// (transport "auto") and zstd-compresses SSE bodies; atto always uses
// uncompressed SSE, which the backend accepts.

func init() {
	registerBuiltinApi(ApiOpenAICodexResponses, ProviderStreams{Stream: streamOpenAICodexResponsesAny, StreamSimple: StreamSimpleOpenAICodexResponses})
}

const (
	defaultCodexBaseURL    = "https://chatgpt.com/backend-api"
	codexJWTClaimPath      = "https://api.openai.com/auth"
	codexBaseDelay         = time.Second
	defaultCodexMaxRetries = 0
)

// CodexOriginator names the client to the Codex backend (pi sends "pi").
var CodexOriginator = "atto"

var codexToolCallProviders = map[string]bool{"openai": true, "openai-codex": true, "opencode": true}

// OpenAICodexResponsesOptions are the API's own options.
type OpenAICodexResponsesOptions struct {
	StreamOptions
	ReasoningEffort  string // also "none"
	ReasoningSummary string
	ServiceTier      string
	TextVerbosity    string
	ToolChoice       string
}

var terminalRateLimit = regexp.MustCompile(`(?i)GoUsageLimitError|FreeUsageLimitError|Monthly usage limit reached|available balance|insufficient_quota|out of budget|quota exceeded|billing`)
var retryableText = regexp.MustCompile(`(?i)rate.?limit|overloaded|service.?unavailable|upstream.?connect|connection.?refused`)

func isRetryableCodexError(status int, text string) bool {
	if status == 429 && terminalRateLimit.MatchString(text) {
		return false
	}
	switch status {
	case 429, 500, 502, 503, 504:
		return true
	}
	return retryableText.MatchString(text)
}

func codexOptionsOf(options any) *OpenAICodexResponsesOptions {
	switch o := options.(type) {
	case *OpenAICodexResponsesOptions:
		if o != nil {
			return o
		}
	case *StreamOptions:
		if o != nil {
			return &OpenAICodexResponsesOptions{StreamOptions: *o}
		}
	}
	return &OpenAICodexResponsesOptions{}
}

func streamOpenAICodexResponsesAny(model *Model, context TranscriptContext, options any) *AssistantMessageEventStream {
	return StreamOpenAICodexResponses(model, context, codexOptionsOf(options))
}

// codexAPIError is an error event or failed response from the backend.
type codexAPIError struct{ msg, code string }

func (e *codexAPIError) Error() string { return e.msg }

// StreamOpenAICodexResponses streams a Codex request (pi: stream).
func StreamOpenAICodexResponses(model *Model, context TranscriptContext, options *OpenAICodexResponsesOptions) *AssistantMessageEventStream {
	stream := NewAssistantMessageEventStream()
	supportsMidConvo := model.Compat != nil && pick(model.Compat.SupportsMidConvoSystemMessages, false)
	normalized := ResolveTranscript(context, supportsMidConvo)
	if options == nil {
		options = &OpenAICodexResponsesOptions{}
	}
	go func() {
		output := newAssistantOutput(model, ApiOpenAICodexResponses)
		err := func() error {
			ctx, cancel := requestContext(&options.StreamOptions)
			defer cancel()
			apiKey := options.APIKey
			if apiKey == "" {
				return fmt.Errorf("No API key for provider: %s", model.Provider)
			}
			accountID, err := ExtractCodexAccountID(apiKey)
			if err != nil {
				return err
			}
			cacheSessionID := options.SessionID
			if options.CacheRetention == CacheRetentionNone {
				cacheSessionID = ""
			}
			codexSessionID := ClampOpenAIPromptCacheKey(cacheSessionID)
			params := buildCodexRequestBody(model, normalized, options, codexSessionID)
			if options.OnPayload != nil {
				if next := options.OnPayload(params, model); next != nil {
					params = next
				}
			}
			body, err := marshalBody(params)
			if err != nil {
				return err
			}
			if options.OnRequestBody != nil {
				options.OnRequestBody(body)
			}
			headers := buildCodexSSEHeaders(model.Headers, options.Headers, accountID, codexSessionID)

			var resp *http.Response
			maxRetries := options.MaxRetries
			for attempt := 0; attempt <= maxRetries; attempt++ {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				resp, err = postJSON(ctx, options.HTTPClient, resolveCodexURL(model.BaseURL), body, apiKey, headers)
				if err == nil {
					break
				}
				if pe, ok := errors.AsType[*ProviderError](err); ok {
					if attempt < maxRetries && isRetryableCodexError(pe.Status, pe.Body) {
						d := codexBaseDelay * time.Duration(math.Pow(2, float64(attempt)))
						if ra, derr := retryDelay(pe, attempt, options.MaxRetryDelayMs); derr == nil && (pe.Headers.Get("retry-after") != "" || pe.Headers.Get("retry-after-ms") != "") {
							d = ra
						}
						select {
						case <-time.After(d):
						case <-ctx.Done():
							return ctx.Err()
						}
						continue
					}
					return errors.New(parseCodexErrorResponse(pe))
				}
				if attempt < maxRetries && !strings.Contains(err.Error(), "usage limit") {
					select {
					case <-time.After(codexBaseDelay * time.Duration(math.Pow(2, float64(attempt)))):
					case <-ctx.Done():
						return ctx.Err()
					}
					continue
				}
				return err
			}
			defer resp.Body.Close()
			if options.OnResponse != nil {
				options.OnResponse(ProviderResponse{Status: resp.StatusCode, Headers: resp.Header}, model)
			}
			stream.Push(AssistantMessageEvent{Type: EventStart, Partial: output})
			src := newSSESource(resp.Body)
			defer src.close()
			err = ProcessResponsesStream(mapCodexEvents(sseResponsesEvents(src), output), output, stream, model, ResponsesStreamOptions{
				ServiceTier:             options.ServiceTier,
				ResolveServiceTier:      resolveCodexServiceTier,
				ApplyServiceTierPricing: func(u *Usage, tier string) { applyServiceTierPricing(u, tier, model.ID, false) },
			})
			if err != nil {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			switch output.StopReason {
			case StopPending:
				return fmt.Errorf("Codex stream ended without a stop reason")
			case StopError, StopAborted:
				if output.ErrorMessage != "" {
					return fmt.Errorf("%s", output.ErrorMessage)
				}
				return fmt.Errorf("An unknown error occurred")
			}
			return nil
		}()
		if err == nil {
			stream.Push(AssistantMessageEvent{Type: EventDone, Reason: output.StopReason, Message: output})
			stream.End()
			return
		}
		output.StopReason = StopError
		if options.ctx().Err() != nil {
			output.StopReason = StopAborted
		}
		output.ErrorMessage = FormatProviderError(err, "")
		stream.Push(AssistantMessageEvent{Type: EventError, Reason: output.StopReason, Error: output})
		stream.End()
	}()
	return stream
}

// StreamSimpleOpenAICodexResponses maps provider-neutral options.
func StreamSimpleOpenAICodexResponses(model *Model, context TranscriptContext, options *SimpleStreamOptions) *AssistantMessageEventStream {
	if options == nil {
		options = &SimpleStreamOptions{}
	}
	base := BuildBaseOptions(model, context, options, options.APIKey)
	effort := ""
	if options.Reasoning != "" {
		effort = ClampThinkingLevel(model, options.Reasoning)
	}
	if effort == ThinkingOff {
		effort = ""
	}
	return StreamOpenAICodexResponses(model, context, &OpenAICodexResponsesOptions{StreamOptions: base, ReasoningEffort: effort, ToolChoice: options.ToolChoice})
}

func buildCodexRequestBody(model *Model, context TranscriptContext, options *OpenAICodexResponsesOptions, cacheSessionID string) map[string]any {
	supportsStrictMode := model.Compat == nil || pick(model.Compat.SupportsStrictMode, true)
	supportsMidConvo := model.Compat != nil && pick(model.Compat.SupportsMidConvoSystemMessages, false)
	transcriptTools := ResolveTranscriptTools(context.Messages, false)
	messages := ConvertResponsesMessages(model, context, codexToolCallProviders, ConvertResponsesMessagesOptions{
		ExcludeSystemPrompt: true, SupportsMidConvoSystemMessages: supportsMidConvo,
	})
	instructions := ""
	if m := GetInitialSystemMessage(context.Messages); m != nil {
		instructions = GetSystemMessageText(m)
	}
	if instructions == "" {
		instructions = "You are a helpful assistant."
	}
	verbosity := options.TextVerbosity
	if verbosity == "" {
		verbosity = "low"
	}
	toolChoice := options.ToolChoice
	if toolChoice == "" {
		toolChoice = "auto"
	}
	body := map[string]any{
		"model":               model.ID,
		"store":               false,
		"stream":              true,
		"instructions":        instructions,
		"input":               messages,
		"text":                obj("verbosity", verbosity),
		"include":             []string{"reasoning.encrypted_content"},
		"tool_choice":         toolChoice,
		"parallel_tool_calls": true,
	}
	if cacheSessionID != "" {
		body["prompt_cache_key"] = cacheSessionID
	}
	if options.Temperature != nil {
		body["temperature"] = *options.Temperature
	}
	if options.ServiceTier != "" {
		body["service_tier"] = options.ServiceTier
	}
	if len(transcriptTools.RequestTools) > 0 {
		body["tools"] = ConvertResponsesTools(transcriptTools.RequestTools, supportsStrictMode, nil)
	}
	tlm := model.ThinkingLevelMap
	if options.ReasoningEffort != "" {
		effort, null := options.ReasoningEffort, false
		if effort == "none" {
			if v, present, isNull := tlm.Lookup(ThinkingOff); present {
				effort, null = v, isNull
			}
		} else if v, present, isNull := tlm.Lookup(effort); present && !isNull {
			effort = v
		}
		if !null {
			summary := options.ReasoningSummary
			if summary == "" {
				summary = "auto"
			}
			body["reasoning"] = obj("effort", effort, "summary", summary)
		}
	} else if v, present, isNull := tlm.Lookup(ThinkingOff); model.Reasoning && !isNull {
		if !present {
			v = "none"
		}
		body["reasoning"] = obj("effort", v)
	}
	return body
}

func resolveCodexServiceTier(response, request string) string {
	if response == "default" && (request == "flex" || request == "priority") {
		return request
	}
	if response != "" {
		return response
	}
	return request
}

func resolveCodexURL(baseURL string) string {
	raw := baseURL
	if strings.TrimSpace(raw) == "" {
		raw = defaultCodexBaseURL
	}
	n := strings.TrimRight(raw, "/")
	switch {
	case strings.HasSuffix(n, "/codex/responses"):
		return n
	case strings.HasSuffix(n, "/codex"):
		return n + "/responses"
	}
	return n + "/codex/responses"
}

var codexStatuses = map[string]bool{"completed": true, "incomplete": true, "failed": true, "cancelled": true, "queued": true, "in_progress": true}

// mapCodexEvents turns Codex error events into errors and normalizes the
// terminal event to response.completed.
func mapCodexEvents(next func() (responsesEvent, bool, error), output *AssistantMessage) func() (responsesEvent, bool, error) {
	finished := false
	return func() (responsesEvent, bool, error) {
		if finished {
			return responsesEvent{}, false, nil
		}
		for {
			ev, ok, err := next()
			if err != nil || !ok {
				return ev, ok, err
			}
			switch ev.Type {
			case "":
				continue
			case "error":
				msg := ev.Message
				code := ""
				if ev.Code != nil {
					code = fmt.Sprint(ev.Code)
				}
				if msg == "" {
					msg = code
				}
				return ev, false, &codexAPIError{msg: "Codex error: " + msg, code: code}
			case "response.failed":
				msg, code := "Codex response failed", ""
				if r := ev.Response; r != nil && r.Error != nil {
					code = r.Error.Code
					if r.Error.Message != "" {
						msg = r.Error.Message
					}
				}
				return ev, false, &codexAPIError{msg: msg, code: code}
			case "response.done", "response.completed", "response.incomplete":
				if r := ev.Response; r != nil {
					if r.EndTurn != nil {
						output.EndTurn = r.EndTurn
					}
					if !codexStatuses[r.Status] {
						r.Status = ""
					}
				}
				ev.Type = "response.completed"
				finished = true
				return ev, true, nil
			}
			return ev, true, nil
		}
	}
}

func parseCodexErrorResponse(pe *ProviderError) string {
	message := pe.Body
	if message == "" {
		message = pe.StatusText
	}
	if message == "" {
		message = "Request failed"
	}
	var parsed struct {
		Error *struct {
			Code     string  `json:"code"`
			Type     string  `json:"type"`
			Message  string  `json:"message"`
			PlanType string  `json:"plan_type"`
			ResetsAt float64 `json:"resets_at"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(pe.Body), &parsed) == nil && parsed.Error != nil {
		e := parsed.Error
		code := e.Code
		if code == "" {
			code = e.Type
		}
		friendly := ""
		if regexp.MustCompile(`(?i)usage_limit_reached|usage_not_included|rate_limit_exceeded`).MatchString(code) || pe.Status == 429 {
			plan := ""
			if e.PlanType != "" {
				plan = " (" + strings.ToLower(e.PlanType) + " plan)"
			}
			when := ""
			if e.ResetsAt > 0 {
				mins := max(0, int(math.Round((e.ResetsAt*1000-float64(time.Now().UnixMilli()))/60000)))
				when = fmt.Sprintf(" Try again in ~%d min.", mins)
			}
			friendly = strings.TrimSpace("You have hit your ChatGPT usage limit" + plan + "." + when)
		}
		if friendly != "" {
			return friendly
		}
		if e.Message != "" {
			return e.Message
		}
	}
	return message
}

// ExtractCodexAccountID reads the ChatGPT account id from an access token.
func ExtractCodexAccountID(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) == 3 {
		payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
		if err != nil {
			payload, err = base64.StdEncoding.DecodeString(parts[1])
		}
		if err == nil {
			var claims map[string]json.RawMessage
			if json.Unmarshal(payload, &claims) == nil {
				var auth struct {
					AccountID string `json:"chatgpt_account_id"`
				}
				if json.Unmarshal(claims[codexJWTClaimPath], &auth) == nil && auth.AccountID != "" {
					return auth.AccountID, nil
				}
			}
		}
	}
	return "", errors.New("Failed to extract accountId from token")
}

func buildCodexSSEHeaders(initHeaders, additional map[string]string, accountID, sessionID string) map[string]string {
	h := map[string]string{}
	maps.Copy(h, initHeaders)
	maps.Copy(h, additional)
	h["chatgpt-account-id"] = accountID
	h["originator"] = CodexOriginator
	h["OpenAI-Beta"] = "responses=experimental"
	h["accept"] = "text/event-stream"
	h["content-type"] = "application/json"
	if sessionID != "" {
		h["session-id"] = sessionID
		h["x-client-request-id"] = sessionID
	}
	return h
}

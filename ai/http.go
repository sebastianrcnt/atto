package ai

// Ported from pi (https://github.com/earendil-works/pi), Copyright (c) 2025
// Mario Zechner, MIT License; see THIRD_PARTY_NOTICES.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// pi talks to OpenAI-compatible servers through the openai SDK. This file
// is the Go replacement for the parts of it pi relies on: a JSON POST with
// default headers, Server-Sent Events decoding, HTTP errors that carry
// status and body, and the SDK retry policy (src/utils/provider-retry.ts)
// plus error formatting (src/utils/error-body.ts).

// UserAgent identifies the client; atto sets it at startup.
var UserAgent = "atto"

func nowMillis() int64 { return time.Now().UnixMilli() }

// ProviderError is a non-2xx HTTP response.
type ProviderError struct {
	Status     int
	StatusText string
	Body       string
	Headers    http.Header
}

func (e *ProviderError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("%d %s", e.Status, e.StatusText)
	}
	return fmt.Sprintf("%d: %s", e.Status, e.Body)
}

// MaxProviderErrorBodyChars caps error bodies in messages.
const MaxProviderErrorBodyChars = 4000

func truncateErrorText(text string, maxChars int) string {
	if len(text) <= maxChars {
		return text
	}
	return fmt.Sprintf("%s... [truncated %d chars]", text[:maxChars], len(text)-maxChars)
}

// FormatProviderError renders an error for AssistantMessage.ErrorMessage:
// "<status>: <body>", or "<prefix> (<status>): <body>" with a prefix.
func FormatProviderError(err error, prefix string) string {
	if pe, ok := errors.AsType[*ProviderError](err); ok {
		body := truncateErrorText(strings.TrimSpace(pe.Body), MaxProviderErrorBodyChars)
		if body == "" {
			body = strings.TrimSpace(fmt.Sprintf("%d %s", pe.Status, pe.StatusText))
		}
		if prefix != "" {
			return fmt.Sprintf("%s (%d): %s", prefix, pe.Status, body)
		}
		return fmt.Sprintf("%d: %s", pe.Status, body)
	}
	return err.Error()
}

// postJSON sends body to url. Default headers come first, then extra
// headers (later maps win). "Authorization: Bearer <apiKey>" is set when
// apiKey is non-empty (atto: keyless local servers get no header).
func postJSON(ctx context.Context, client *http.Client, url string, body []byte, apiKey string, headerSets ...map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", UserAgent)
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	for _, hs := range headerSets {
		for k, v := range hs {
			req.Header.Set(k, v)
		}
	}
	if client == nil {
		client = http.DefaultClient
	}
	recordRequest(url, body)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return nil, &ProviderError{
			Status: resp.StatusCode, StatusText: strings.TrimPrefix(resp.Status, strconv.Itoa(resp.StatusCode)+" "),
			Body: string(b), Headers: resp.Header,
		}
	}
	return resp, nil
}

// sseEvent is one Server-Sent Event.
type sseEvent struct {
	Event string
	Data  string
}

// readSSE calls fn for every event in r until EOF, fn returns false, or an
// error. Data lines of one event are joined with "\n".
func readSSE(r io.Reader, fn func(sseEvent) (bool, error)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 64*1024*1024)
	var ev sseEvent
	var data []string
	flush := func() (bool, error) {
		if len(data) == 0 && ev.Event == "" {
			return true, nil
		}
		ev.Data = strings.Join(data, "\n")
		cont, err := fn(ev)
		ev, data = sseEvent{}, nil
		return cont, err
	}
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if line == "" {
			if cont, err := flush(); err != nil || !cont {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "data":
			data = append(data, value)
		case "event":
			ev.Event = value
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	_, err := flush()
	return err
}

// streamError is an error the server reported inside the event stream.
type streamError struct {
	Message string
	Code    string
	Raw     json.RawMessage
}

func (e *streamError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return string(e.Raw)
}

// --- src/utils/provider-retry.ts ---

const defaultMaxRetryDelayMs = 60_000

func isRetryableProviderError(err error) (*ProviderError, bool) {
	var pe *ProviderError
	if !errors.As(err, &pe) {
		return nil, false
	}
	switch pe.Headers.Get("x-should-retry") {
	case "true":
		return pe, true
	case "false":
		return pe, false
	}
	return pe, pe.Status == 408 || pe.Status == 409 || pe.Status == 429 || pe.Status >= 500
}

func retryDelay(pe *ProviderError, retryIndex, maxRetryDelayMs int) (time.Duration, error) {
	maxDelay := maxRetryDelayMs
	if maxDelay == 0 {
		maxDelay = defaultMaxRetryDelayMs
	}
	check := func(ms float64) (time.Duration, error) {
		if maxDelay > 0 && ms > float64(maxDelay) {
			return 0, fmt.Errorf("Server requested %ds retry delay (max: %ds). %s", int((ms+999)/1000), (maxDelay+999)/1000, pe.Error())
		}
		return time.Duration(ms * float64(time.Millisecond)), nil
	}
	if v := pe.Headers.Get("retry-after-ms"); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil {
			return check(ms)
		}
	}
	if v := pe.Headers.Get("retry-after"); v != "" {
		if s, err := strconv.ParseFloat(v, 64); err == nil {
			return check(s * 1000)
		}
		if t, err := http.ParseTime(v); err == nil {
			return check(float64(time.Until(t).Milliseconds()))
		}
	}
	exp := min(0.5*float64(int(1)<<retryIndex), 8) * 1000
	return time.Duration(exp * (1 - rand.Float64()*0.25) * float64(time.Millisecond)), nil
}

// retryProviderRequest repeats request on retryable HTTP errors, up to
// maxRetries times (pi's default is 0: no retries).
func retryProviderRequest[T any](ctx context.Context, maxRetries, maxRetryDelayMs int, request func() (T, error)) (T, error) {
	remaining := maxRetries
	for {
		v, err := request()
		if err == nil {
			return v, nil
		}
		if ctx.Err() != nil {
			return v, ctx.Err()
		}
		pe, ok := isRetryableProviderError(err)
		if remaining <= 0 || !ok {
			return v, err
		}
		d, derr := retryDelay(pe, maxRetries-remaining, maxRetryDelayMs)
		if derr != nil {
			return v, derr
		}
		remaining--
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return v, ctx.Err()
		}
	}
}

// requestContext applies TimeoutMs to ctx.
func requestContext(o *StreamOptions) (context.Context, context.CancelFunc) {
	ctx := o.ctx()
	if o != nil && o.TimeoutMs > 0 {
		return context.WithTimeout(ctx, time.Duration(o.TimeoutMs)*time.Millisecond)
	}
	return context.WithCancel(ctx)
}

// headerSet reports whether headers has a non-empty value for name.
func hasHeader(headers map[string]string, name string) bool {
	for k, v := range headers {
		if strings.EqualFold(k, name) && strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

// marshalBody serializes request params. Go escapes "<", ">" and "&" where
// JSON.stringify does not; atto has always sent the escaped form, and
// keeping it keeps existing cached prefixes byte-identical. The JSON value
// is the same either way.
func marshalBody(params map[string]any) ([]byte, error) {
	return json.Marshal(params)
}

// sseSource pulls events from a body one at a time.
type sseSource struct {
	events chan sseEvent
	err    error
	done   chan struct{}
}

// newSSESource starts reading body; close stops the reader (the caller
// also closes body).
func newSSESource(body io.Reader) *sseSource {
	s := &sseSource{events: make(chan sseEvent), done: make(chan struct{})}
	go func() {
		defer close(s.events)
		s.err = readSSE(body, func(ev sseEvent) (bool, error) {
			select {
			case s.events <- ev:
				return true, nil
			case <-s.done:
				return false, nil
			}
		})
	}()
	return s
}

// next returns the next event; ok is false at the end, with the read
// error if there was one.
func (s *sseSource) next() (sseEvent, bool, error) {
	ev, ok := <-s.events
	if !ok {
		return sseEvent{}, false, s.err
	}
	return ev, true, nil
}

func (s *sseSource) close() {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}

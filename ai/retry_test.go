package ai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestIsPermanent(t *testing.T) {
	permanent := []error{
		context.Canceled,
		errors.New("401: invalid api key"),
		errors.New("403: forbidden"),
		errors.New("404: model not found"),
		errors.New("400: invalid request: tools must be an array"),
		errors.New("Upstream request failed (400): unsupported parameter: reasoning_effort"),
		errors.New("422: unprocessable entity"),
		errors.New("400: bad request"),
		errors.New("410: gone"),
		errors.New("429: You have hit your usage limit"),
		errors.New("This model's maximum context length is 131072 tokens"),
		errors.New("400: the response was flagged by the content filter"),
		&ProviderError{Status: 402, Body: "pay up"},
		fmt.Errorf("turn: %w", context.Canceled),
	}
	for _, err := range permanent {
		if !IsPermanent(err) {
			t.Errorf("%v should be permanent", err)
		}
	}
	passing := []error{
		errors.New("Stream ended without finish_reason"),
		errors.New("boom"),
		errors.New("500: internal error"),
		errors.New("503 Service Unavailable"),
		errors.New("429: Rate limit exceeded, retry later"),
		&ProviderError{Status: 408, Body: "request timeout"},
		&ProviderError{Status: 409, Body: "conflict"},
		&ProviderError{Status: 425, Body: "too early"},
		errors.New("400: Upstream request failed: Model is unavailable"),
		errors.New(`Post "https://x/v1": read tcp: connection reset by peer`),
		context.DeadlineExceeded,
		&ProviderError{Status: 502, StatusText: "Bad Gateway"},
	}
	for _, err := range passing {
		if IsPermanent(err) {
			t.Errorf("%v should be retried", err)
		}
	}
}

func TestRetryWait(t *testing.T) {
	for attempt, want := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 9: 30 * time.Second} {
		got, err := RetryWait(errors.New("boom"), attempt, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if got < want*9/10 || got > want*11/10 {
			t.Errorf("attempt %d: %v, want about %v", attempt, got, want)
		}
	}
	pe := &ProviderError{Status: 429, Headers: http.Header{"Retry-After": {"7"}}}
	if got, err := RetryWait(pe, 1, time.Minute); err != nil || got != 7*time.Second {
		t.Errorf("retry-after: %v %v", got, err)
	}
}

func TestStatusOf(t *testing.T) {
	cases := map[error]int{
		nil:                         0,
		errors.New("boom"):          0,
		errors.New("503: overload"): 503,
		errors.New("Upstream request failed (400): bad"): 400,
		&ProviderError{Status: 429}:                      429,
	}
	for err, want := range cases {
		if got := StatusOf(err); got != want {
			t.Errorf("StatusOf(%v) = %d, want %d", err, got, want)
		}
	}
}

func TestRetryWaitRejectsLongProviderDelay(t *testing.T) {
	for _, header := range []http.Header{
		{"Retry-After": {"3600"}},
		{"Retry-After-Ms": {"3600000"}},
		{"Retry-After": {time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)}},
	} {
		pe := &ProviderError{Status: 429, Headers: header}
		delay, err := RetryWait(pe, 1, 5*time.Minute)
		if err == nil || delay != 0 || !strings.Contains(err.Error(), "retry delay") || !strings.Contains(err.Error(), "3600s") {
			t.Fatalf("header %v: delay %v, err %v", header, delay, err)
		}
	}
}

func TestRepeatedServerError(t *testing.T) {
	vision := &ProviderError{Status: 503, Body: `{"error": {"message": "the vision encoder could not start"}}`}
	again := &ProviderError{Status: 503, Body: ` {"error": {"message":   "the vision encoder could not start"}}
`}
	for _, c := range []struct {
		name      string
		prev, err error
		want      bool
	}{
		{"same message", vision, again, true},
		{"nothing before", nil, vision, false},
		{"other body", vision, &ProviderError{Status: 503, Body: "boom"}, false},
		{"other status", vision, &ProviderError{Status: 502, Body: vision.Body}, false},
		{"no message", &ProviderError{Status: 503}, &ProviderError{Status: 503}, false},
		{"overloaded", &ProviderError{Status: 503, Body: "overloaded"}, &ProviderError{Status: 503, Body: "overloaded"}, false},
		{"generic", &ProviderError{Status: 500, Body: "Internal Server Error"}, &ProviderError{Status: 500, Body: "Internal Server Error"}, false},
		{"4xx", &ProviderError{Status: 400, Body: "x"}, &ProviderError{Status: 400, Body: "x"}, false},
		{"not a provider error", errors.New("503: x"), errors.New("503: x"), false},
		{"wrapped", fmt.Errorf("a: %w", vision), fmt.Errorf("b: %w", again), true},
	} {
		if got := RepeatedServerError(c.prev, c.err); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

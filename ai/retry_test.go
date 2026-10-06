package ai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
		errors.New("400: Upstream request failed: Model is unavailable"),
		errors.New("400: bad request"),
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
		got := RetryWait(errors.New("boom"), attempt, time.Minute)
		if got < want*9/10 || got > want*11/10 {
			t.Errorf("attempt %d: %v, want about %v", attempt, got, want)
		}
	}
	pe := &ProviderError{Status: 429, Headers: http.Header{"Retry-After": {"7"}}}
	if got := RetryWait(pe, 1, time.Minute); got != 7*time.Second {
		t.Errorf("retry-after: %v", got)
	}
}

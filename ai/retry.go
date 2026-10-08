package ai

import (
	"context"
	"errors"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A failed model request is sent again unless the failure says it would
// happen again, as codex does (CodexErr::retry_delay): an unknown error, a
// stream cut off mid-reply or a 5xx passes by itself more often than not,
// and a retry costs little next to a turn (or a goal) that stops for it.
// IsPermanent names what is not retried; everything else is.

// usageLimit matches the errors that mean the account's usage or quota is
// used up, as opposed to a rate limit that passes.
var usageLimit = regexp.MustCompile(`(?i)usage.?limit|insufficient_quota|quota exceeded|exceeded your current quota|out of (credits|budget)|available balance|billing`)

// IsUsageLimit reports whether err is the provider saying the usage limit
// was reached (codex's UsageLimitExceeded): a 402, or an error whose text
// names a usage limit or quota.
func IsUsageLimit(err error) bool {
	if err == nil {
		return false
	}
	if pe, ok := errors.AsType[*ProviderError](err); ok && pe.Status == 402 {
		return true
	}
	return usageLimit.MatchString(err.Error())
}

// IsContextOverflow reports whether err is the request not fitting the
// model's context window: compaction, not a retry, is what helps.
func IsContextOverflow(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "context") && (strings.Contains(s, "exceed") || strings.Contains(s, "too long") ||
		strings.Contains(s, "maximum") || strings.Contains(s, "no room"))
}

// permanentText marks a request the provider rejected for what it is (a
// bad parameter, an unsupported feature, a refused content), so sending it
// again fails the same way. Only checked under a 400-class status: some
// gateways answer a passing outage with a 400 too.
var permanentText = regexp.MustCompile(`(?i)invalid|must (be|not|have)|required|not supported|unsupported|unknown (parameter|field|model)|unrecognized|malformed|schema|does not exist|not found|not allowed`)

// policyText marks a reply refused by the provider's content policy.
var policyText = regexp.MustCompile(`(?i)content.?(filter|policy|management)|safety (system|policy)|flagged`)

// passingText marks a failure that passes by itself even under a status
// that would otherwise mean a bad request.
var passingText = regexp.MustCompile(`(?i)unavailable|overloaded|gateway|time-?d? ?out|temporar|try again|at capacity|rate.?limit`)

// ErrNotRetryable, wrapped in an error, says that trying again will not
// help, though the provider did not refuse anything.
var ErrNotRetryable = errors.New("retrying will not help")

// IsPermanent reports whether sending the request again would fail the
// same way: an interrupt, a usage limit, a context overflow (compact
// instead), an authentication or permission failure, a missing model or
// endpoint, a request the provider rejected for its content or a content
// policy refusal.
func IsPermanent(err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, context.Canceled), errors.Is(err, ErrNotRetryable):
		return true
	case errors.Is(err, context.DeadlineExceeded):
		return false
	case IsUsageLimit(err), IsContextOverflow(err), policyText.MatchString(err.Error()):
		return true
	}
	switch st := StatusOf(err); {
	case st == 401 || st == 402 || st == 403 || st == 404 || st == 405 || st == 413:
		return true
	case st == 422:
		return !passingText.MatchString(err.Error())
	case st == 400:
		msg := err.Error()
		return permanentText.MatchString(msg) && !passingText.MatchString(msg)
	}
	return false
}

// statusText is the status atto's error messages start with ("503: ...",
// "Upstream request failed (400): ...").
var statusText = regexp.MustCompile(`^(?:(\d{3})\b|[^(:]*\((\d{3})\):)`)

// StatusOf is the HTTP status an error carries: the provider's, or the one
// its message starts with; 0 for none.
func StatusOf(err error) int {
	if err == nil {
		return 0
	}
	if pe, ok := errors.AsType[*ProviderError](err); ok {
		return pe.Status
	}
	m := statusText.FindStringSubmatch(err.Error())
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1] + m[2])
	return n
}

// RetryWait is how long to wait before sending a failed request again for
// the attempt-th time (from 1): the provider's Retry-After when it gives
// one, else an exponential backoff from 1s, capped at 30s, with jitter.
// A provider delay above max returns an error: do not retry this turn.
func RetryWait(err error, attempt int, max time.Duration) (time.Duration, error) {
	if pe, ok := errors.AsType[*ProviderError](err); ok && pe.Headers != nil &&
		(pe.Headers.Get("retry-after") != "" || pe.Headers.Get("retry-after-ms") != "") {
		return retryDelay(pe, attempt-1, int(max.Milliseconds()))
	}
	d := time.Second << min(attempt-1, 5)
	d = min(d, 30*time.Second)
	return time.Duration(float64(d) * (0.9 + 0.2*rand.Float64())), nil
}

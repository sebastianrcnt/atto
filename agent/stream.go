package agent

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/ai"
	"github.com/sebastianrcnt/atto/provider"
)

// StreamRetry reports a model request that failed and is sent again within
// the turn: the reply streamed so far (if any) is dropped, not kept.
// Context-pressure compaction has its own one-retry budget per turn.
type StreamRetry struct {
	Attempt, Of int
	Wait        time.Duration
	Err         string
}

// streamRetries is how many times a failed model request is sent again
// within a turn before the turn fails (codex's stream_max_retries).
// ai.IsPermanent failures are not retried; context pressure is compacted
// once per turn instead. A server that answers the same 5xx with the same
// message twice in a row is not asked a third time
// (ai.RepeatedServerError).
const streamRetries = 5

// maxRetryWait caps a provider's Retry-After for these retries.
const maxRetryWait = 5 * time.Minute

// retryWait is ai.RetryWait; tests shorten it.
var retryWait = ai.RetryWait

func (a *Agent) tools() []provider.Tool {
	return []provider.Tool{{
		Type: "function",
		Function: provider.ToolFunction{
			Name:        a.Shell.ToolName(),
			Description: toolDescription(a.Shell),
			Parameters:  toolSchema(a.Shell),
		},
	}}
}

// request builds a request and returns the client to send it with.
func (a *Agent) request(extra ...provider.Message) (provider.Streamer, provider.Request) {
	// Model, effort, transport and session routing are one configuration
	// snapshot. SetModel may replace the client as well as the model, so
	// reading them under separate locks can build a request from two models.
	a.cfgMu.Lock()
	model, effort, client, sessID := a.model, a.effort, a.client, a.sessID
	a.cfgMu.Unlock()
	msgs := make([]provider.Message, 0, len(a.messages)+len(extra)+1)
	msgs = append(msgs, provider.Message{Role: "system", Content: a.SystemPrompt()})
	msgs = append(msgs, a.messages...)
	msgs = append(msgs, extra...)
	if !model.Model.Images() {
		msgs = withoutImages(msgs, ImagesUnsupported)
	}
	return client, provider.Request{
		SessionID: sessID,
		Model:     model.Model.ID,
		Messages:  msgs,
		Tools:     a.tools(),
		Effort:    effort,
		MaxTokens: model.Model.MaxTokens,
	}
}

// streamStep receives one response, retrying transient failures and recovering
// context pressure at most once for the turn.
func (a *Agent) streamStep(ctx context.Context, emit func(any), compacted *bool) (provider.Result, *draftTracker, int64, stepTiming, error) {
	var thinkStart, thinkEnd time.Time
	var timing stepTiming
	var drafts *draftTracker
	var res provider.Result
	var err, prev error // prev: the failure of the attempt before
	var req provider.Request
	repeated := false   // err repeats prev: the server will say it again
	fromRequest := true // err is the request's failure (not a compaction's)
	for attempt := 1; ; {
		thinkStart, thinkEnd = time.Time{}, time.Time{}
		var first time.Time // first streamed output of this attempt
		output := func() {
			if first.IsZero() {
				first = time.Now()
			}
		}
		drafts = &draftTracker{emit: emit}
		h := provider.Handler{
			OnToolCallStart: func(i int) {
				output()
				drafts.start(i)
			},
			OnToolCallDelta: func(i int, s string) {
				output()
				drafts.delta(i, s)
			},
			OnReasoning: func(s string) {
				output()
				if thinkStart.IsZero() {
					thinkStart = time.Now()
				}
				emit(ReasoningDelta{s})
			},
			OnText: func(s string) {
				output()
				if !thinkStart.IsZero() && thinkEnd.IsZero() {
					thinkEnd = time.Now()
				}
				emit(TextDelta{s})
			},
		}
		var client provider.Streamer
		client, req = a.request()
		sent := time.Now()
		tctx, trace := ai.WithConnTrace(ctx)
		res, err = client.Stream(tctx, req, h)
		timing = stepTiming{}
		if !first.IsZero() {
			timing = stepTiming{TTFT: first.Sub(sent), Generation: time.Since(first)}
		}
		if ctx.Err() != nil {
			break
		}
		logReq := func(event string, wait time.Duration) {
			a.cfgMu.Lock()
			prov := a.model.ProviderName
			a.cfgMu.Unlock()
			e := requestLogEntry{Time: time.Now(), Event: event, Session: req.SessionID, Provider: prov, Model: req.Model,
				Attempt: attempt, WaitMs: wait.Milliseconds(), ElapsedMs: time.Since(sent).Milliseconds()}
			if err != nil {
				e.Error = err.Error()
			}
			if repeated {
				e.Note = "the same error twice in a row; not retrying"
			}
			if c, ok := trace.Last(); ok {
				e.Conn = &c
			}
			logRequest(e)
		}
		// An early length stop can mean the server ran out of context,
		// rather than output tokens. Leave 10% slack for provider accounting;
		// unknown usage is not enough to tell.
		earlyLength := err == nil && res.FinishReason == "length" && req.MaxTokens > 0 &&
			res.Usage.CompletionTokens > 0 && res.Usage.CompletionTokens < req.MaxTokens*9/10
		// Neither the failed reply nor its tool calls enter the context.
		if (ai.IsContextOverflow(err) || earlyLength) && !*compacted && len(a.messages) > 0 {
			*compacted = true
			drafts.endAll()
			why := "response ended before the output token limit"
			if err != nil {
				why = err.Error()
			}
			emit(StreamRetry{Attempt: 1, Of: 1, Err: why})
			if cerr := a.compact(ctx, emit, true); cerr != nil {
				if err == nil {
					err = cerr
				}
				fromRequest = false
				break
			}
			prev = nil
			continue
		}
		repeated = ai.RepeatedServerError(prev, err)
		if err == nil || ai.IsContextOverflow(err) || ai.IsPermanent(err) || repeated || attempt > streamRetries {
			switch {
			case err != nil:
				logReq(requestFailed, 0)
			case attempt > 1:
				logReq(requestRecovered, 0)
			}
			break
		}
		// Anything else may pass: drop what streamed and send it again.
		drafts.endAll()
		wait, werr := retryWait(err, attempt, maxRetryWait)
		if werr != nil {
			logReq(requestFailed, 0)
			err = werr
			break
		}
		logReq(requestRetry, wait)
		emit(StreamRetry{Attempt: attempt, Of: streamRetries, Wait: wait, Err: err.Error()})
		select {
		case <-time.After(wait):
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		if ai.IsConnectionError(err) {
			ai.ResetConnections()
		}
		prev = err
		attempt++
	}
	if err != nil && fromRequest && ctx.Err() == nil {
		err = explainFailure(err, req, repeated)
	}
	var thinkMs int64
	if !thinkStart.IsZero() {
		if thinkEnd.IsZero() {
			thinkEnd = time.Now()
		}
		thinkMs = thinkEnd.Sub(thinkStart).Milliseconds()
	}
	return res, drafts, thinkMs, timing, err
}

// imageText marks a failure that is about the image the request carried.
var imageText = regexp.MustCompile(`(?i)vision|image|encoder|mmproj`)

// failure is err with what the user can do about it added to its message;
// the error it wraps still answers errors.As and ai.StatusOf.
type failure struct {
	err  error
	hint string
}

func (f *failure) Error() string { return f.err.Error() + " (" + f.hint + ")" }
func (f *failure) Unwrap() error { return f.err }

// explainFailure says in err's message what a retry of the turn would hit:
// that the server gave the same answer twice and was not asked again, and,
// when the request carried images and the failure is about them, that the
// server could not process the image. The image is never dropped for the
// user: the message stays in the conversation to be sent again.
func explainFailure(err error, req provider.Request, repeated bool) error {
	var hints []string
	if repeated {
		hints = append(hints, "the server gave the same error twice in a row, so atto did not try again")
	}
	if imageText.MatchString(err.Error()) && requestHasImages(req) {
		hints = append(hints, "the server could not process the image; send the message again without it")
	}
	if len(hints) == 0 {
		return err
	}
	return &failure{err: err, hint: strings.Join(hints, "; ")}
}

func requestHasImages(req provider.Request) bool {
	for _, m := range req.Messages {
		if len(m.Images) > 0 {
			return true
		}
	}
	return false
}

// stepTiming splits a response's time: TTFT from sending the request to its
// first streamed output (reasoning, text or a tool call), Generation from
// there to the end of the stream. Zero when nothing streamed.
type stepTiming struct {
	TTFT, Generation time.Duration
}

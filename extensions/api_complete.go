//go:build !noext

package extensions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dop251/goja"
)

const (
	defaultCompleteTimeout = 30 * time.Second
	// defaultCompleting is how many atto.complete requests one extension has
	// in flight unless it calls atto.setCompleteConcurrency: local servers
	// get slower, and may hang, when asked in parallel. More wait their turn.
	defaultCompleting = 1
	maxCompleting     = 16
)

// jsComplete is atto.complete({model, prompt, system, maxTokens, timeoutMs}):
// one request to a model of the user's models.json, with no tools, no
// streaming and no part of the conversation. It resolves to {text}.
// The timeout covers the request, not the wait for a free slot.
func (e *ext) jsComplete(opts *goja.Object) goja.Value {
	model := strings.TrimSpace(optString(opts, "model"))
	prompt := optString(opts, "prompt")
	system := optString(opts, "system")
	maxTokens := int(optNumber(opts, "maxTokens"))
	effort := strings.TrimSpace(optString(opts, "reasoningEffort"))
	timeout := defaultCompleteTimeout
	if ms := optNumber(opts, "timeoutMs"); ms > 0 {
		timeout = time.Duration(ms * float64(time.Millisecond))
	}
	switch {
	case model == "":
		return e.rejected(errors.New(`atto.complete: pass {model: "provider/id", prompt}`))
	case prompt == "":
		return e.rejected(errors.New("atto.complete: prompt is empty"))
	}
	ctx := e.requestContext()
	sid, _ := e.m.session()
	return e.async(func() (func() goja.Value, error) {
		if !e.acquire(ctx) {
			return nil, errors.New(canceledMsg)
		}
		defer e.release()
		start := time.Now()
		ref, text, err := complete(ctx, model, system, prompt, effort, maxTokens, timeout, sid)
		outcome := "ok"
		if err != nil {
			outcome = "failed: " + err.Error()
		}
		e.countComplete(orDefault(ref, model), err != nil)
		e.m.log(e.spec.Name, fmt.Sprintf("complete %s %s in %s (prompt %d chars, reply %d chars)",
			orDefault(ref, model), outcome, time.Since(start).Round(time.Millisecond), len(system)+len(prompt), len(text)))
		if err != nil {
			return nil, err
		}
		return func() goja.Value { return e.object(map[string]any{"text": text}) }, nil
	})
}

// jsSetCompleteConcurrency is atto.setCompleteConcurrency(n): how many
// atto.complete requests this extension has in flight (1 to 16; default 1).
func (e *ext) jsSetCompleteConcurrency(n int) {
	if n < 1 || n > maxCompleting {
		panic(e.vm.NewTypeError("atto.setCompleteConcurrency: n must be from 1 to %d", maxCompleting))
	}
	e.slotMu.Lock()
	e.limit = n
	e.wakeSlots()
	e.slotMu.Unlock()
}

// wakeSlots wakes the requests waiting for a slot. With slotMu held.
func (e *ext) wakeSlots() {
	if e.slotWake != nil {
		close(e.slotWake)
		e.slotWake = nil
	}
}

// acquire waits for a free slot; false if ctx ended first.
func (e *ext) acquire(ctx context.Context) bool {
	for {
		e.slotMu.Lock()
		limit := e.limit
		if limit < 1 {
			limit = defaultCompleting
		}
		if e.inflight < limit {
			e.inflight++
			e.slotMu.Unlock()
			return true
		}
		if e.slotWake == nil {
			e.slotWake = make(chan struct{})
		}
		wake := e.slotWake
		e.slotMu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return false
		}
	}
}

func (e *ext) release() {
	e.slotMu.Lock()
	e.inflight--
	e.wakeSlots()
	e.slotMu.Unlock()
}

// requestContext is the context requests made now run under; it ends when
// the session does or extensions reload.
func (e *ext) requestContext() context.Context {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.reqCtx == nil {
		e.reqCtx, e.reqCancel = context.WithCancel(context.Background())
	}
	return e.reqCtx
}

// cancelRequests cancels the requests in flight and queued (the session
// ended); later ones run under a new context.
func (e *ext) cancelRequests() {
	e.mu.Lock()
	cancel := e.reqCancel
	e.reqCtx, e.reqCancel = nil, nil
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (e *ext) countComplete(model string, failed bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.completes {
		if e.completes[i].Model == model {
			e.completes[i].Calls++
			if failed {
				e.completes[i].Failed++
			}
			return
		}
	}
	st := CompleteStat{Model: model, Calls: 1}
	if failed {
		st.Failed = 1
	}
	e.completes = append(e.completes, st)
}

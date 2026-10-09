//go:build !noext

package extensions

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"
)

// ext is one loaded extension: a goja runtime and the goroutine that owns
// it. goja is not safe for concurrent use, so every piece of the
// extension's JavaScript (its factory, handlers, timer callbacks, promise
// continuations) runs as a job on that goroutine, one at a time.
type ext struct {
	m    *Manager
	spec Spec
	hash string
	vm   *goja.Runtime

	q        jobQueue
	stop     chan struct{} // closed to end the loop
	stopped  chan struct{} // closed when the loop has ended
	stopOnce sync.Once

	// Only touched on the loop.
	handlers      map[string][]goja.Callable
	disposers     []goja.Callable
	cmdFns        map[string]goja.Callable
	ctxObj        *goja.Object
	renderContext context.Context
	actionClient  string
	toastSeq      int64

	// interrupted is set by the watchdog when a job ran too long; busy
	// while a job runs.
	interrupted atomic.Bool
	busy        atomic.Bool
	// asking counts open dialogs: waiting for the user does not count
	// against handler timeouts.
	asking atomic.Int32

	mu        sync.Mutex
	status    string // Loaded, Failed...
	err       string
	commands  []Command
	events    []string
	timers    map[int64]*time.Timer
	nextTimer int64

	// atto.complete: slots limit the requests in flight; reqCtx is
	// canceled when the session ends or the extension stops (see
	// api_complete.go); completes counts the requests made, per model.
	slotMu    sync.Mutex // guards limit, inflight and slotWake
	limit     int
	inflight  int
	slotWake  chan struct{}
	reqCtx    context.Context
	reqCancel context.CancelFunc
	completes []CompleteStat
}

func newExt(m *Manager, s Spec, code string) *ext {
	e := &ext{
		m: m, spec: s, hash: hash(code), vm: goja.New(),
		stop: make(chan struct{}), stopped: make(chan struct{}),
		handlers: map[string][]goja.Callable{}, cmdFns: map[string]goja.Callable{},
		timers: map[int64]*time.Timer{}, status: Loaded,
	}
	e.vm.SetAsyncContextTracker(&uiContextTracker{e: e})
	e.q.wake = make(chan struct{}, 1)
	return e
}

// jobQueue is an unbounded FIFO, so posting never blocks (a job may post
// another, and goroutines finishing exec or fetch post their results).
type jobQueue struct {
	mu   sync.Mutex
	jobs []func()
	wake chan struct{}
}

func (q *jobQueue) push(fn func()) {
	q.mu.Lock()
	q.jobs = append(q.jobs, fn)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *jobQueue) pop() (func(), bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.jobs) == 0 {
		return nil, false
	}
	fn := q.jobs[0]
	q.jobs[0] = nil
	q.jobs = q.jobs[1:]
	return fn, true
}

func (e *ext) loop() {
	defer close(e.stopped)
	for {
		if fn, ok := e.q.pop(); ok {
			select {
			case <-e.stop:
				return
			default:
			}
			e.run(fn)
			continue
		}
		select {
		case <-e.stop:
			return
		case <-e.q.wake:
		}
	}
}

// run runs one job under the watchdog: script that runs longer than the
// timeout without yielding is interrupted and the extension fails. A Go
// panic fails it too; atto goes on.
func (e *ext) run(fn func()) {
	e.busy.Store(true)
	defer e.busy.Store(false)
	dog := time.AfterFunc(e.m.timeout(), func() {
		e.interrupted.Store(true)
		e.vm.Interrupt(errRunaway)
	})
	defer func() {
		if !dog.Stop() {
			e.vm.ClearInterrupt()
		}
		if r := recover(); r != nil {
			e.fail(fmt.Sprintf("panic: %v", r))
			e.m.log(e.spec.Name, fmt.Sprintf("panic: %v\n%s", r, debug.Stack()))
			return
		}
		if e.interrupted.Load() {
			e.fail(fmt.Sprintf("script ran for more than %s without yielding; interrupted", e.m.timeout()))
		}
	}()
	fn()
}

var errRunaway = errors.New("interrupted: ran too long")

// errStopped: the extension stopped (failed, reloaded or closed) before
// it answered.
var errStopped = errors.New("extension stopped")

// errTimeout: a handler's promise did not settle in time.
type errTimeout struct{ d time.Duration }

func (e errTimeout) Error() string { return fmt.Sprintf("no answer within %s; ignored", e.d) }

// post queues fn on the loop; false if the extension stopped.
func (e *ext) post(fn func()) bool {
	if e.dead() {
		return false
	}
	e.q.push(fn)
	return true
}

func (e *ext) dead() bool {
	select {
	case <-e.stop:
		return true
	default:
		return false
	}
}

// await runs fn on the loop and returns what it returns (exported to Go),
// after the promise settles if it is one. timeout bounds the wait, not
// counting time the user spends answering a dialog of this extension;
// ctx ends it early.
func (e *ext) await(ctx context.Context, timeout time.Duration, fn func() (goja.Value, error)) (any, error) {
	type result struct {
		v   any
		err error
	}
	ch := make(chan result, 1)
	var once sync.Once
	done := func(v any, err error) { once.Do(func() { ch <- result{v, err} }) }
	if !e.post(func() {
		v, err := fn()
		if err != nil {
			done(nil, err)
			return
		}
		e.settle(v, done)
	}) {
		return nil, errStopped
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	// Script still running when the time is up is the watchdog's: it
	// interrupts it within the timeout. Wait for that, up to a limit, so
	// the caller learns the extension failed.
	extra := 3
	for {
		select {
		case r := <-ch:
			return r.v, r.err
		case <-e.stopped:
			select { // it may have answered just before it stopped
			case r := <-ch:
				return r.v, r.err
			default:
			}
			return nil, errStopped
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
			if e.asking.Load() > 0 || (e.busy.Load() && extra > 0) {
				if e.asking.Load() == 0 {
					extra--
				}
				t.Reset(timeout)
				continue
			}
			return nil, errTimeout{timeout}
		}
	}
}

// settle calls done with v, or with what v resolves to when it is a
// promise. On the loop.
func (e *ext) settle(v goja.Value, done func(any, error)) {
	p, ok := exported(v).(*goja.Promise)
	if !ok {
		done(exported(v), nil)
		return
	}
	switch p.State() {
	case goja.PromiseStateFulfilled:
		done(exported(p.Result()), nil)
		return
	case goja.PromiseStateRejected:
		done(nil, e.rejection(p.Result()))
		return
	}
	then, ok := goja.AssertFunction(v.ToObject(e.vm).Get("then"))
	if !ok {
		done(nil, errors.New("not a promise"))
		return
	}
	_, err := then(v,
		e.vm.ToValue(func(c goja.FunctionCall) goja.Value { done(exported(c.Argument(0)), nil); return goja.Undefined() }),
		e.vm.ToValue(func(c goja.FunctionCall) goja.Value { done(nil, e.rejection(c.Argument(0))); return goja.Undefined() }))
	if err != nil {
		done(nil, err)
	}
}

func exported(v goja.Value) any {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil
	}
	return v.Export()
}

// rejection makes an error of a rejected promise's value, with the
// stack when it is an Error.
func (e *ext) rejection(v goja.Value) error {
	if v == nil {
		return errors.New("rejected")
	}
	if o, ok := v.(*goja.Object); ok {
		if st := o.Get("stack"); st != nil && !goja.IsUndefined(st) && st.String() != "" {
			return errors.New(cleanStack(st.String()))
		}
	}
	return errors.New(v.String())
}

// jsError is an error from running script, as text: the message and
// where it was thrown, in the original file.
func jsError(err error) string {
	if ex, ok := errors.AsType[*goja.Exception](err); ok {
		if o, ok := ex.Value().(*goja.Object); ok {
			if st := o.Get("stack"); st != nil && !goja.IsUndefined(st) && st.String() != "" {
				return cleanStack(st.String())
			}
		}
		return cleanStack(ex.Error())
	}
	return err.Error()
}

// cleanStack keeps a stack trace short: the message and the first frame
// in the extension's own files.
func cleanStack(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	out := []string{lines[0]}
	for _, l := range lines[1:] {
		l = strings.TrimSpace(l)
		if strings.Contains(l, "native") || l == "" {
			continue
		}
		out = append(out, l)
		break
	}
	return strings.Join(out, " ")
}

// call calls fn with args on the loop, reporting a throw (or a rejected
// promise it returns) as a notice. For callbacks nobody waits for: timers,
// events that only observe, commands.
func (e *ext) call(what string, fn goja.Callable, args ...goja.Value) {
	v, err := fn(goja.Undefined(), args...)
	if err != nil {
		e.report(what, err)
		return
	}
	e.settle(v, func(_ any, err error) {
		if err != nil {
			e.report(what, err)
		}
	})
}

// report shows an error a handler or callback threw. An interrupt is not
// reported here: run fails the extension for it.
func (e *ext) report(what string, err error) {
	var ie *goja.InterruptedError
	if errors.As(err, &ie) || e.interrupted.Load() {
		return
	}
	msg := what + ": " + jsError(err)
	if strings.Contains(msg, canceledMsg) { // the session moved on: nobody is waiting
		return
	}
	e.m.log(e.spec.Name, msg)
	e.m.host().Notify(e.spec.Name, msg, "error")
}

// fail disables the extension: it stops running and its status items
// and widgets go. Safe from any goroutine.
func (e *ext) fail(reason string) {
	e.mu.Lock()
	if e.status != Loaded {
		e.mu.Unlock()
		return
	}
	e.status, e.err = Failed, reason
	e.mu.Unlock()
	e.halt()
	e.m.log(e.spec.Name, "disabled: "+reason)
	e.m.host().Notify(e.spec.Name, "extension disabled: "+reason, "error")
}

// halt ends the loop and the timers and removes the extension's UI.
func (e *ext) halt() {
	e.stopOnce.Do(func() {
		close(e.stop)
		e.cancelRequests()
		e.mu.Lock()
		for id, t := range e.timers {
			t.Stop()
			delete(e.timers, id)
		}
		e.mu.Unlock()
		e.m.host().DisposeUI(e.spec.Name)
	})
}

// dispose runs the onDispose callbacks (bounded by a second), then halts.
// A loop stuck in script is interrupted.
func (e *ext) dispose() {
	if !e.dead() {
		_, _ = e.await(context.Background(), time.Second, func() (goja.Value, error) {
			var ps []goja.Value
			for _, fn := range e.disposers {
				v, err := fn(goja.Undefined())
				if err != nil {
					e.report("onDispose", err)
					continue
				}
				ps = append(ps, v)
			}
			// Wait for async disposers together.
			all, _ := goja.AssertFunction(e.vm.Get("Promise").ToObject(e.vm).Get("all"))
			return all(e.vm.Get("Promise"), e.vm.ToValue(ps))
		})
	}
	e.halt()
	select {
	case <-e.stopped:
	case <-time.After(100 * time.Millisecond):
		e.vm.Interrupt(errStopped)
	}
}

func (e *ext) setStatus(status, err string) {
	e.mu.Lock()
	e.status, e.err = status, err
	e.mu.Unlock()
}

func (e *ext) live() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status == Loaded && !e.dead()
}

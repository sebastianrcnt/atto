//go:build !noext

package extensions

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/dop251/goja"
)

// boot runs the bundled script and calls its default export with the
// atto object. On the loop.
func (e *ext) boot(code string) (goja.Value, error) {
	vm := e.vm
	vm.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))
	atto := e.install()
	module := vm.NewObject()
	exports := vm.NewObject()
	_ = module.Set("exports", exports)
	_ = vm.Set("module", module)
	_ = vm.Set("exports", exports)
	_ = vm.Set("require", func(c goja.FunctionCall) goja.Value {
		panic(vm.NewTypeError("require(%q): only relative imports are bundled; Node and npm modules are not available", c.Argument(0).String()))
	})
	if _, err := vm.RunScript(e.spec.Path, code); err != nil {
		return nil, err
	}
	exp := module.Get("exports")
	fn, ok := goja.AssertFunction(exp)
	if !ok && exp != nil {
		fn, ok = goja.AssertFunction(exp.ToObject(vm).Get("default"))
	}
	if !ok {
		return nil, errors.New("no default export: write export default function (atto) { ... }")
	}
	return fn(goja.Undefined(), atto)
}

// install builds the atto object, the handler context and the globals
// (timers, console, fetch).
func (e *ext) install() *goja.Object {
	vm := e.vm
	ui := e.uiObject()
	session := e.sessionObject()

	ctx := vm.NewObject()
	_ = ctx.Set("ui", ui)
	_ = ctx.Set("hasUI", e.m.host().HasUI())
	_ = ctx.Set("cwd", e.m.cwd)
	_ = ctx.Set("session", session)
	e.ctxObj = ctx

	atto := vm.NewObject()
	_ = atto.Set("on", e.jsOn)
	_ = atto.Set("registerCommand", e.jsRegisterCommand)
	_ = atto.Set("onDispose", func(v goja.Value) {
		e.readOnlyRender()
		fn, ok := goja.AssertFunction(v)
		if !ok {
			panic(e.vm.NewTypeError("atto.onDispose: pass a function"))
		}
		e.disposers = append(e.disposers, fn)
	})
	_ = atto.Set("exec", e.jsExec)
	_ = atto.Set("fs", e.fsObject())
	_ = atto.Set("fetch", e.jsFetch)
	_ = atto.Set("complete", e.jsComplete)
	_ = atto.Set("setCompleteConcurrency", e.jsSetCompleteConcurrency)
	_ = atto.Set("mcp", e.mcpObject())
	_ = atto.Set("sendMessage", func(text string) { e.readOnlyRender(); e.m.host().SendMessage(text) })
	_ = atto.Set("log", e.jsLog)
	_ = atto.Set("session", session)
	_ = atto.Set("ui", ui)
	_ = atto.Set("store", e.storeObject())
	_ = atto.Set("cwd", e.m.cwd)
	_ = atto.Set("name", e.spec.Name)

	console := vm.NewObject()
	for _, k := range []string{"log", "info", "warn", "error", "debug"} {
		_ = console.Set(k, e.jsLog)
	}
	_ = vm.Set("console", console)
	_ = vm.Set("fetch", e.jsFetch)
	e.installTimers()
	return atto
}

// eventNames are the events atto.on accepts.
var eventNames = []string{"session_start", "session_end", "turn_start", "turn_end", "tool_call", "tool_result", "user_prompt", "message_end", "reasoning_end", "step_end"}

func (e *ext) jsOn(event string, v goja.Value) {
	e.readOnlyRender()
	if !slices.Contains(eventNames, event) {
		panic(e.vm.NewTypeError("atto.on: unknown event %q (events: %s)", event, strings.Join(eventNames, ", ")))
	}
	fn, ok := goja.AssertFunction(v)
	if !ok {
		panic(e.vm.NewTypeError("atto.on(%q): the handler must be a function", event))
	}
	e.handlers[event] = append(e.handlers[event], fn)
	e.mu.Lock()
	if !slices.Contains(e.events, event) {
		e.events = append(e.events, event)
	}
	e.mu.Unlock()
}

var commandName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_-]*$`)

func (e *ext) jsRegisterCommand(name string, spec *goja.Object) {
	e.readOnlyRender()
	if !commandName.MatchString(name) {
		panic(e.vm.NewTypeError("registerCommand: %q is not a command name (letters, digits, -, _, :; no leading /)", name))
	}
	if spec == nil {
		panic(e.vm.NewTypeError("registerCommand(%q): pass {description, handler}", name))
	}
	fn, ok := goja.AssertFunction(spec.Get("handler"))
	if !ok {
		panic(e.vm.NewTypeError("registerCommand(%q): handler must be a function", name))
	}
	desc := ""
	if d := spec.Get("description"); d != nil && !goja.IsUndefined(d) {
		desc = d.String()
	}
	e.cmdFns[name] = fn
	e.mu.Lock()
	defer e.mu.Unlock()
	e.commands = slices.DeleteFunc(e.commands, func(c Command) bool { return c.Name == name })
	e.commands = append(e.commands, Command{Name: name, Description: desc, Ext: e.spec.Name})
	e.m.cmdVer.Add(1)
}

func (e *ext) jsLog(c goja.FunctionCall) goja.Value {
	var parts []string
	for _, a := range c.Arguments {
		parts = append(parts, e.show(a))
	}
	e.m.log(e.spec.Name, strings.Join(parts, " "))
	return goja.Undefined()
}

// show formats a value for the log: strings as is, objects as JSON.
func (e *ext) show(v goja.Value) string {
	if o, ok := v.(*goja.Object); ok {
		if st := o.Get("stack"); st != nil && !goja.IsUndefined(st) {
			return st.String()
		}
		if b, err := o.MarshalJSON(); err == nil {
			return string(b)
		}
	}
	return v.String()
}

func (e *ext) sessionObject() *goja.Object {
	vm := e.vm
	o := vm.NewObject()
	get := func(name string, fn func() string) {
		_ = o.DefineAccessorProperty(name, vm.ToValue(fn), nil, goja.FLAG_FALSE, goja.FLAG_TRUE)
	}
	get("id", func() string { id, _ := e.m.session(); return id })
	get("model", func() string { _, model := e.m.session(); return model })
	_ = o.Set("cwd", e.m.cwd)
	get("name", func() string { name, _ := e.m.sessionText(0); return name })
	// messages(limit) is the conversation's text so far, oldest first: the
	// user's messages and the model's answers, without commands and their
	// output.
	_ = o.Set("messages", func(c goja.FunctionCall) goja.Value {
		e.readOnlyRender()
		limit := 50
		if v := c.Argument(0); !goja.IsUndefined(v) && !goja.IsNull(v) {
			limit = int(v.ToInteger())
		}
		_, msgs := e.m.sessionText(max(0, limit))
		out := make([]any, len(msgs))
		for i, m := range msgs {
			// An object, not a Go map: a map's keys come out in random order.
			mo := vm.NewObject()
			_ = mo.Set("role", m.Role)
			_ = mo.Set("text", m.Content)
			out[i] = mo
		}
		return vm.ToValue(out)
	})
	_ = o.Set("setName", func(name string) {
		e.readOnlyRender()
		name = strings.Join(strings.Fields(name), " ")
		if name == "" {
			panic(vm.NewTypeError("the name is empty"))
		}
		if err := e.m.host().SetSessionName(e.spec.Name, name); err != nil {
			panic(vm.NewGoError(err))
		}
	})
	return o
}

// installTimers defines setTimeout, setInterval and their clear
// functions. Callbacks run on the loop; reloading stops them all.
func (e *ext) installTimers() {
	vm := e.vm
	add := func(repeat bool) func(goja.FunctionCall) goja.Value {
		return func(c goja.FunctionCall) goja.Value {
			e.readOnlyRender()
			fn, ok := goja.AssertFunction(c.Argument(0))
			if !ok {
				panic(vm.NewTypeError("the callback must be a function"))
			}
			d := time.Duration(c.Argument(1).ToFloat() * float64(time.Millisecond))
			if d < 0 || d != d {
				d = 0
			}
			if repeat && d < time.Millisecond {
				d = time.Millisecond
			}
			args := slices.Clone(c.Arguments[min(2, len(c.Arguments)):])
			e.mu.Lock()
			e.nextTimer++
			id := e.nextTimer
			var fire func()
			fire = func() {
				e.post(func() {
					e.mu.Lock()
					_, ok := e.timers[id]
					if ok && repeat {
						e.timers[id] = time.AfterFunc(d, fire)
					} else {
						delete(e.timers, id)
					}
					e.mu.Unlock()
					if ok {
						e.call("timer", fn, args...)
					}
				})
			}
			e.timers[id] = time.AfterFunc(d, fire)
			e.mu.Unlock()
			return vm.ToValue(id)
		}
	}
	clear := func(c goja.FunctionCall) goja.Value {
		e.readOnlyRender()
		id := c.Argument(0).ToInteger()
		e.mu.Lock()
		if t, ok := e.timers[id]; ok {
			t.Stop()
			delete(e.timers, id)
		}
		e.mu.Unlock()
		return goja.Undefined()
	}
	_ = vm.Set("setTimeout", add(false))
	_ = vm.Set("setInterval", add(true))
	_ = vm.Set("clearTimeout", clear)
	_ = vm.Set("clearInterval", clear)
}

// async runs work on its own goroutine and returns a promise of its
// result: build runs on the loop and makes the JavaScript value.
func (e *ext) async(work func() (func() goja.Value, error)) goja.Value {
	p, resolve, reject := e.vm.NewPromise()
	go func() {
		build, err := work()
		e.post(func() {
			if err != nil {
				_ = reject(e.vm.NewGoError(err))
				return
			}
			_ = resolve(build())
		})
	}()
	return e.vm.ToValue(p)
}

// optString reads an optional string property.
func optString(o *goja.Object, key string) string {
	if o == nil {
		return ""
	}
	if v := o.Get(key); v != nil && !goja.IsUndefined(v) && !goja.IsNull(v) {
		return v.String()
	}
	return ""
}

// optNumber reads an optional number property (0 when missing).
func optNumber(o *goja.Object, key string) float64 {
	if o == nil {
		return 0
	}
	if v := o.Get(key); v != nil && !goja.IsUndefined(v) && !goja.IsNull(v) {
		return v.ToFloat()
	}
	return 0
}

func errorf(format string, args ...any) error { return fmt.Errorf(format, args...) }

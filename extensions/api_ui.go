//go:build !noext

package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"
	"github.com/sebastianrcnt/atto/ui"
)

// Authoring objects are opaque Go values. Engine references retain their Go
// provenance, and JavaScript cannot forge wire nodes or another owner's keys.
type drawing struct {
	node      ui.Node
	children  []*drawing
	callbacks map[ui.EventType]goja.Callable
}

func (e *ext) readOnlyRender() {
	if e.renderContext != nil {
		panic(e.vm.NewTypeError("render hooks cannot write state or do I/O"))
	}
}
func (e *ext) uiPromise(work func(*ui.Registry) error) goja.Value {
	e.readOnlyRender()
	p, resolve, reject := e.vm.NewPromise()
	UIWork(e.m.host(), func(r *ui.Registry) error {
		if e.dead() {
			return errStopped
		}
		return work(r)
	}, func(err error) {
		e.post(func() {
			if err != nil {
				_ = reject(e.vm.NewGoError(err))
			} else {
				_ = resolve(goja.Undefined())
			}
		})
	})
	return e.vm.ToValue(p)
}
func (e *ext) localMatch(o *goja.Object, optional bool) ui.Match {
	m := ui.Match{Site: ui.Site(optString(o, "site")), ID: optString(o, "id")}
	if (!optional || m.Site != "") && !ui.ValidSite(m.Site) {
		panic(e.vm.NewTypeError("invalid UI site"))
	}
	if m.ID != "" {
		if strings.Contains(m.ID, "/") || !ui.ValidID(e.spec.Name+"/"+m.ID) {
			panic(e.vm.NewTypeError("invalid provider-local UI id"))
		}
		m.ID = e.spec.Name + "/" + m.ID
	}
	return m
}
func (e *ext) uiObject() *goja.Object {
	vm := e.vm
	o := vm.NewObject()
	_ = o.Set("resolve", func(_ goja.Value) *goja.Object {
		catalog := vm.NewObject()
		for _, name := range []string{"Box", "Text", "Markdown", "Code", "Diff", "Link", "Button", "Input", "Select", "List", "Table", "Progress", "Collapse", "Image"} {
			_ = catalog.Set(name, func(c goja.FunctionCall) goja.Value {
				return vm.ToValue(e.construct(name, c.Argument(0), c.Arguments[min(1, len(c.Arguments)):]))
			})
		}
		return catalog
	})
	_ = o.Set("Fragment", func(c goja.FunctionCall) goja.Value {
		return vm.ToValue(e.construct("Box", c.Argument(0), c.Arguments[min(1, len(c.Arguments)):]))
	})
	_ = o.Set("jsx", func(c goja.FunctionCall) goja.Value {
		fn, ok := goja.AssertFunction(c.Argument(0))
		if !ok {
			panic(vm.NewTypeError("jsx requires a constructor"))
		}
		v, err := fn(goja.Undefined(), c.Arguments[1:]...)
		if err != nil {
			panic(err)
		}
		return v
	})
	_ = o.Set("render", func(match *goja.Object, value goja.Value) goja.Value {
		e.readOnlyRender()
		m := e.localMatch(match, false)
		fn, ok := goja.AssertFunction(value)
		if !ok {
			panic(vm.NewTypeError("render requires a function"))
		}
		var disposed atomic.Bool
		var dispose ui.Dispose
		UIWork(e.m.host(), func(r *ui.Registry) error {
			if disposed.Load() || e.dead() {
				return nil
			}
			dispose = r.Render(e.spec.Name, m, func(ev ui.Event, next ui.Next) (*ui.Node, error) { return e.renderUI(r, ev, next, fn) })
			// Disposal itself is always queued; no JavaScript crosses the worker lane.

			r.Invalidate(m)
			return nil
		}, func(err error) {
			if err != nil {
				e.m.log(e.spec.Name, err.Error())
			}
		})
		return vm.ToValue(func() {
			if disposed.CompareAndSwap(false, true) {
				UIWork(e.m.host(), func(r *ui.Registry) error {
					if dispose != nil {
						dispose()
					}
					return nil
				}, func(error) {})
			}
		})
	})
	_ = o.Set("open", func(opts *goja.Object) goja.Value {
		m := e.localMatch(opts, false)
		if m.ID == "" || ui.IsItem(m.Site) || m.Site == ui.Toast {
			panic(vm.NewTypeError("open requires an owned non-item site/id"))
		}
		for _, key := range []string{"columns", "rows", "priority"} {
			if v := opts.Get(key); v != nil && !goja.IsUndefined(v) {
				n, ok := v.Export().(int64)
				if !ok {
					f, yes := v.Export().(float64)
					if !yes || math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) {
						panic(vm.NewTypeError("%s must be an integer", key))
					}
					n = int64(f)
				}
				if key != "priority" && n < 1 {
					panic(vm.NewTypeError("%s must be positive", key))
				}
			}
		}
		options := ui.OpenOptions{Site: m.Site, ID: m.ID, Title: optString(opts, "title"), Placement: optString(opts, "placement"), Columns: int(optNumber(opts, "columns")), Rows: int(optNumber(opts, "rows")), Priority: int(optNumber(opts, "priority")), Align: optString(opts, "align"), CloseOnEscape: optBool(opts, "closeOnEscape")}
		if optBool(opts, "focus") {
			options.FocusClientID = e.actionClient
		}
		if options.Title == "" {
			options.Title = optString(opts, "id")
		}
		return e.uiPromise(func(r *ui.Registry) error { return r.OpenDefault(e.spec.Name, options, openProps(options), nil) })
	})
	_ = o.Set("close", func(opts *goja.Object) goja.Value {
		m := e.localMatch(opts, false)
		if m.ID == "" || ui.IsItem(m.Site) {
			panic(vm.NewTypeError("close requires an owned non-item site/id"))
		}
		return e.uiPromise(func(r *ui.Registry) error { return r.Close(e.spec.Name, m.Site, m.ID) })
	})
	_ = o.Set("invalidate", func(opts *goja.Object) {
		e.readOnlyRender()
		m := e.localMatch(opts, true)
		all := opts == nil || m.ID == ""
		UIWork(e.m.host(), func(r *ui.Registry) error {
			if e.dead() {
				return errStopped
			}
			if all {
				r.InvalidateOwner(e.spec.Name, m)
			} else {
				r.Invalidate(m)
			}
			return nil
		}, func(error) {})
	})
	_ = o.Set("toast", func(text string, opts *goja.Object) goja.Value {
		level := optString(opts, "level")
		if level == "" {
			level = "info"
		}
		if level != "info" && level != "warning" && level != "error" {
			panic(vm.NewTypeError("invalid toast level"))
		}
		ms := optNumber(opts, "timeoutMs")
		if opts == nil || opts.Get("timeoutMs") == nil || goja.IsUndefined(opts.Get("timeoutMs")) {
			ms = 4000
		}
		if ms < 500 || ms > 30000 {
			panic(vm.NewTypeError("toast timeout must be 500..30000 ms"))
		}
		e.toastSeq++
		options := ui.OpenOptions{Site: ui.Toast, ID: fmt.Sprintf("%s/toast-%d", e.spec.Name, e.toastSeq), Level: level, ExpiresAt: time.Now().Add(time.Duration(ms) * time.Millisecond).UnixMilli()}
		n := ui.Text(ui.TextProps{Text: text})
		return e.uiPromise(func(r *ui.Registry) error {
			return r.OpenDefault(e.spec.Name, options, map[string]any{"level": level, "expiresAt": options.ExpiresAt}, &n)
		})
	})
	_ = o.Set("notify", func(text string, level goja.Value) {
		e.readOnlyRender()
		lv := "info"
		if level != nil && !goja.IsUndefined(level) {
			lv = level.String()
		}
		if lv != "info" && lv != "warning" && lv != "error" {
			panic(vm.NewTypeError("invalid notice level"))
		}
		e.m.host().Notify(e.spec.Name, text, lv)
	})
	_ = o.Set("select", func(title string, options []string) goja.Value {
		return e.ask(Question{Kind: "select", Title: title, Options: options})
	})
	_ = o.Set("confirm", func(text string) goja.Value { return e.ask(Question{Kind: "confirm", Title: text}) })
	_ = o.Set("input", func(text string) goja.Value { return e.ask(Question{Kind: "input", Title: text}) })
	return o
}
func optBool(o *goja.Object, key string) bool {
	return o != nil && o.Get(key) != nil && o.Get(key).ToBoolean()
}
func openProps(o ui.OpenOptions) map[string]any {
	columns, rows := o.Columns, o.Rows
	if columns == 0 {
		columns = 40
	}
	if rows == 0 {
		rows = 8
	}
	placement := o.Placement
	if placement == "" {
		placement = "auto"
	}
	align := o.Align
	if align == "" {
		align = "start"
	}
	switch o.Site {
	case ui.Pane:
		return map[string]any{"title": o.Title, "placement": placement, "columns": columns, "rows": rows, "closeOnEscape": o.CloseOnEscape}
	case ui.Status:
		return map[string]any{"busy": false, "priority": o.Priority, "align": align}
	case ui.Band:
		return map[string]any{"busy": false}
	case ui.Transcript:
		return map[string]any{"title": o.Title, "entryId": ""}
	case ui.Dialog:
		return map[string]any{"kind": "custom", "title": o.Title}
	default:
		return nil
	}
}
func (e *ext) construct(name string, props goja.Value, children []goja.Value) *drawing {
	d := &drawing{node: ui.Node{Type: name, Props: map[string]any{}}, callbacks: map[ui.EventType]goja.Callable{}}
	if name == "Table" {
		d.node.Type = "List"
		d.node.Props["mode"] = "table"
	}
	if props != nil && !goja.IsUndefined(props) && !goja.IsNull(props) {
		o := props.ToObject(e.vm)
		for _, key := range o.Keys() {
			v := o.Get(key)
			switch key {
			case "key":
				d.node.Key = v.String()
				if strings.Contains(d.node.Key, "/") {
					panic(e.vm.NewTypeError("keys must be provider-local"))
				}
			case "children":
				e.addChildren(d, v)
			case "onPress", "onInput", "onSubmit", "onSelect":
				fn, ok := goja.AssertFunction(v)
				if !ok {
					panic(e.vm.NewTypeError("%s must be a function", key))
				}
				kind := map[string]ui.EventType{"onPress": ui.Press, "onInput": ui.InputEvent, "onSubmit": ui.Submit, "onSelect": ui.SelectEvent}[key]
				d.callbacks[kind] = fn
				d.node.Events = append(d.node.Events, kind)
			default:
				if !goja.IsUndefined(v) {
					d.node.Props[key] = v.Export()
				}
			}
		}
	}
	for _, v := range children {
		e.addChildren(d, v)
	}
	return d
}
func (e *ext) addChildren(d *drawing, v goja.Value) {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return
	}
	if child, ok := v.Export().(*drawing); ok {
		d.children = append(d.children, child)
		return
	}
	if child, ok := v.Export().(*ui.Node); ok {
		d.children = append(d.children, &drawing{node: *child})
		return
	}
	if o, ok := v.(*goja.Object); ok && o.ClassName() == "Array" {
		for i := 0; i < int(o.Get("length").ToInteger()); i++ {
			e.addChildren(d, o.Get(fmt.Sprint(i)))
		}
		return
	}
	if _, ok := v.Export().(string); !ok {
		panic(e.vm.NewTypeError("children must be elements or strings"))
	}
	d.children = append(d.children, &drawing{node: ui.Text(ui.TextProps{Text: v.String()})})
}
func (e *ext) renderUI(r *ui.Registry, ev ui.Event, next ui.Next, fn goja.Callable) (*ui.Node, error) {
	ctx := ev.Context
	if ctx == nil {
		ctx = context.Background()
	}
	result, err := e.await(ctx, 100*time.Millisecond, func() (goja.Value, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		previous := e.renderContext
		e.renderContext = ctx
		defer func() { e.renderContext = previous }()
		// Interrupt synchronous runaway renders at their short UI deadline without
		// disabling the extension's unrelated handlers.
		dogDone := make(chan struct{})
		dog := time.AfterFunc(100*time.Millisecond, func() { e.vm.Interrupt(fmt.Errorf("UI render deadline exceeded")); close(dogDone) })
		defer func() {
			if !dog.Stop() {
				<-dogDone
				e.vm.ClearInterrupt()
			}
		}()
		event := e.vm.ToValue(map[string]any{"site": string(ev.Site), "id": ev.ID, "surface": "shared", "props": ev.Props})
		nextValue := e.vm.ToValue(func(c goja.FunctionCall) goja.Value {
			modified := ev
			if v := c.Argument(0); !goja.IsUndefined(v) && !goja.IsNull(v) {
				var payload struct {
					Site    ui.Site        `json:"site"`
					ID      string         `json:"id"`
					Surface string         `json:"surface"`
					Props   map[string]any `json:"props"`
				}
				if err := e.vm.ExportTo(v, &payload); err != nil {
					panic(e.vm.NewTypeError("invalid next event"))
				}
				modified.Site, modified.ID, modified.Surface, modified.Props = payload.Site, payload.ID, payload.Surface, payload.Props
			}
			return e.async(func() (func() goja.Value, error) {
				n, err := next(modified)
				return func() goja.Value { return e.vm.ToValue(n) }, err
			})
		})
		v, err := fn(goja.Undefined(), event, nextValue)
		if err != nil {
			return nil, err
		}
		p, resolve, reject := e.vm.NewPromise()
		e.settle(v, func(value any, err error) {
			if err == nil {
				err = ctx.Err()
			}
			if err != nil {
				_ = reject(e.vm.NewGoError(err))
				return
			}
			binds := map[string]map[ui.EventType]ui.Handler{}
			var convert func(any, int) (*ui.Node, error)
			count := 0
			convert = func(value any, depth int) (*ui.Node, error) {
				if value == nil {
					return nil, nil
				}
				count++
				if count > ui.MaxNodes || depth > ui.MaxDepth {
					return nil, fmt.Errorf("UI tree limits")
				}
				if n, ok := value.(*ui.Node); ok {
					return n, nil
				}
				d, ok := value.(*drawing)
				if !ok {
					return nil, fmt.Errorf("render must return an element, next reference or null")
				}
				n := d.node
				n.Children = append([]ui.Node(nil), n.Children...)
				if len(d.callbacks) > 0 {
					binds[n.Key] = map[ui.EventType]ui.Handler{}
					for kind, callback := range d.callbacks {
						binds[n.Key][kind] = func(ctx context.Context, a ui.Action) error {
							// Accepted callbacks must yield the worker lane before promise I/O can
							// enqueue store/UI work there. The runtime's watchdog still bounds JS.
							go func() {
								_, err := e.await(ctx, e.m.timeout(), func() (goja.Value, error) {
									args := []goja.Value{e.vm.ToValue(map[string]any{"clientId": a.ClientID, "surface": a.Surface, "rev": a.Rev})}
									if kind != ui.Press {
										args = append([]goja.Value{e.vm.ToValue(*a.Value)}, args...)
									}
									e.actionClient = a.ClientID
									defer func() { e.actionClient = "" }()
									return callback(goja.Undefined(), args...)
								})
								if err != nil && !e.dead() {
									e.m.log(e.spec.Name, fmt.Sprintf("UI callback %s/%s/%s: %.256s", a.Site, a.ID, a.Key, err))
									n := ui.Text(ui.TextProps{Text: fmt.Sprintf("UI action failed: %.256s", err), Color: ui.Error})
									UIWork(e.m.host(), func(r *ui.Registry) error {
										if e.dead() {
											return errStopped
										}
										return r.OpenDefault(e.spec.Name, ui.OpenOptions{Site: ui.Toast, ID: fmt.Sprintf("%s/callback-%d", e.spec.Name, time.Now().UnixNano()), Level: "error", ExpiresAt: time.Now().Add(4 * time.Second).UnixMilli()}, nil, &n)
									}, func(error) {})
								}
							}()
							return nil
						}
					}
				}
				for _, child := range d.children {
					c, err := convert(child, depth+1)
					if err != nil {
						return nil, err
					}
					if c != nil {
						n.Children = append(n.Children, *c)
					}
				}
				return &n, nil
			}
			n, err := convert(value, 1)
			if err != nil {
				_ = reject(e.vm.NewGoError(err))
				return
			}
			r.ReplaceBindings(e.spec.Name, ui.Match{Site: ev.Site, ID: ev.ID}, binds)
			_ = resolve(n)
		})
		return e.vm.ToValue(p), nil
	})
	if err != nil {
		e.m.log(e.spec.Name, fmt.Sprintf("render %s/%s: %.256s", ev.Site, ev.ID, err))
		return nil, err
	}
	if result == nil {
		return nil, nil
	}
	n, ok := result.(*ui.Node)
	if !ok {
		return nil, fmt.Errorf("invalid render result")
	}
	return n, nil
}
func (e *ext) ask(q Question) goja.Value {
	e.readOnlyRender()
	p, resolve, _ := e.vm.NewPromise()
	e.asking.Add(1)
	e.m.host().Ask(e.spec.Name, q, func(v any) {
		e.asking.Add(-1)
		e.post(func() {
			if v == nil {
				_ = resolve(goja.Undefined())
			} else {
				_ = resolve(v)
			}
		})
	})
	return e.vm.ToValue(p)
}
func (e *ext) storeObject() *goja.Object {
	o := e.vm.NewObject()
	for _, op := range []string{"get", "set", "delete", "keys"} {
		_ = o.Set(op, func(c goja.FunctionCall) goja.Value {
			if op == "set" || op == "delete" {
				e.readOnlyRender()
			}
			key := ""
			if op != "keys" {
				key = c.Argument(0).String()
				if key == "" || len(key) > 128 {
					panic(e.vm.NewTypeError("store key must be 1..128 bytes"))
				}
			}
			var value json.RawMessage
			if op == "set" {
				v := c.Argument(1)
				if err := e.checkJSJSON(v, 0, map[*goja.Object]bool{}); err != nil {
					panic(e.vm.NewTypeError("%s", err))
				}
				if goja.IsUndefined(v) {
					panic(e.vm.NewTypeError("store accepts JSON only"))
				}
				var b []byte
				var err error
				if goja.IsNull(v) {
					b = []byte("null")
				} else {
					b, err = v.ToObject(e.vm).MarshalJSON()
				}
				if err != nil {
					panic(e.vm.NewTypeError("store accepts JSON only"))
				}
				value = b
			}
			ctx := e.requestContext()
			return e.async(func() (func() goja.Value, error) {
				h, ok := e.m.host().(PortableHost)
				if !ok {
					return nil, fmt.Errorf("store unavailable")
				}
				b, err := h.Store(ctx, e.spec.Name, op, key, value)
				return func() goja.Value {
					if len(b) == 0 {
						return goja.Undefined()
					}
					var v any
					_ = json.Unmarshal(b, &v)
					return e.vm.ToValue(v)
				}, err
			})
		})
	}
	return o
}

func checkJSON(value any, depth int) error {
	if depth > 64 {
		return fmt.Errorf("JSON nesting too deep")
	}
	switch v := value.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("store numbers must be finite")
		}
		return nil
	case nil, bool, string, int64, int:
		return nil
	case []any:
		for _, x := range v {
			if err := checkJSON(x, depth+1); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		for _, x := range v {
			if err := checkJSON(x, depth+1); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("store accepts JSON only")
	}
}

func (e *ext) checkJSJSON(v goja.Value, depth int, seen map[*goja.Object]bool) error {
	if depth > 64 {
		return fmt.Errorf("JSON nesting too deep")
	}
	if goja.IsUndefined(v) {
		return fmt.Errorf("store accepts JSON only")
	}
	if o, ok := v.(*goja.Object); ok {
		if _, ok := goja.AssertFunction(v); ok {
			return fmt.Errorf("store accepts JSON only")
		}
		if o.ClassName() != "Object" && o.ClassName() != "Array" {
			return fmt.Errorf("store accepts plain JSON objects/arrays only")
		}
		if seen[o] {
			return fmt.Errorf("cyclic JSON")
		}
		seen[o] = true
		defer delete(seen, o)
		for _, key := range o.Keys() {
			if err := e.checkJSJSON(o.Get(key), depth+1, seen); err != nil {
				return err
			}
		}
		return nil
	}
	return checkJSON(v.Export(), depth)
}

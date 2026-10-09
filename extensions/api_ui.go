//go:build !noext

package extensions

import (
	"github.com/dop251/goja"
)

// uiObject is ctx.ui. Everything goes to the Host; dialogs return
// promises that settle when the user answers (at once, with the default
// answer, without a UI).
func (e *ext) uiObject() *goja.Object {
	vm := e.vm
	ui := vm.NewObject()
	_ = ui.Set("notify", func(text string, level goja.Value) {
		lv := "info"
		if level != nil && !goja.IsUndefined(level) {
			switch l := level.String(); l {
			case "info", "warning", "error":
				lv = l
			default:
				panic(vm.NewTypeError("ui.notify: level must be info, warning or error, not %q", l))
			}
		}
		e.m.host().Notify(e.spec.Name, text, lv)
	})
	_ = ui.Set("setStatus", func(key string, text goja.Value) {
		s := ""
		if text != nil && !goja.IsUndefined(text) && !goja.IsNull(text) {
			s = text.String()
		}
		e.m.host().SetStatus(e.spec.Name, key, s)
	})
	_ = ui.Set("setWidget", func(key string, lines goja.Value) {
		var ls []string
		if lines != nil && !goja.IsUndefined(lines) && !goja.IsNull(lines) {
			if err := vm.ExportTo(lines, &ls); err != nil {
				panic(vm.NewTypeError("ui.setWidget: lines must be an array of strings"))
			}
			if ls == nil {
				ls = []string{}
			}
		}
		e.m.host().SetWidget(e.spec.Name, key, ls)
	})
	text := func(v goja.Value) string {
		if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
			return ""
		}
		return v.String()
	}
	_ = ui.Set("setBlockStatus", func(id string, s goja.Value) {
		e.m.host().SetBlockStatus(e.spec.Name, id, text(s))
	})
	_ = ui.Set("setBlockDisplay", func(id string, s goja.Value) {
		e.m.host().SetBlockDisplay(e.spec.Name, id, text(s))
	})
	_ = ui.Set("showText", func(title, body string, opts *goja.Object) {
		o := TextOptions{Lang: optString(opts, "lang")}
		if n := optNumber(opts, "preview"); n > 0 {
			o.Preview = int(n)
		}
		e.m.host().ShowText(e.spec.Name, title, body, o)
	})
	_ = ui.Set("select", func(title string, options []string) goja.Value {
		return e.ask(Question{Kind: "select", Title: title, Options: options})
	})
	_ = ui.Set("confirm", func(text string) goja.Value {
		return e.ask(Question{Kind: "confirm", Title: text})
	})
	_ = ui.Set("input", func(prompt string) goja.Value {
		return e.ask(Question{Kind: "input", Title: prompt})
	})
	return ui
}

// ask shows q through the host and returns a promise of the answer.
// While it is open, handler timeouts of this extension are on hold.
func (e *ext) ask(q Question) goja.Value {
	p, resolve, _ := e.vm.NewPromise()
	e.asking.Add(1)
	e.m.host().Ask(e.spec.Name, q, func(v any) {
		e.asking.Add(-1)
		e.post(func() {
			if v == nil {
				_ = resolve(goja.Undefined())
				return
			}
			_ = resolve(v)
		})
	})
	return e.vm.ToValue(p)
}

//go:build !noext

package extensions

import "context"

// goja promise reactions preserve the originating action/render context. This
// prevents async drawing from evading write guards, or losing a focus hint after
// await, without contaminating unrelated commands while a promise is pending.
type uiTaskContext struct {
	client string
	render context.Context
}
type uiContextTracker struct {
	e *ext
}

func (t *uiContextTracker) Grab() any { return uiTaskContext{t.e.actionClient, t.e.renderContext} }
func (t *uiContextTracker) Resumed(value any) {
	current := value.(uiTaskContext)
	t.e.actionClient = current.client
	t.e.renderContext = current.render
}
func (t *uiContextTracker) Exited() {
	t.e.actionClient = ""
	t.e.renderContext = nil
}

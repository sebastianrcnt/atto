package app

import (
	"github.com/sebastianrcnt/atto/tui"
)

// Extensions run in the runtime; what they show around the input (status
// items and widgets) arrives as extension/ui, their notices and text
// blocks as items, and their questions as prompts (prompts.go).

// renderWidgets draws the extensions' widgets above the input.
func (a *App) renderWidgets(width int) []string {
	ui := a.info.ExtensionUI
	if a.modal != nil || ui == nil {
		return nil
	}
	var out []string
	for _, w := range ui.Widgets {
		for _, l := range w.Lines {
			out = append(out, tui.Truncate(" "+l, width, "…"))
		}
	}
	return out
}

// extensionStatus is the status line items extensions set.
func (a *App) extensionStatus() []string {
	ui := a.info.ExtensionUI
	if ui == nil {
		return nil
	}
	var out []string
	for _, s := range ui.Status {
		out = append(out, s.Text)
	}
	return out
}

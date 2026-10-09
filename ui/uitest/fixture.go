// Package uitest supplies shared frontend fixtures; it has no frontend dependencies.
package uitest

import "github.com/sebastianrcnt/atto/ui"

func Catalog() ui.Node {
	return ui.Box(ui.BoxProps{BorderStyle: "round", Padding: 1, Gap: 1}, ui.Text(ui.TextProps{Text: "Portable é 👩‍💻 漢字\tUI", Bold: true}), ui.Box(ui.BoxProps{FlexDirection: "row", Gap: 2}, ui.Text(ui.TextProps{Text: "Left", Color: ui.Accent}), ui.Text(ui.TextProps{Text: "Right"})), ui.Diff(ui.DiffProps{Source: "--- a/test\n+++ b/test\n@@ -1 +1 @@\n-old\n+new"}), ui.List(ui.ListProps{Mode: "table", Columns: []ui.Column{{Label: "Job"}, {Label: "State"}}, Rows: []ui.Row{{Key: "1", Cells: []string{"1 · sleep 60", "running"}}}}), ui.Button(ui.ButtonProps{Key: "stop", Label: "Stop job 1", Hotkey: "s"}), ui.Collapse(ui.CollapseProps{Key: "more", Title: "Details", PreviewLines: 1}, ui.Text(ui.TextProps{Text: "one\ntwo\nthree"})))
}

package session

import "github.com/sebastianrcnt/atto/ui"

// LegacyTextTree converts old ext_text data on read only. The source file is
// never rewritten and no historical extension code is called.
func LegacyTextTree(e Entry) ui.Node {
	text := ui.Markdown(ui.MarkdownProps{Text: e.Display})
	preview := e.Preview
	if preview <= 0 {
		preview = 10
	}
	return ui.Collapse(ui.CollapseProps{Key: "legacy/" + e.ID, Title: e.Title, PreviewLines: preview}, text)
}

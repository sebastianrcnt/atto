package app

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/tui"
	"github.com/sebastianrcnt/atto/ui"
)

type uiItemBlock struct {
	elements   *tui.Elements
	original   tui.Component
	show       expander
	toggleLine int
	wire       server.Item
	app        *App
}

func (b *uiItemBlock) Render(width int) []string {
	var lines []string
	if b.elements.Tree == nil || b.show.expanded() {
		lines = b.original.Render(width)
	} else {
		lines = b.elements.Render(width)
	}
	b.toggleLine = len(lines)
	what := "shown: provider (click or ctrl+o to show original)"
	if b.show.expanded() {
		what = "original shown (click or ctrl+o to show provider)"
	}
	return append(lines, tui.Truncate(tui.Dim("  · "+what), width, "…"))
}
func (b *uiItemBlock) Click(line int) bool {
	if line == b.toggleLine {
		b.show.toggle()
		return true
	}
	if b.show.expanded() {
		if c, ok := b.original.(tui.Clickable); ok {
			return c.Click(line)
		}
	} else {
		return b.elements.Click(line)
	}
	return false
}
func (a *App) rememberNative(w server.Item, c tui.Component) {
	if a.nativeItems == nil {
		a.nativeItems = map[string]tui.Component{}
	}
	a.nativeItems[w.ID] = c
	a.applyUIItem(w)
}
func (a *App) applyUIItem(w server.Item) {
	if w.UIDisplay == nil {
		return
	}
	native := a.nativeItems[w.ID]
	if native == nil {
		return
	}
	if a.itemUI == nil {
		a.itemUI = map[string]*uiItemBlock{}
	}
	b := a.itemUI[w.ID]
	if w.UIDisplay.Tree == nil {
		if b != nil {
			a.replaceUIComponent(b, native)
			delete(a.itemUI, w.ID)
		}
		return
	}
	if b == nil {
		b = &uiItemBlock{original: native, show: expander{d: &a.origView}, elements: &tui.Elements{OnAction: a.uiAction}, app: a}
		a.itemUI[w.ID] = b
		a.replaceUIComponent(native, b)
	}
	b.wire = w
	n := w.UIDisplay.Tree
	if !w.UIDisplay.ActionsEnabled {
		n = passiveUITree(n)
	}
	_ = b.elements.SetTree(itemSite(w), w.ID, w.UIDisplay.Rev, n)
	b.elements.Engine = func(ref ui.Node, width int) []string {
		if ref.Props["site"] != string(itemSite(w)) || ref.Props["id"] != w.ID {
			return native.Render(width)
		}
		over, _ := ref.Props["overrides"].(map[string]any)
		if len(over) == 0 {
			return native.Render(width)
		}
		shown := w
		for key, value := range over {
			text, _ := value.(string)
			switch key {
			case "text":
				shown.Text = text
			case "title":
				shown.Title = text
			case "description":
				shown.Description = text
			case "output":
				shown.Output = text
			}
		}
		return a.nativeUIReference(shown).Render(width)
	}
}
func passiveUITree(n *ui.Node) *ui.Node {
	if n == nil {
		return nil
	}
	b, _ := json.Marshal(n)
	var out ui.Node
	_ = json.Unmarshal(b, &out)
	var walk func(*ui.Node)
	walk = func(n *ui.Node) {
		if n.Type == "Button" || n.Type == "Input" || n.Type == "Select" {
			n.Props["disabled"] = true
		}
		for i := range n.Children {
			walk(&n.Children[i])
		}
	}
	walk(&out)
	return &out
}
func itemSite(w server.Item) ui.Site {
	switch w.Type {
	case server.ItemUser:
		return ui.UserMessage
	case server.ItemAgent, server.ItemReasoning:
		return ui.AssistantMessage
	case server.ItemCommand:
		return ui.ToolCall
	default:
		return ui.Notice
	}
}
func (a *App) replaceUIComponent(old, next tui.Component) {
	for i, c := range a.ui.Body.Children {
		if g, ok := c.(gap); ok {
			if g.Component == old {
				a.ui.Body.Children[i] = gap{next}
				return
			}
			if run, ok := g.Component.(*toolRun); ok {
				for j, member := range run.members {
					if member == old {
						run.members[j] = next
						return
					}
				}
			}
		}
	}
}
func (a *App) nativeUIReference(w server.Item) tui.Component {
	switch w.Type {
	case server.ItemUser:
		return &userBlock{text: w.Text}
	case server.ItemAgent:
		b := &textBlock{}
		b.text.WriteString(w.Text)
		return b
	case server.ItemReasoning:
		b := &thinkingBlock{done: w.Status != "inProgress", start: time.UnixMilli(w.StartedMs), dur: time.Duration(w.DurationMs) * time.Millisecond}
		b.text.WriteString(w.Text)
		return b
	case server.ItemCommand:
		b := &toolBlock{args: agent.BashArgs{Command: w.Command, Description: w.Description}, pending: w.Pending, done: w.Status != "inProgress", timeout: time.Duration(w.TimeoutMs) * time.Millisecond, start: time.UnixMilli(w.StartedMs)}
		b.output.WriteString(w.Output)
		b.total = len(w.Output)
		b.res = agent.BashResult{Duration: time.Duration(w.DurationMs) * time.Millisecond, Job: w.Job, Background: w.Background, Canceled: w.Canceled, TimedOut: w.TimedOut}
		if w.ExitCode != nil {
			b.res.ExitCode = *w.ExitCode
		}
		return b
	default:
		if w.Title != "" {
			return &infoBlock{title: w.Title, hint: w.Text}
		}
		return &noticeBlock{text: w.Text, style: tui.Dim}
	}
}
func (a *App) uiItemDelta(id, delta string) {
	if b := a.itemUI[id]; b != nil {
		if b.wire.Type == server.ItemCommand {
			b.wire.Output += delta
		} else {
			b.wire.Text += delta
		}
		if strings.TrimSpace(delta) != "" {
			w := b.wire
			a.applyUIItem(w)
		}
	}
}

// Package ui defines session-owned portable UI data. It has no runtime or
// frontend dependencies; callbacks are held by Registry, never by wire nodes.
package ui

import "encoding/json"

type Site string

const (
	Pane             Site = "pane"
	Band             Site = "band"
	Status           Site = "status"
	Toast            Site = "toast"
	Transcript       Site = "transcript"
	Dialog           Site = "dialog"
	UserMessage      Site = "userMessage"
	AssistantMessage Site = "assistantMessage"
	ToolCall         Site = "toolCall"
	Notice           Site = "notice"
)

type EventType string

const (
	Press       EventType = "press"
	InputEvent  EventType = "input"
	Submit      EventType = "submit"
	SelectEvent EventType = "select"
	CloseEvent  EventType = "close"
)

type ThemeKey string

const (
	ColorText  ThemeKey = "text"
	Muted      ThemeKey = "muted"
	Accent     ThemeKey = "accent"
	Success    ThemeKey = "success"
	Warning    ThemeKey = "warning"
	Error      ThemeKey = "error"
	Border     ThemeKey = "border"
	Surface    ThemeKey = "surface"
	DiffAdd    ThemeKey = "diffAdd"
	DiffRemove ThemeKey = "diffRemove"
	DiffHunk   ThemeKey = "diffHunk"
)

// Node is wire data. Treat returned nodes as immutable. Null trees use *Node(nil).
type Node struct {
	Type     string         `json:"type"`
	Key      string         `json:"key,omitempty"`
	Props    map[string]any `json:"props"`
	Children []Node         `json:"children,omitempty"`
	Events   []EventType    `json:"events,omitempty"`
	engine   bool
}
type Common struct {
	Key             string      `json:"-"`
	Events          []EventType `json:"-"`
	Color           ThemeKey    `json:"color,omitempty"`
	BackgroundColor ThemeKey    `json:"backgroundColor,omitempty"`
}
type Control struct {
	Common
	Disabled  bool `json:"disabled,omitempty"`
	AutoFocus bool `json:"autoFocus,omitempty"`
}

// Width represents the catalog's integer | "fill" union. Zero means default.
type Width int

const Fill Width = 0

func (w Width) MarshalJSON() ([]byte, error) {
	if w == Fill {
		return []byte(`"fill"`), nil
	}
	return json.Marshal(int(w))
}

type BoxProps struct {
	Common
	FlexDirection string  `json:"flexDirection,omitempty"`
	Gap           int     `json:"gap,omitempty"`
	Padding       int     `json:"padding,omitempty"`
	Width         Width   `json:"width,omitempty"`
	Height        int     `json:"height,omitempty"`
	Grow          float64 `json:"grow,omitempty"`
	Align         string  `json:"align,omitempty"`
	BorderStyle   string  `json:"borderStyle,omitempty"`
}
type TextProps struct {
	Common
	Text      string `json:"text,omitempty"`
	Bold      bool   `json:"bold,omitempty"`
	Italic    bool   `json:"italic,omitempty"`
	Underline bool   `json:"underline,omitempty"`
	Wrap      string `json:"wrap,omitempty"`
	MaxLines  int    `json:"maxLines,omitempty"`
}
type MarkdownProps struct {
	Common
	Text     string `json:"text"`
	MaxLines int    `json:"maxLines,omitempty"`
}
type CodeProps struct {
	Common
	Source      string `json:"source"`
	Language    string `json:"language,omitempty"`
	Path        string `json:"path,omitempty"`
	StartLine   int    `json:"startLine,omitempty"`
	LineNumbers bool   `json:"lineNumbers,omitempty"`
	Wrap        string `json:"wrap,omitempty"`
}
type DiffProps struct {
	Common
	Source      string `json:"source"`
	Path        string `json:"path,omitempty"`
	LineNumbers bool   `json:"lineNumbers,omitempty"`
	Wrap        string `json:"wrap,omitempty"`
}
type LinkProps struct {
	Common
	Href  string `json:"href"`
	Label string `json:"label,omitempty"`
}
type ButtonProps struct {
	Control
	Label  string `json:"label"`
	Plain  bool   `json:"plain,omitempty"`
	Hotkey string `json:"hotkey,omitempty"`
}
type InputProps struct {
	Control
	Label       string `json:"label,omitempty"`
	Value       string `json:"value,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	SubmitLabel string `json:"submitLabel,omitempty"`
	MaxLength   int    `json:"maxLength,omitempty"`
}
type Option struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Disabled    bool   `json:"disabled,omitempty"`
}
type SelectProps struct {
	Control
	Options []Option `json:"options"`
	Label   string   `json:"label,omitempty"`
	Value   *string  `json:"value,omitempty"`
}
type Row struct {
	Key   string   `json:"key"`
	Cells []string `json:"cells"`
}
type Column struct {
	Label string `json:"label"`
	Width int    `json:"width,omitempty"`
	Align string `json:"align,omitempty"`
}
type ListProps struct {
	Common
	Mode      string   `json:"mode,omitempty"`
	Rows      []Row    `json:"rows"`
	Columns   []Column `json:"columns,omitempty"`
	EmptyText string   `json:"emptyText,omitempty"`
}
type ProgressProps struct {
	Common
	Value *float64 `json:"value,omitempty"`
	Label string   `json:"label,omitempty"`
	Width int      `json:"width,omitempty"`
}
type CollapseProps struct {
	Common
	Title        string `json:"title"`
	DefaultOpen  bool   `json:"defaultOpen,omitempty"`
	PreviewLines int    `json:"previewLines,omitempty"`
}
type ImageProps struct {
	Common
	Resource string `json:"resource"`
	Alt      string `json:"alt"`
	Columns  int    `json:"columns,omitempty"`
	Rows     int    `json:"rows,omitempty"`
	Fit      string `json:"fit,omitempty"`
}

func node(kind string, p any, c Common, children ...Node) Node {
	b, err := json.Marshal(p)
	if err != nil {
		return Node{Type: kind, Props: map[string]any{"invalid": err.Error()}}
	}
	var props map[string]any
	_ = json.Unmarshal(b, &props)
	return Node{Type: kind, Key: c.Key, Props: props, Events: append([]EventType(nil), c.Events...), Children: append([]Node(nil), children...)}
}
func Box(p BoxProps, c ...Node) Node   { return node("Box", p, p.Common, c...) }
func Text(p TextProps, c ...Node) Node { return node("Text", p, p.Common, c...) }
func Markdown(p MarkdownProps) Node    { return node("Markdown", p, p.Common) }
func Code(p CodeProps) Node            { return node("Code", p, p.Common) }
func Diff(p DiffProps) Node            { return node("Diff", p, p.Common) }
func Link(p LinkProps) Node            { return node("Link", p, p.Common) }
func Button(p ButtonProps) Node {
	if len(p.Events) == 0 {
		p.Events = []EventType{Press}
	}
	return node("Button", p, p.Common)
}
func Input(p InputProps) Node {
	if len(p.Events) == 0 {
		p.Events = []EventType{Submit}
	}
	return node("Input", p, p.Common)
}
func Select(p SelectProps) Node {
	if len(p.Events) == 0 {
		p.Events = []EventType{SelectEvent}
	}
	return node("Select", p, p.Common)
}
func List(p ListProps) Node {
	if p.Rows == nil {
		p.Rows = []Row{}
	}
	return node("List", p, p.Common)
}
func Progress(p ProgressProps) Node            { return node("Progress", p, p.Common) }
func Collapse(p CollapseProps, c ...Node) Node { return node("Collapse", p, p.Common, c...) }
func Image(p ImageProps) Node                  { return node("Image", p, p.Common) }
func IsItem(site Site) bool {
	return site == UserMessage || site == AssistantMessage || site == ToolCall || site == Notice
}
func ValidSite(site Site) bool {
	return IsItem(site) || site == Pane || site == Band || site == Status || site == Toast || site == Transcript || site == Dialog
}

package ui

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

// PlainText is the headless fallback, in document order. It never evaluates
// controls, resources, Markdown HTML, or engine references.
func PlainText(n Node) string {
	str := func(k string) string { s, _ := n.Props[k].(string); return CleanText(s) }
	var lines []string
	switch n.Type {
	case "Text", "Markdown":
		lines = append(lines, str("text"))
	case "Code", "Diff":
		if p := str("path"); p != "" {
			lines = append(lines, p)
		}
		lines = append(lines, str("source"))
	case "Link":
		label := str("label")
		href := str("href")
		if label == "" || label == href {
			lines = append(lines, href)
		} else {
			lines = append(lines, label+" ("+href+")")
		}
	case "Button":
		lines = append(lines, "[ "+str("label")+" ]")
	case "Input":
		lines = append(lines, str("label")+": "+str("value"))
	case "Select":
		if l := str("label"); l != "" {
			lines = append(lines, l)
		}
		b, _ := json.Marshal(n.Props["options"])
		var opts []Option
		_ = json.Unmarshal(b, &opts)
		for _, o := range opts {
			lines = append(lines, "• "+CleanText(o.Label))
		}
	case "List":
		b, _ := json.Marshal(n.Props["rows"])
		var rows []Row
		_ = json.Unmarshal(b, &rows)
		if len(rows) == 0 {
			text := str("emptyText")
			if text == "" {
				text = "No items"
			}
			lines = append(lines, text)
		}
		for _, r := range rows {
			lines = append(lines, CleanText(strings.Join(r.Cells, "  ")))
		}
	case "Progress":
		if v, ok := n.Props["value"].(float64); ok {
			lines = append(lines, fmt.Sprintf("%s %.0f%%", str("label"), v*100))
		} else {
			lines = append(lines, "… "+str("label"))
		}
	case "Collapse":
		lines = append(lines, str("title"))
	case "Image":
		lines = append(lines, "[image: "+str("alt")+"]")
	case "engine":
		lines = append(lines, "[original: "+str("site")+"/"+str("id")+"]")
	case "Box":
	default:
		for _, k := range []string{"text", "source", "label", "alt"} {
			if s := str(k); s != "" {
				lines = append(lines, s)
			}
		}
		if len(lines) == 0 && len(n.Children) == 0 {
			lines = append(lines, "[unsupported: "+CleanText(n.Type)+"]")
		}
	}
	for _, c := range n.Children {
		text := PlainText(c)
		if n.Type == "Text" && len(lines) > 0 {
			lines[len(lines)-1] += text
		} else {
			lines = append(lines, text)
		}
	}
	return strings.Join(lines, "\n")
}

// CleanText removes entire ANSI/OSC sequences, not merely the ESC byte.
func CleanText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 27 {
			i++
			if i >= len(s) {
				break
			}
			kind := s[i]
			i++
			switch kind {
			case '[':
				for i < len(s) {
					c := s[i]
					i++
					if c >= 0x40 && c <= 0x7e {
						break
					}
				}
			case ']', '_', 'P', '^', 'X':
				for i < len(s) {
					if s[i] == 7 {
						i++
						break
					}
					if s[i] == 27 && i+1 < len(s) && s[i+1] == '\\' {
						i += 2
						break
					}
					i++
				}
			}
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, b.String())
}

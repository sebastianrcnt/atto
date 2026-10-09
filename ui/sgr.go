package ui

import (
	"strconv"
	"strings"
)

// ThemedText converts legacy safe SGR command output into catalog spans. Only
// semantic palette colors and text attributes survive; OSC/other controls and
// arbitrary RGB never enter a wire tree.
func ThemedText(s string) Node {
	var spans []Node
	props := TextProps{}
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			p := props
			p.Text = CleanText(b.String())
			spans = append(spans, Text(p))
			b.Reset()
		}
	}
	for i := 0; i < len(s); {
		if s[i] != 27 {
			b.WriteByte(s[i])
			i++
			continue
		}
		flush()
		start := i
		i++
		if i >= len(s) {
			break
		}
		if s[i] == '[' {
			i++
			params := i
			for i < len(s) && !(s[i] >= 0x40 && s[i] <= 0x7e) {
				i++
			}
			if i < len(s) && s[i] == 'm' {
				codes := strings.Split(s[params:i], ";")
				for j := 0; j < len(codes); j++ {
					code, _ := strconv.Atoi(codes[j])
					switch code {
					case 0:
						props = TextProps{}
					case 1:
						props.Bold = true
					case 2:
						props.Color = Muted
					case 3:
						props.Italic = true
					case 4:
						props.Underline = true
					case 22:
						props.Bold = false
						props.Color = ""
					case 23:
						props.Italic = false
					case 24:
						props.Underline = false
					case 31, 91:
						props.Color = Error
					case 32, 92:
						props.Color = Success
					case 33, 93:
						props.Color = Warning
					case 34, 35, 36, 94, 95, 96:
						props.Color = Accent
					case 39:
						props.Color = ""
					case 38:
						if j+2 < len(codes) && codes[j+1] == "5" {
							v, _ := strconv.Atoi(codes[j+2])
							switch v {
							case 1:
								props.Color = Error
							case 2:
								props.Color = Success
							case 3:
								props.Color = Warning
							case 4, 5, 6:
								props.Color = Accent
							default:
								props.Color = ColorText
							}
							j += 2
						} else if j+4 < len(codes) && codes[j+1] == "2" {
							j += 4
						}
					}
				}
			}
			if i < len(s) {
				i++
			}
			continue
		}
		if s[i] == ']' || s[i] == '_' || s[i] == 'P' || s[i] == '^' || s[i] == 'X' {
			i++
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
		} else {
			i = min(len(s), start+2)
		}
	}
	flush()
	return Text(TextProps{}, spans...)
}

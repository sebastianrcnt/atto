package tui

import (
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
)

// PastePrefix marks an input event that carries bracketed-paste content.
// The event is PastePrefix followed by the pasted text.
const PastePrefix = "\x00paste:"

// inputParser splits raw stdin bytes into individual key sequences and
// reassembles bracketed pastes. Incomplete sequences are held until the next
// read.
type inputParser struct {
	pending string
	paste   *strings.Builder
	// lastBurst is when the last unbracketed paste was read; now is
	// time.Now unless a test sets it.
	lastBurst time.Time
	now       func() time.Time
	// bursts turns on the unbracketed-paste heuristic. Only Windows needs
	// it (conhost sends no paste markers); Unix terminals bracket pastes,
	// and there the heuristic could only turn a fast Enter into a newline.
	bursts bool
}

// burstGap is how soon after an unbracketed paste a read still belongs
// to it: a long paste arrives over several reads.
const burstGap = 30 * time.Millisecond

func (p *inputParser) feed(data string) []string {
	if p.bursts && p.pending == "" && p.paste == nil {
		now := time.Now
		if p.now != nil {
			now = p.now
		}
		t := now()
		cont := !p.lastBurst.IsZero() && t.Sub(p.lastBurst) < burstGap && plainText(data)
		if cont || unbracketedPaste(data) {
			p.lastBurst = t
			return []string{PastePrefix + data}
		}
		p.lastBurst = time.Time{}
	}
	s := p.pending + data
	p.pending = ""
	var out []string
	for len(s) > 0 {
		if p.paste != nil {
			end := strings.Index(s, pasteEnd)
			if end < 0 {
				// Hold back a partial end marker split across reads.
				keep := 0
				for k := min(len(pasteEnd)-1, len(s)); k > 0; k-- {
					if strings.HasSuffix(s, pasteEnd[:k]) {
						keep = k
						break
					}
				}
				p.paste.WriteString(s[:len(s)-keep])
				p.pending = s[len(s)-keep:]
				return out
			}
			p.paste.WriteString(s[:end])
			out = append(out, PastePrefix+p.paste.String())
			p.paste = nil
			s = s[end+len(pasteEnd):]
			continue
		}
		if strings.HasPrefix(s, pasteStart) {
			p.paste = &strings.Builder{}
			s = s[len(pasteStart):]
			continue
		}
		n, complete := seqLen(s)
		if !complete {
			p.pending = s
			return out
		}
		out = append(out, normalizeKey(s[:n]))
		s = s[n:]
	}
	return out
}

// unbracketedPaste reports whether one read looks like pasted text that
// arrived without bracketed-paste markers, as in the classic Windows
// console: plain text with a line break followed by more text. Typing
// cannot produce that within one read, so a paste no longer submits at its
// first newline. (codex-rs detects such bursts by key timing instead.)
func unbracketedPaste(data string) bool {
	return strings.ContainsAny(strings.TrimRight(data, "\r\n"), "\r\n") && plainText(data)
}

// plainText reports whether data is text and line breaks only: no escape
// sequences or control keys.
func plainText(data string) bool {
	for _, r := range data {
		if (r < 0x20 && r != '\r' && r != '\n' && r != '\t') || r == 0x7f {
			return false
		}
	}
	return data != "" && utf8.ValidString(data)
}

// seqLen returns the length of the first key sequence in s and whether it is
// complete. A lone ESC at the end of a read is treated as the Escape key.
func seqLen(s string) (int, bool) {
	if s[0] != 0x1b {
		if !utf8.FullRuneInString(s) {
			return 0, false
		}
		_, n := utf8.DecodeRuneInString(s)
		return n, true
	}
	if len(s) == 1 {
		return 1, true
	}
	switch s[1] {
	case '[':
		// Legacy X10 mouse report: ESC [ M followed by three raw bytes.
		if len(s) >= 3 && s[2] == 'M' {
			if len(s) < 6 {
				return 0, false
			}
			return 6, true
		}
		for j := 2; j < len(s); j++ {
			if c := s[j]; c >= 0x40 && c <= 0x7e {
				return j + 1, true
			}
		}
		return 0, false
	case 'O':
		if len(s) < 3 {
			return 0, false
		}
		return 3, true
	default:
		// Alt+key: ESC followed by one rune.
		if !utf8.FullRuneInString(s[1:]) {
			return 0, false
		}
		_, n := utf8.DecodeRuneInString(s[1:])
		return 1 + n, true
	}
}

// ctrlEnter is how a Ctrl+Enter report (either encoding) is passed on.
const ctrlEnter = "\x1b[13;5u"

// decodeExtended reads a key report of the enhanced keyboard modes: xterm's
// modifyOtherKeys, ESC [ 27 ; mod ; code ~, and the kitty protocol (also
// tmux's extended-keys csi-u), ESC [ code ; mod u. It returns the code and
// the modifiers (shift 1, alt 2, ctrl 4, super 8, ...). The kitty
// caps/num-lock bits and any event-type or alternate-key subfields are dropped.
func decodeExtended(data string) (code, mod int, ok bool) {
	if len(data) < 4 || data[0] != 0x1b || data[1] != '[' {
		return 0, 0, false
	}
	final := data[len(data)-1]
	if final != 'u' && final != '~' {
		return 0, 0, false
	}
	f := strings.Split(data[2:len(data)-1], ";")
	if final == '~' {
		if len(f) != 3 || f[0] != "27" {
			return 0, 0, false
		}
		f = f[1:]
		f[0], f[1] = f[1], f[0] // code first, as in the kitty form
	}
	field := func(s string) (int, bool) {
		n, err := strconv.Atoi(strings.SplitN(s, ":", 2)[0])
		return n, err == nil && n >= 0
	}
	if len(f) < 1 || len(f) > 3 {
		return 0, 0, false
	}
	if code, ok = field(f[0]); !ok {
		return 0, 0, false
	}
	mod = 1
	if len(f) > 1 && f[1] != "" {
		if mod, ok = field(f[1]); !ok || mod < 1 {
			return 0, 0, false
		}
	}
	return code, (mod - 1) &^ 0xc0, true
}

// ctrlByte is the control character the legacy encoding gives Ctrl+code.
func ctrlByte(code int) (byte, bool) {
	switch {
	case code >= 'a' && code <= 'z':
		return byte(code - 'a' + 1), true
	case code >= 'A' && code <= 'Z':
		return byte(code - 'A' + 1), true
	}
	switch code {
	case ' ', '@':
		return 0, true
	case '\\':
		return 0x1c, true
	case ']':
		return 0x1d, true
	case '^':
		return 0x1e, true
	case '_', '/':
		return 0x1f, true
	}
	return 0, false
}

// normalizeKey turns the reports of the enhanced keyboard modes (see
// keyboardOn) back into the legacy bytes, so every key but Ctrl+Enter reads
// the same whatever the terminal does: Ctrl+C may come as ESC [ 27 ; 5 ; 99 ~
// or ESC [ 99 ; 5 u. Ctrl+Enter, which the legacy encoding cannot tell from
// Enter, becomes ctrlEnter. Reports with no legacy form stay as they are.
func normalizeKey(data string) string {
	code, mod, ok := decodeExtended(data)
	if !ok {
		return data
	}
	shift, alt, ctrl := mod&1 != 0, mod&2 != 0, mod&4 != 0
	if mod&^7 != 0 {
		return data // super, hyper, meta
	}
	switch {
	case ctrl && !alt && !shift && code == 13:
		return ctrlEnter
	case ctrl && !alt && !shift:
		if b, ok := ctrlByte(code); ok {
			return string(rune(b))
		}
	case alt && !ctrl:
		switch code {
		case 13:
			if !shift {
				return "\x1b\r"
			}
		case 127, 8:
			if !shift {
				return "\x1b\x7f"
			}
		default:
			if r := textKey(code, shift); r != "" {
				return "\x1b" + r
			}
		}
	case !alt && !ctrl:
		switch code {
		case 27:
			if !shift {
				return "\x1b"
			}
		case 13: // Shift+Enter is Enter, as in the legacy encoding
			return "\r"
		case 9:
			if shift {
				return "\x1b[Z"
			}
			return "\t"
		case 127, 8:
			if !shift {
				return "\x7f"
			}
		default:
			if r := textKey(code, shift); r != "" {
				return r
			}
		}
	}
	return data
}

// textKey is the text a printable key code types, with shift applied to
// letters (the kitty code is the unshifted one). Empty for anything else.
func textKey(code int, shift bool) string {
	r := rune(code)
	if code < 0x20 || code == 0x7f || !utf8.ValidRune(r) {
		return ""
	}
	if shift {
		r = unicode.ToUpper(r)
	}
	return string(r)
}

// Key names a decoded key sequence, e.g. "enter", "ctrl+c", "left", "alt+b".
// Printable input returns "" — use the raw data instead.
func Key(data string) string {
	data = normalizeKey(data)
	switch data {
	case ctrlEnter:
		return "ctrl+enter"
	case "\r":
		return "enter"
	case "\n":
		return "ctrl+j"
	case "\t":
		return "tab"
	case "\x1b[Z":
		return "shift+tab"
	case "\x7f", "\x08":
		return "backspace"
	case "\x1b":
		return "escape"
	case "\x1b\r":
		return "alt+enter"
	case "\x1b[A", "\x1bOA":
		return "up"
	case "\x1b[B", "\x1bOB":
		return "down"
	case "\x1b[C", "\x1bOC":
		return "right"
	case "\x1b[D", "\x1bOD":
		return "left"
	case "\x1b[H", "\x1bOH", "\x1b[1~":
		return "home"
	case "\x1b[F", "\x1bOF", "\x1b[4~":
		return "end"
	case "\x1b[3~":
		return "delete"
	case "\x1b[5~":
		return "pageup"
	case "\x1b[6~":
		return "pagedown"
	case "\x1b[1;2D":
		return "shift+left"
	case "\x1b[1;2C":
		return "shift+right"
	case "\x1b[1;2A":
		return "shift+up"
	case "\x1b[1;2B":
		return "shift+down"
	case "\x1b[1;5C", "\x1bf":
		return "word-right"
	case "\x1b[1;5D", "\x1bb":
		return "word-left"
	case "\x1b\x7f":
		return "alt+backspace"
	}
	if len(data) == 1 && data[0] < 0x20 {
		return "ctrl+" + string(rune('a'+data[0]-1))
	}
	return ""
}

// Printable reports whether data is plain text to insert.
func Printable(data string) bool {
	if data == "" || strings.HasPrefix(data, PastePrefix) {
		return false
	}
	for _, r := range data {
		if r < 0x20 || r == 0x7f || r == 0x1b {
			return false
		}
	}
	return true
}

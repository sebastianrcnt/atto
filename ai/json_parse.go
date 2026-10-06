package ai

// Ported from pi (https://github.com/earendil-works/pi), Copyright (c) 2025
// Mario Zechner, MIT License; see THIRD_PARTY_NOTICES.

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
)

// Port of src/utils/json-parse.ts. pi uses the partial-json package for
// incomplete input; partialParse below closes open strings, arrays and
// objects instead, which covers what streamed tool arguments look like.

var hex4 = regexp.MustCompile(`^[0-9a-fA-F]{4}$`)

// RepairJSON escapes raw control characters inside strings and doubles
// backslashes before invalid escape characters.
func RepairJSON(s string) string {
	var b strings.Builder
	inString := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !inString {
			b.WriteByte(c)
			if c == '"' {
				inString = true
			}
			continue
		}
		switch {
		case c == '"':
			b.WriteByte(c)
			inString = false
		case c == '\\':
			if i+1 >= len(s) {
				b.WriteString(`\\`)
				continue
			}
			next := s[i+1]
			if next == 'u' && i+6 <= len(s) && hex4.MatchString(s[i+2:i+6]) {
				b.WriteString(s[i : i+6])
				i += 5
				continue
			}
			if strings.IndexByte(`"\/bfnrtu`, next) >= 0 {
				b.WriteByte('\\')
				b.WriteByte(next)
				i++
				continue
			}
			b.WriteString(`\\`)
		case c < 0x20:
			switch c {
			case '\b':
				b.WriteString(`\b`)
			case '\f':
				b.WriteString(`\f`)
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			default:
				b.WriteString(`\u00`)
				b.WriteByte("0123456789abcdef"[c>>4])
				b.WriteByte("0123456789abcdef"[c&15])
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// parseJSONWithRepair parses s, retrying once with RepairJSON.
func parseJSONWithRepair(s string, v any) error {
	err := json.Unmarshal([]byte(s), v)
	if err == nil {
		return nil
	}
	if r := RepairJSON(s); r != s {
		return json.Unmarshal([]byte(r), v)
	}
	return err
}

// ParseStreamingJSON parses possibly incomplete JSON from a stream. It
// always returns an object, empty when nothing can be recovered.
func ParseStreamingJSON(partial string) map[string]any {
	if strings.TrimSpace(partial) == "" {
		return map[string]any{}
	}
	var out map[string]any
	if parseJSONWithRepair(partial, &out) == nil && out != nil {
		return out
	}
	// Repaired first: raw control characters make every prefix of the
	// unrepaired text invalid down to "{}".
	for _, s := range []string{RepairJSON(partial), partial} {
		if m, ok := partialParse(s); ok {
			return m
		}
	}
	return map[string]any{}
}

// partialParse completes truncated JSON by closing what is open, after
// dropping a trailing incomplete key, separator or literal.
func partialParse(s string) (map[string]any, bool) {
	var stack []byte
	inString, escaped := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	t := s
	if inString {
		if escaped {
			t = t[:len(t)-1]
		}
		t += `"`
	}
	closed := func(t string) string {
		var b strings.Builder
		b.WriteString(t)
		for _, c := range slices.Backward(stack) {
			b.WriteByte(c)
		}
		return b.String()
	}
	// Try shorter prefixes: a dangling key, ":" or "," cannot be closed
	// into valid JSON.
	for range 64 {
		if len(t) == 0 {
			break
		}
		var m map[string]any
		if json.Unmarshal([]byte(closed(t)), &m) == nil && m != nil {
			return m, true
		}
		t = strings.TrimRight(t, " \t\r\n")
		switch {
		case strings.HasSuffix(t, ",") || strings.HasSuffix(t, ":"):
			t = t[:len(t)-1]
		case strings.HasSuffix(t, `"`) && len(t) > 1:
			j := strings.LastIndex(t[:len(t)-1], `"`)
			if j < 0 {
				return nil, false
			}
			t = t[:j]
		default:
			t = t[:len(t)-1]
		}
	}
	return nil, false
}

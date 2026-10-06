package tui

import (
	"slices"
	"testing"
)

func TestExtendedKeys(t *testing.T) {
	for _, c := range []struct{ in, key string }{
		// Ctrl+Enter, both encodings (and with num lock on, in kitty's).
		{"\x1b[27;5;13~", "ctrl+enter"},
		{"\x1b[13;5u", "ctrl+enter"},
		{"\x1b[13;133u", "ctrl+enter"},
		// The legacy bytes are unchanged.
		{"\r", "enter"},
		{"\n", "ctrl+j"},
		{"\x1b\r", "alt+enter"},
		{"\x07", "ctrl+g"},
		// What modifyOtherKeys level 2 and kitty's disambiguate flag send
		// for keys that are plain bytes without them.
		{"\x1b[27;5;99~", "ctrl+c"},
		{"\x1b[99;5u", "ctrl+c"},
		{"\x1b[27;5;67~", "ctrl+c"},
		{"\x1b[27;5;106~", "ctrl+j"},
		{"\x1b[27;5;100~", "ctrl+d"},
		{"\x1b[27;5;119~", "ctrl+w"},
		{"\x1b[27;3;13~", "alt+enter"},
		{"\x1b[13;3u", "alt+enter"},
		{"\x1b[27;3;127~", "alt+backspace"},
		{"\x1b[127;3u", "alt+backspace"},
		{"\x1b[27;2;9~", "shift+tab"},
		{"\x1b[9;2u", "shift+tab"},
		{"\x1b[27u", "escape"},
		{"\x1b[27;1u", "escape"},
		{"\x1b[13u", "enter"},
		{"\x1b[13;2u", "enter"}, // shift+enter: still Enter
		{"\x1b[27;2;13~", "enter"},
		{"\x1b[9u", "tab"},
		{"\x1b[127u", "backspace"},
		{"\x1b[27;3;98~", "word-left"},     // alt+b
		{"\x1b[102;3u", "word-right"},      // alt+f
		{"\x1b[27;5;91~", ""},              // ctrl+[: no legacy form kept
		{"\x1b[1;5C", "word-right"},        // not a letter key: untouched
		{"\x1b[111;6u", ""},                // shift+ctrl+o stays for the tree
		{"\x1b[27;6;111~", ""},             // (decoded there)
		{"\x1b[13;9u", ""},                 // super+enter
		{"\x1b[27;5;13;1~", ""},            // malformed
		{"\x1b[x;5u", ""},                  // malformed
		{"\x1b[200~", ""},                  // not a key report
		{"\x1b[<0;5;5M", ""},               // mouse
		{"\x1b[13;5:3u", "ctrl+enter"},     // event type subfield (not requested, but harmless)
		{"\x1b[13:13;5;13u", "ctrl+enter"}, // alternate keys and text
	} {
		if got := Key(c.in); got != c.key {
			t.Errorf("Key(%q) = %q, want %q", c.in, got, c.key)
		}
	}
}

func TestNormalizeKey(t *testing.T) {
	for in, want := range map[string]string{
		"\x1b[27;5;99~":  "\x03",
		"\x1b[99;5u":     "\x03",
		"\x1b[27;5;32~":  "\x00",
		"\x1b[27;5;47~":  "\x1f",
		"\x1b[98;4u":     "\x1bB", // alt+shift+b in kitty: the unshifted code
		"\x1b[27;3;66~":  "\x1bB",
		"\x1b[27;3;98~":  "\x1bb",
		"\x1b[27;3;233~": "\x1bé",
		"\x1b[27;2;76~":  "L", // shift+l
		"\x1b[111;6u":    "\x1b[111;6u",
		"\x1b[27;6;111~": "\x1b[27;6;111~",
		"\x1b[13;5u":     ctrlEnter,
		"\x1b[A":         "\x1b[A",
		"abc":            "abc",
	} {
		if got := normalizeKey(in); got != want {
			t.Errorf("normalizeKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// The parser hands on the legacy bytes for everything but Ctrl+Enter, also
// when reports are split across reads or mixed with a paste.
func TestParserNormalizesKeys(t *testing.T) {
	p := &inputParser{}
	got := p.feed("a\x1b[27;5;99~\x1b[27u\x1b[13;5u\x1b[27;3;13~\x1b[1;5C")
	want := []string{"a", "\x03", "\x1b", ctrlEnter, "\x1b\r", "\x1b[1;5C"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	got = p.feed("\x1b[27;5;")
	got = append(got, p.feed("13~x")...)
	if !slices.Equal(got, []string{ctrlEnter, "x"}) {
		t.Fatalf("split report: %q", got)
	}
	// Pasted text is never read as key reports.
	got = p.feed("\x1b[200~\x1b[27;5;99~\x1b[201~")
	if !slices.Equal(got, []string{PastePrefix + "\x1b[27;5;99~"}) {
		t.Fatalf("paste: %q", got)
	}
}

func TestEditorSendNow(t *testing.T) {
	var sent, submitted []string
	e := NewEditor("> ")
	e.OnSubmit = func(text string, _ []Attachment) { submitted = append(submitted, text) }
	for _, key := range []string{"\x1b[27;5;13~", "\x1b[13;5u", "\x07"} {
		e.SetText("hello")
		e.HandleInput(key)
		if e.Text() != "" {
			t.Fatalf("%q left %q", key, e.Text())
		}
	}
	if len(submitted) != 3 || submitted[0] != "hello" { // no OnSendNow: it submits
		t.Fatalf("submitted %q", submitted)
	}
	e.OnSendNow = func(text string, _ []Attachment) { sent = append(sent, text) }
	e.SetText("now")
	e.HandleInput("\x1b[27;5;13~")
	e.SetText("later")
	e.HandleInput("\r")
	if !slices.Equal(sent, []string{"now"}) || len(submitted) != 4 || submitted[3] != "later" {
		t.Fatalf("sent %q, submitted %q", sent, submitted)
	}
	// Newlines still work in every encoding.
	e.SetText("a")
	for _, key := range []string{"\x1b\r", "\n", "\x1b[13;3u", "\x1b[27;3;13~", "\x1b[106;5u"} {
		e.HandleInput(key)
	}
	if e.Text() != "a\n\n\n\n\n" {
		t.Fatalf("newlines: %q", e.Text())
	}
}

package ai

import (
	"strings"
	"testing"
	"unicode/utf16"
)

func jsLenOld(s string) int { return len(utf16.Encode([]rune(s))) }

var jsLenInputs = []string{
	"", "a", "hello world", "héllo", "한국어 텍스트", "日本語", "😀", "a😀b😀😀",
	"\xff", "ab\xc3", "\xed\xa0\x80", "\xf4\x90\x80\x80", "\xf0\x9f\x98", "x\xe2\x82y",
	"\u0000\u007f\u0080߿ࠀ￿\U00010000\U0010ffff", "�", "é 👨‍👩‍👧",
	strings.Repeat("😀한a\xff", 100),
}

func TestJSLenMatchesUTF16Encode(t *testing.T) {
	for _, s := range jsLenInputs {
		if got, want := jsLen(s), jsLenOld(s); got != want {
			t.Errorf("jsLen(%q) = %d, want %d", s, got, want)
		}
	}
	// Every two-byte string, bare and after a truncated emoji prefix.
	for a := range 256 {
		for b := range 256 {
			s := string([]byte{byte(a), byte(b)})
			if jsLen(s) != jsLenOld(s) {
				t.Fatalf("jsLen(%q) = %d, want %d", s, jsLen(s), jsLenOld(s))
			}
			s = "\xf0\x9f" + s
			if jsLen(s) != jsLenOld(s) {
				t.Fatalf("jsLen(%q) = %d, want %d", s, jsLen(s), jsLenOld(s))
			}
		}
	}
}

var sink int

func BenchmarkJSLen(b *testing.B) {
	s := strings.Repeat("hello 한국어 😀 world\xff ", 5000)
	b.ReportAllocs()
	for b.Loop() {
		sink = jsLen(s)
	}
}

func BenchmarkJSLenOld(b *testing.B) {
	s := strings.Repeat("hello 한국어 😀 world\xff ", 5000)
	b.ReportAllocs()
	for b.Loop() {
		sink = jsLenOld(s)
	}
}

package daemon

import (
	"strings"
	"testing"
)

func feedAll(s *stream, chunks ...string) (string, []string) {
	var out strings.Builder
	var ms []string
	for _, c := range chunks {
		o, m := s.feed([]byte(c))
		out.Write(o)
		ms = append(ms, m...)
	}
	return out.String(), ms
}

func TestStreamMarkersSplitAcrossWrites(t *testing.T) {
	s := newStream()
	m := MarkerSeq("session", "abc", "my name")
	out, ms := feedAll(s, "hi\x1b[1m", m[:5], m[5:12], m[12:]+"there\x1b]0;title\x07")
	if out != "hi\x1b[1mthere\x1b]0;title\x07" {
		t.Fatalf("out %q", out)
	}
	if len(ms) != 1 || ms[0] != "session;abc;my name" {
		t.Fatalf("markers %q", ms)
	}
	// ST-terminated markers count too.
	if _, ms := feedAll(s, "\x1b]7337;detach\x1b\\"); len(ms) != 1 || ms[0] != "detach" {
		t.Fatalf("ST marker %q", ms)
	}
}

func TestStreamModes(t *testing.T) {
	s := newStream()
	feedAll(s, "\x1b[?1049h\x1b[?2004h\x1b[>4;2m\x1b[>1u\x1b[?1000h\x1b[?1006", "h\x1b[?25l\x1b[?1h")
	want := "\x1b[?1049h\x1b[?1h\x1b[?25l\x1b[?1000h\x1b[?1006h\x1b[?2004h\x1b[>4;2m\x1b[>1u"
	if got := s.restore(); got != want {
		t.Fatalf("restore %q, want %q", got, want)
	}
	want = "\x1b[0m\x1b[<1u\x1b[>4m\x1b[?1l\x1b[?1000l\x1b[?1006l\x1b[?2004l\x1b[?1049l\x1b[?25h"
	if got := s.reset(); got != want {
		t.Fatalf("reset %q, want %q", got, want)
	}
	// Turned back off: nothing to restore beyond the defaults.
	feedAll(s, "\x1b[<u\x1b[>4m\x1b[?2004l\x1b[?1000l\x1b[?1006l\x1b[?1l\x1b[?25h\x1b[?1049l")
	if got := s.restore(); got != "" {
		t.Fatalf("restore after off %q", got)
	}
}

func TestStreamPassesOtherBytes(t *testing.T) {
	s := newStream()
	in := "plain \x1b7\x1b[2J\x1b[38;5;3mcolor\x1b(B ünïcode"
	if out, _ := feedAll(s, in); out != in {
		t.Fatalf("out %q", out)
	}
	long := "\x1b]8;;" + strings.Repeat("x", maxSeq+10) + "\x07"
	if out, _ := feedAll(s, long); out != long {
		t.Fatal("a long OSC was not passed through whole")
	}
}

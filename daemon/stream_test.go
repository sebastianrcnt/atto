package daemon

import (
	"os"
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
	s.token = "secret"
	m := markerSeq("secret", "session", "abc", "my name")
	out, ms := feedAll(s, "hi\x1b[1m", m[:5], m[5:12], m[12:]+"there\x1b]0;title\x07")
	if out != "hi\x1b[1mthere\x1b]0;title\x07" {
		t.Fatalf("out %q", out)
	}
	if len(ms) != 1 || ms[0] != "session;abc;my name" {
		t.Fatalf("markers %q", ms)
	}
	// ST-terminated markers count too.
	if _, ms := feedAll(s, "\x1b]7337;secret;detach\x1b\\"); len(ms) != 1 || ms[0] != "detach" {
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

func TestMarkersRequirePaneToken(t *testing.T) {
	s := newStream()
	s.token = "pane-secret"
	for _, spoof := range []string{"\x1b]7337;detach\x07", markerSeq("other-pane", "new", "/tmp"), markerSeq("", "state", "working")} {
		out, markers := feedAll(s, spoof)
		if out != "" || len(markers) != 0 {
			t.Fatalf("accepted unauthenticated marker: output %q markers %q", out, markers)
		}
	}
	_, markers := feedAll(s, markerSeq("pane-secret", "state", "working"))
	if len(markers) != 1 || markers[0] != "state;working" {
		t.Fatalf("authenticated markers %q", markers)
	}
	// A client stream has no token and cannot interpret pane controls.
	_, markers = feedAll(newStream(), markerSeq("pane-secret", "detach"))
	if len(markers) != 0 {
		t.Fatal("client stream accepted a marker")
	}
}

func TestConsumePaneToken(t *testing.T) {
	old := paneToken
	defer func() { paneToken = old }()
	t.Setenv(EnvPaneToken, "test-secret")
	ConsumePaneToken()
	if _, ok := os.LookupEnv(EnvPaneToken); ok {
		t.Fatal("token left in child environment")
	}
	if got := MarkerSeq("ready"); got != markerSeq("test-secret", "ready") {
		t.Fatalf("marker %q", got)
	}
}

func TestKeyboardStacksAreScreenLocal(t *testing.T) {
	s := newStream()
	feedAll(s, "\x1b[>2u\x1b[?1049h\x1b[>1u")
	want := "\x1b[>2u\x1b[?1049h\x1b[>1u"
	if got := s.restore(); got != want {
		t.Fatalf("restore %q, want %q", got, want)
	}
	reset := s.reset()
	if altPop, leave, mainPop := strings.Index(reset, "\x1b[<1u"), strings.Index(reset, "\x1b[?1049l"), strings.LastIndex(reset, "\x1b[<1u"); !(altPop < leave && leave < mainPop) {
		t.Fatalf("screen-local reset order: %q", reset)
	}
	feedAll(s, "\x1b[<u\x1b[?1049l")
	if got := s.restore(); got != "\x1b[>2u" {
		t.Fatalf("main stack lost: %q", got)
	}
	feedAll(s, "\x1b[<u")
	if got := s.restore(); got != "" {
		t.Fatalf("main pop left flags: %q", got)
	}
}

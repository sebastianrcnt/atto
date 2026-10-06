package tui

import "testing"

// TestTruncatePlainStaysPlain: the tail Reset is only there to close styles
// the kept text left open. Injecting it into a plain string leaks raw
// escapes into anything that is not a terminal ("…\x1b[0m\x1b]8;;\x07").
func TestTruncatePlainStaysPlain(t *testing.T) {
	if got, want := Truncate("fix the basement plumbing for good", 8, "…"), "fix the…"; got != want {
		t.Fatalf("Truncate plain = %q, want %q", got, want)
	}
	// Styles and hyperlinks are still closed on cut.
	styled := FG(2, "fix the basement plumbing for good") // "\x1b[38;5;2m…\x1b[39m"
	if got, want := Truncate(styled, 8, "…"), "\x1b[38;5;2mfix the"+Reset+"…"; got != want {
		t.Fatalf("Truncate styled = %q, want %q", got, want)
	}
	link := "\x1b]8;;http://x\x07fix the basement\x1b]8;;\x07"
	if got := Truncate(link, 20, "…"); got != link {
		t.Fatalf("Truncate link = %q, want %q", got, link)
	}
	if got, want := Truncate(link, 5, "…"), "\x1b]8;;http://x\x07fix "+Reset+"…"; got != want {
		t.Fatalf("Truncate cut link = %q, want %q", got, want)
	}
	// Styles past the cut are dropped with the text, so nothing is open.
	if got, want := Truncate("abc"+FG(2, "def"), 4, "…"), "abc…"; got != want {
		t.Fatalf("Truncate = %q, want %q", got, want)
	}
}

package server

import (
	"slices"
	"testing"

	"github.com/sebastianrcnt/atto/core/transcript"
	"github.com/sebastianrcnt/atto/events"
)

// An event item carries one title per [atto event], as the TUI shows them,
// and the full text the model got.
func TestWireEventItemTitles(t *testing.T) {
	text := events.Prefix + "Background job 10 exited with code 1 after 7m34s. Last output:\nTraceback (most recent call last):\n  File \"x.py\"" +
		"\n\n" + events.Prefix + "Timer t1 fired: check the build"
	w := wireItem("s1", &transcript.Item{ID: "i1", Kind: transcript.Event, Text: text})
	want := []string{"Background job 10 exited with code 1 after 7m34s. Last output:", "Timer t1 fired: check the build"}
	if w.Type != ItemEvent || !slices.Equal(w.Titles, want) || w.Text != text {
		t.Fatalf("%+v", w)
	}
}

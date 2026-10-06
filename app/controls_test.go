package app

import (
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/agent"
)

func TestToolHeadersDoNotEmitUntrustedMarkers(t *testing.T) {
	marker := "\x1b]7337;new;/tmp\x07"
	for _, done := range []bool{false, true} {
		b := &toolBlock{args: agent.BashArgs{Command: "echo " + marker, Description: "description " + marker}, done: done}
		for _, line := range b.Render(120) {
			if strings.Contains(line, "\x1b]7337") {
				t.Fatalf("tool emitted marker %q", line)
			}
		}
	}
	for _, line := range commandLines("echo "+marker, 120, 0) {
		if strings.Contains(line, "\x1b]7337") {
			t.Fatalf("command emitted marker %q", line)
		}
	}
}

func TestShellAndExtensionTitlesDoNotEmitMarkers(t *testing.T) {
	marker := "\x1b]7337;detach\x07"
	for _, component := range []interface{ Render(int) []string }{
		&shellBlock{cmd: "echo " + marker},
		&extTextBlock{title: marker, ext: marker, text: marker},
		&userBlock{text: marker},
	} {
		for _, line := range component.Render(120) {
			if strings.Contains(line, "\x1b]7337") {
				t.Fatalf("emitted marker %q", line)
			}
		}
	}
}

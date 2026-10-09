package app

import (
	"fmt"
	"github.com/sebastianrcnt/atto/tui"
	"os"
	"testing"
)

func builtinGolden(t *testing.T, name string, width int, lines []string) {
	t.Helper()
	got := plainLines(lines) + "\n"
	path := fmt.Sprintf("testdata/ui-%s-%d.txt", name, width)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		t.Fatalf("%s mismatch:\n%s", path, got)
	}
	for _, l := range lines {
		if tui.VisibleWidth(l) > width {
			t.Fatalf("%s overflow", name)
		}
	}
}

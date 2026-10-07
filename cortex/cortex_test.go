package cortex

import (
	"strings"
	"testing"

	"atto2/kernel"
)

func TestInstructionsGrant(t *testing.T) {
	for _, names := range [][]string{{"now", "exit"}, {"bash", "now", "exit"}} {
		k, err := kernel.WithGrant(t.TempDir(), names...)
		if err != nil {
			t.Fatal(err)
		}
		got := Instructions(k, "project")
		if strings.Contains(got, "bash") != k.Granted("bash") {
			t.Fatal(got)
		}
		if !strings.Contains(got, k.Instructions()) {
			t.Fatal(got)
		}
	}
}

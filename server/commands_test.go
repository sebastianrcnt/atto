package server

import (
	"strings"
	"testing"
)

func TestRemovedDetachCommandPointer(t *testing.T) {
	for _, c := range Builtins {
		if c.Name == "detach" {
			t.Fatal("removed command is advertised")
		}
	}
	_, _, err := ResolveCommand(Builtins, "/detach")
	if err == nil || !strings.Contains(err.Error(), "atto resume") || strings.Contains(err.Error(), "Unknown") {
		t.Fatalf("removed command: %v", err)
	}
}

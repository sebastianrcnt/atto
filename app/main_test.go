package app

import (
	"os"
	"testing"

	"github.com/sebastianrcnt/atto/internal/testhome"
)

// New scans the home directory for skills; keep tests off the real one.
func TestMain(m *testing.M) {
	cleanup, err := testhome.Use()
	if err != nil {
		panic(err)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}

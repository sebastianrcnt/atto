//go:build noext

package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sebastianrcnt/atto/config"
)

func TestSlimHasNativeCommandsButNoExtensionCommands(t *testing.T) {
	h := newHarness(t)
	path := filepath.Join(config.ExtensionsDir(), "ignored.ts")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`throw new Error("must not run")`), 0600); err != nil {
		t.Fatal(err)
	}
	h.call("thread/reload", nil)
	h.wait("thread/reloaded", nil)
	th, err := h.s.thread(h.id)
	if err != nil {
		t.Fatal(err)
	}
	if err := th.call(func() error {
		seen := map[string]bool{}
		for _, c := range th.commands() {
			if c.Origin == "extension" {
				t.Errorf("extension surface present: %+v", c)
			}
			seen[c.Name] = true
		}
		if !seen["diff"] || !seen["autorename"] {
			t.Errorf("missing native commands: %+v", seen)
		}
		if !th.ui.Empty() {
			t.Error("extension UI present")
		}
		if len(th.ext.Report()) != 1 || th.ext.Report()[0].Status != "ignored (slim)" {
			t.Errorf("ignored files missing: %+v", th.ext.Report())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

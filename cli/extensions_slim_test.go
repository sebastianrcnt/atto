//go:build noext

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/config"
)

func TestSlimExtensionsListAndApprove(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	if err := config.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config.ExtensionsDir(), "test.ts"), []byte("throw 1"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunExtensions(nil, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "this atto build has no extension support (slim); 1 extension files ignored") {
		t.Fatal(out.String())
	}
	if err := RunExtensions([]string{"approve", "test"}, &out); err == nil || !strings.Contains(err.Error(), "1 extension files ignored") {
		t.Fatalf("%v", err)
	}
}

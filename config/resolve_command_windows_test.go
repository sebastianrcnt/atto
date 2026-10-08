//go:build windows

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sebastianrcnt/atto/shell"
)

func TestCommandValuesUseRawCmdLine(t *testing.T) {
	// Config commands have always used cmd, even if the agent uses PowerShell.
	t.Setenv(shell.EnvOverride, "powershell.exe")
	dir := filepath.Join(t.TempDir(), "path with spaces")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "say.cmd")
	if err := os.WriteFile(path, []byte("@echo path success\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for command, want := range map[string]string{
		`!echo "a b"`:              `"a b"`,
		`!echo "x"& echo "y"`:      "\"x\"\r\n\"y\"",
		fmt.Sprintf(`!"%s"`, path): "path success",
	} {
		if got, ok := ResolveConfigValueUncached(command, nil); !ok || got != want {
			t.Errorf("%s = %q (%v), want %q", command, got, ok, want)
		}
	}
}

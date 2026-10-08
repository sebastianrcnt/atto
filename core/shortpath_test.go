package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestShortPathWholeElements(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if got, err := os.UserHomeDir(); err != nil || got != home {
		t.Fatalf("home %q: %v", got, err)
	}
	for path, want := range map[string]string{home: "~", filepath.Join(home, "file"): "~" + string(filepath.Separator) + "file", home + "2": home + "2"} {
		if got := ShortPath(path); got != want {
			t.Errorf("ShortPath(%q) = %q, want %q", path, got, want)
		}
	}
}

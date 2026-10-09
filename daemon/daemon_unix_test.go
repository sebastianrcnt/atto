//go:build !windows

package daemon

import (
	"os"
	"testing"

	"github.com/sebastianrcnt/atto/config"
)

func TestSocketDirectoryRejectsUnsafePaths(t *testing.T) {
	for _, mode := range []os.FileMode{0o755, 0o777} {
		dir := t.TempDir()
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		if err := privateDir(dir); err == nil {
			t.Fatalf("accepted mode %o", mode)
		}
	}
	root := t.TempDir()
	link := root + "/link"
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if err := privateDir(link); err == nil {
		t.Fatal("accepted a symlink")
	}
	dir := root + "/private"
	if err := privateDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := privateDir(dir); err != nil {
		t.Fatal(err)
	}
}

func TestDialRejectsUnsafeDirectoryBeforeHello(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	if err := os.MkdirAll(RunDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(RunDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if c, err := dial(false); err == nil {
		c.Close()
		t.Fatal("accepted unsafe socket directory")
	}
}

func TestPeerUID(t *testing.T) {
	if err := peerUID(uint32(os.Getuid())); err != nil {
		t.Fatal(err)
	}
	if err := peerUID(uint32(os.Getuid()) + 1); err == nil {
		t.Fatal("accepted another user's daemon")
	}
}

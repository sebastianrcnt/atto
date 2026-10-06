package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHeadLongLine(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	path := OutputPath("s", 1)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("x", 2<<20)
	if err := os.WriteFile(path, []byte(line+"\nlast"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Head("s", 1, 1)
	if err != nil || got != line {
		t.Fatalf("head: %d bytes, %v", len(got), err)
	}
	got, err = Head("s", 1, 3)
	if err != nil || got != line+"\nlast" {
		t.Fatalf("head to EOF: %d bytes, %v", len(got), err)
	}
}

func TestCappedFileNoticesFinalTruncatingWrite(t *testing.T) {
	for _, room := range []int64{0, 4} {
		f, err := os.CreateTemp(t.TempDir(), "output")
		if err != nil {
			t.Fatal(err)
		}
		c := cappedFile{f: f, n: maxLog - room}
		if n, err := c.Write([]byte("abcdefgh")); n != 8 || err != nil {
			t.Fatalf("write: %d, %v", n, err)
		}
		if _, err := c.Write([]byte("ignored")); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(data), "output beyond") != 1 || !strings.HasPrefix(string(data), "abcdefgh"[:room]) {
			t.Fatalf("truncation notice: %q", data)
		}
	}
}

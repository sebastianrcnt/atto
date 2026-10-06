package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/jobs"
)

func TestJobOutputFlagsAfterID(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	t.Setenv("ATTO_SESSION_ID", "s")
	path := jobs.OutputPath("s", 1)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"output", "1", "-tail", "1"}, "three\n"},
		{[]string{"output", "1", "-head", "1"}, "one\n"},
		{[]string{"log", "-tail", "1", "1"}, "three\n"},
		{[]string{"output", "1", "-session", "s", "-tail=2"}, "two\nthree\n"},
	} {
		var out bytes.Buffer
		if err := RunJob(tc.args, &out); err != nil {
			t.Fatal(err)
		}
		if got := out.String(); got != tc.want {
			t.Errorf("%v: %q, want %q", tc.args, got, tc.want)
		}
	}
	var out bytes.Buffer
	if err := RunJob([]string{"output", "1", "-tail", "oops"}, &out); err == nil || !strings.Contains(err.Error(), "invalid value") {
		t.Fatalf("bad trailing flag: %v", err)
	}
}

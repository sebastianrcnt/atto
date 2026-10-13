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

// A broken job record must not turn a partial/failed listing into "no jobs".
func TestJobListReportsUnreadableStateAndKeepsPartialResults(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	badDir := filepath.Join(jobs.Root("s"), "1")
	if err := os.MkdirAll(badDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "job.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunJob([]string{"list", "-session", "s"}, &out); err == nil || strings.Contains(out.String(), "no jobs") {
		t.Fatalf("failed list: %q, %v", out.String(), err)
	}
	goodDir := filepath.Join(jobs.Root("s"), "2")
	if err := os.Mkdir(goodDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goodDir, "job.json"), []byte(`{"id":2,"session":"s","status":"exited","name":"readable job"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := RunJob([]string{"list", "-session", "s"}, &out); err == nil || !strings.Contains(out.String(), "readable job") {
		t.Fatalf("partial list: %q, %v", out.String(), err)
	}
}

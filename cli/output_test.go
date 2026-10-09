package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/outputs"
)

func runOutput(args ...string) (string, error) {
	var out strings.Builder
	err := RunOutput(args, &out)
	return out.String(), err
}

func TestRunOutput(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	t.Setenv("ATTO_SESSION_ID", "sess-1")
	var text strings.Builder
	for i := 1; i <= 1000; i++ {
		fmt.Fprintf(&text, "line %d\n", i)
	}
	w := outputs.New(outputs.Options{Session: "sess-1", Name: "call_7", Keep: 50, Limits: &outputs.Limits{MinFree: -1}})
	w.Write([]byte(text.String()))
	saved, err := w.Save()
	if err != nil || saved.Path == "" {
		t.Fatalf("%+v %v", saved, err)
	}

	got, err := runOutput(saved.Path)
	if err != nil || got != text.String() {
		t.Fatalf("whole: %d bytes, %v", len(got), err)
	}
	// Flags may follow the path, as in the hint given to the model.
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{saved.Path, "-head", "2"}, "line 1\nline 2\n"},
		{[]string{"-head", "2", saved.Path}, "line 1\nline 2\n"},
		{[]string{saved.Path, "-tail", "3"}, "line 998\nline 999\nline 1000\n"},
		{[]string{saved.Path, "-tail", "5000"}, text.String()},
		{[]string{saved.Path, "-grep", `^line 10[05]$`}, "line 100\nline 105\n"},
		{[]string{saved.Path, "-i", "-grep", `LINE 99\d$`, "-tail", "2"}, "line 998\nline 999\n"},
		{[]string{saved.Path, "-grep", "line 9", "-head", "3"}, "line 9\nline 90\nline 91\n"},
		{[]string{"call_7", "-tail", "1"}, "line 1000\n"},
		{[]string{"call_7.log.zst", "-head", "1"}, "line 1\n"},
		{[]string{saved.Path, "-grep", "nothing like this"}, ""},
	} {
		if got, err := runOutput(tc.args...); err != nil || got != tc.want {
			t.Errorf("%v: %q, %v; want %q", tc.args, got, err, tc.want)
		}
	}
	// A call id from another session is found too.
	t.Setenv("ATTO_SESSION_ID", "other")
	if got, err := runOutput("call_7", "-tail", "1"); err != nil || got != "line 1000\n" {
		t.Errorf("other session: %q, %v", got, err)
	}
	for _, args := range [][]string{nil, {"a", "b"}, {saved.Path, "-head", "1", "-tail", "1"}, {saved.Path, "-head", "-1"}, {saved.Path, "-grep", "("}, {"no-such-call"}, {filepath.Join(t.TempDir(), "x")}} {
		if _, err := runOutput(args...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}

// Plain log files, as older versions left in $TMPDIR, read the same.
func TestRunOutputPlainFile(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	p := filepath.Join(t.TempDir(), "atto-bash-123.log")
	if err := os.WriteFile(p, []byte("a\r\nb\nc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := runOutput(p); err != nil || got != "a\r\nb\nc" {
		t.Fatalf("%q %v", got, err)
	}
	if got, err := runOutput(p, "-grep", "^a$", "-tail", "1"); err != nil || got != "a\r\n" {
		t.Fatalf("%q %v", got, err)
	}
	if got, err := runOutput(p, "-tail", "1"); err != nil || got != "c" {
		t.Fatalf("%q %v", got, err)
	}
}

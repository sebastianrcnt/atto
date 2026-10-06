package jobs

import (
	"os"
	"runtime"
	"testing"
)

func TestJobPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	t.Setenv("ATTO_DIR", t.TempDir())
	if err := os.MkdirAll(Root("s"), 0o755); err != nil {
		t.Fatal(err)
	}
	id, dir, err := reserve("s")
	if err != nil {
		t.Fatal(err)
	}
	if err := save(dir, Job{ID: id, Session: "s"}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{Root("s"), dir} {
		st, err := os.Stat(path)
		if err != nil || st.Mode().Perm() != 0o700 {
			t.Fatalf("directory %s: %v, %v", path, st, err)
		}
	}
	st, err := os.Stat(dir + string(os.PathSeparator) + "job.json")
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("job file: %v, %v", st, err)
	}
}

package maint

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/session"
)

func age(t *testing.T, p string, d time.Duration) {
	t.Helper()
	old := time.Now().Add(-d)
	if e := os.Chtimes(p, old, old); e != nil {
		t.Fatal(e)
	}
}
func idleTemps(t *testing.T) {
	t.Helper()
	old := runtimeProcesses
	runtimeProcesses = func() bool { return false }
	t.Cleanup(func() { runtimeProcesses = old })
}
func TestCleanTableAndCategories(t *testing.T) {
	idleTemps(t)
	root := t.TempDir()
	temp := t.TempDir()
	t.Setenv(config.EnvDir, root)
	existing := "abcdef12"
	p := filepath.Join(root, "sessions", existing+".jsonl")
	put(t, p, []byte("{\"type\":\"session\",\"id\":\"abcdef12\"}\n"))
	put(t, filepath.Join(root, "settings.json"), []byte("keep"))
	remove := []string{filepath.Join(root, "outputs/deleted/log.zst"), filepath.Join(root, "debug/old.dump"), filepath.Join(root, "jobs/deleted/1/job.json"), filepath.Join(temp, "atto-bash-123.log"), filepath.Join(temp, "atto-transcript-123"), filepath.Join(temp, "atto-view-123/image.png"), filepath.Join(temp, "atto-home123/go/mod/readonly"), filepath.Join(temp, "atto-session-test123/file"), filepath.Join(temp, "atto-swing-123/file")}
	for _, p := range remove {
		b := []byte("remove")
		if strings.HasSuffix(p, "job.json") {
			b = []byte("{\"id\":1,\"session\":\"deleted\",\"status\":\"exited\"}")
		}
		put(t, p, b)
		age(t, p, 40*24*time.Hour)
	}
	for _, n := range []string{"atto-home123", "atto-session-test123", "atto-swing-123"} {
		age(t, filepath.Join(temp, n), 48*time.Hour)
	}
	os.Chmod(filepath.Join(temp, "atto-home123/go/mod"), 0o500)
	t.Cleanup(func() { os.Chmod(filepath.Join(temp, "atto-home123/go/mod"), 0o700) })
	keep := []string{filepath.Join(root, "outputs", existing, "log.zst"), filepath.Join(root, "outputs/deleted/recent.zst"), filepath.Join(root, "debug/recent.dump"), filepath.Join(temp, "unrelated.log"), filepath.Join(temp, "atto-home-new/file")}
	for _, p := range keep {
		put(t, p, []byte("keep"))
	}
	age(t, keep[0], 40*24*time.Hour)
	external := filepath.Join(root, "sessions/external.jsonl")
	put(t, external, []byte("{\"type\":\"session\",\"version\":1,\"id\":\"external\",\"external\":true,\"name\":\"atto agent (external)\"}\n"))
	mapping := filepath.Join(root, "external_parents/hash")
	put(t, mapping, []byte("external\n"))
	remove = append(remove, external, mapping)
	empty := filepath.Join(root, "sessions/empty-external.jsonl")
	emptyMap := filepath.Join(root, "external_parents/empty-hash")
	put(t, empty, nil)
	put(t, emptyMap, []byte("empty-external"))
	remove = append(remove, empty, emptyMap)
	var out bytes.Buffer
	o := CleanOptions{Root: root, Temp: temp, DryRun: true, Out: &out}
	plan, e := Clean(o)
	if e != nil {
		t.Fatal(e)
	}
	if len(plan.Items) < 10 || !strings.Contains(out.String(), "CATEGORY") || !strings.Contains(out.String(), "BYTES") {
		t.Fatalf("%+v\n%s", plan, out.String())
	}
	for _, p := range remove {
		if _, e := os.Stat(p); e != nil {
			t.Fatal("dry run removed", p)
		}
	}
	o.DryRun = false
	o.Yes = true
	if _, e = Clean(o); e != nil {
		t.Fatal(e)
	}
	for _, p := range remove {
		if _, e := os.Stat(p); !os.IsNotExist(e) {
			t.Errorf("not removed %s: %v", p, e)
		}
	}
	keep = append(keep, p, filepath.Join(root, "settings.json"))
	for _, p := range keep {
		if _, e := os.Stat(p); e != nil {
			t.Errorf("removed %s", p)
		}
	}
}
func TestCleanKeepsActiveJobAndLockedExternalSession(t *testing.T) {
	idleTemps(t)
	root := t.TempDir()
	t.Setenv(config.EnvDir, root)
	temp := t.TempDir()
	file := filepath.Join(root, "sessions/external.jsonl")
	put(t, file, []byte("{\"type\":\"session\",\"version\":1,\"id\":\"external\",\"external\":true}\n"))
	release, e := session.Lock(file)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	j := jobs.Job{ID: 1, Session: "deleted", Status: jobs.Running, SupervisorPID: os.Getpid(), Started: time.Now()}
	b, _ := json.Marshal(j)
	job := filepath.Join(root, "jobs/deleted/1/job.json")
	put(t, job, b)
	output := filepath.Join(root, "outputs/deleted/log.zst")
	put(t, output, []byte("active"))
	age(t, output, 40*24*time.Hour)
	p, e := Clean(CleanOptions{Root: root, Temp: temp, Yes: true})
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Kept) < 3 {
		t.Fatalf("kept %+v", p.Kept)
	}
	for _, p := range []string{file, job, output} {
		if _, e := os.Stat(p); e != nil {
			t.Fatal("active item removed", p)
		}
	}
}
func TestCleanKeepsOpenTempFile(t *testing.T) {
	idleTemps(t)
	temp := t.TempDir()
	root := t.TempDir()
	file := filepath.Join(temp, "atto-transcript-open")
	put(t, file, []byte("open"))
	f, e := os.Open(file)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	_, e = Clean(CleanOptions{Root: root, Temp: temp, Yes: true})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(file); e != nil {
		t.Fatal("open file deleted")
	}
}

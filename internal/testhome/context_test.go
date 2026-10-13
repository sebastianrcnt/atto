package testhome

import (
	"os"
	"path/filepath"
	"testing"
)

var executionContextKeys = []string{"ATTO_SESSION_ID", "ATTO_AGENT", "ATTO_SUBAGENT", "ATTO_TOOL_CALL_ID"}

func poisonExecutionContext(t *testing.T) {
	t.Helper()
	for _, key := range executionContextKeys {
		t.Setenv(key, "inherited-from-calling-session")
	}
}

func assertExecutionContextCleared(t *testing.T) {
	t.Helper()
	for _, key := range executionContextKeys {
		if value, present := os.LookupEnv(key); present {
			t.Errorf("%s is still present: %q", key, value)
		}
	}
}

func TestClearExecutionContext(t *testing.T) {
	poisonExecutionContext(t)
	// Do not clear unrelated settings or the environment a helper process
	// will need: these are deliberately controlled by individual fixtures.
	t.Setenv("ATTO_DIR", t.TempDir())
	t.Setenv("ATTO_VIEW_DIR", "fixture-view")
	t.Setenv("ATTO_NO_DAEMON", "1")
	dir := os.Getenv("ATTO_DIR")
	if err := ClearExecutionContext(); err != nil {
		t.Fatal(err)
	}
	assertExecutionContextCleared(t)
	if os.Getenv("ATTO_DIR") != dir || os.Getenv("ATTO_VIEW_DIR") != "fixture-view" || os.Getenv("ATTO_NO_DAEMON") != "1" {
		t.Fatal("clearing execution context changed unrelated settings")
	}
	// Explicit fixtures remain possible after ordinary test initialization.
	t.Setenv("ATTO_SESSION_ID", "fixture-session")
	t.Setenv("ATTO_TOOL_CALL_ID", "fixture-call")
	if os.Getenv("ATTO_SESSION_ID") != "fixture-session" || os.Getenv("ATTO_TOOL_CALL_ID") != "fixture-call" {
		t.Fatal("explicit test context was not kept")
	}
}

func TestUseClearsExecutionContextAndPreservesGoCaches(t *testing.T) {
	poisonExecutionContext(t)
	// Preserve this test process's environment when t.Cleanup runs, since Use
	// normally runs once in TestMain and intentionally does not restore it.
	for _, key := range []string{"HOME", "USERPROFILE", "GOPATH", "GOCACHE", "GOENV"} {
		t.Setenv(key, os.Getenv(key))
	}
	cacheRoot := t.TempDir()
	cachePaths := map[string]string{
		"GOPATH":  filepath.Join(cacheRoot, "gopath"),
		"GOCACHE": filepath.Join(cacheRoot, "cache"),
		"GOENV":   filepath.Join(cacheRoot, "goenv"),
	}
	for key, value := range cachePaths {
		t.Setenv(key, value)
	}
	cleanup, err := Use()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	assertExecutionContextCleared(t)
	home := os.Getenv("HOME")
	if home == "" || os.Getenv("USERPROFILE") != home {
		t.Fatalf("private home: HOME=%q USERPROFILE=%q", home, os.Getenv("USERPROFILE"))
	}
	if _, err := os.Stat(home); err != nil {
		t.Fatal(err)
	}
	for key, value := range cachePaths {
		if got := os.Getenv(key); got != value {
			t.Errorf("%s=%q, want %q", key, got, value)
		}
	}
	cleanup()
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("temporary home not removed: %v", err)
	}
}

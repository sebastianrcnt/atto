package fsutil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

type jsonCounter struct {
	Count int `json:"count"`
}

func TestReadAndEditJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "state.json")
	if got, err := ReadJSON[jsonCounter](path); err != nil || got.Count != 0 {
		t.Fatal(got, err)
	}
	if err := EditJSON(path, func(c *jsonCounter) { c.Count++ }); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadJSON[jsonCounter](path); err != nil || got.Count != 1 {
		t.Fatal(got, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "{\n  \"count\": 1\n}\n" {
		t.Fatalf("JSON: %s: %v", data, err)
	}
	if runtime.GOOS != "windows" {
		for p, want := range map[string]os.FileMode{path: 0o600, path + ".lock": 0o600, filepath.Dir(path): 0o700} {
			st, err := os.Stat(p)
			if err != nil || st.Mode().Perm() != want {
				t.Fatalf("mode of %s: %v %v", p, st, err)
			}
		}
	}
	broken := []byte(`{"count":5,`)
	if err := os.WriteFile(path, broken, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadJSON[jsonCounter](path); err == nil || got.Count != 0 {
		t.Fatal("exposed malformed JSON", got, err)
	}
	if err := EditJSON(path, func(c *jsonCounter) { c.Count++ }); err == nil {
		t.Fatal("overwrote malformed JSON")
	}
	if data, _ := os.ReadFile(path); string(data) != string(broken) {
		t.Fatal("malformed file was changed")
	}
}

func TestEditJSONConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 8 {
				if err := EditJSON(path, func(c *jsonCounter) { c.Count++ }); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	if got, err := ReadJSON[jsonCounter](path); err != nil || got.Count != 128 {
		t.Fatalf("lost updates: %+v %v", got, err)
	}
}

func TestEditJSONProcessHelper(t *testing.T) {
	path := os.Getenv("ATTO_JSON_TEST_PATH")
	if path == "" {
		return
	}
	for range 10 {
		if err := EditJSON(path, func(c *jsonCounter) { c.Count++ }); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEditJSONConcurrentProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var commands []*exec.Cmd
	for range 4 {
		cmd := exec.Command(exe, "-test.run=^TestEditJSONProcessHelper$")
		cmd.Env = append(os.Environ(), "ATTO_JSON_TEST_PATH="+path)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, cmd)
	}
	for i, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Error(fmt.Errorf("writer %d: %w", i, err))
		}
	}
	if got, err := ReadJSON[jsonCounter](path); err != nil || got.Count != 40 {
		t.Fatalf("lost process updates: %+v %v", got, err)
	}
}

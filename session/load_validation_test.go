package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRejectsInvalidHeader(t *testing.T) {
	for _, data := range []string{"", "\n", "{broken}\n", "{broken}\n{\"type\":\"session\"}\n", "{\"type\":\"message\"}\n"} {
		path := filepath.Join(t.TempDir(), "session.jsonl")
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Load(path); err == nil {
			t.Errorf("accepted invalid header %q", data)
		}
	}
}

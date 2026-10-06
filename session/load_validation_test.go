package session

import (
	"github.com/sebastianrcnt/atto/provider"
	"os"
	"path/filepath"
	"strings"
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

func TestLoadLongEntry(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	w := New("/work")
	text := strings.Repeat("x", 65<<20)
	w.Append(Entry{Type: TypeMessage, Message: &provider.Message{Role: "user", Content: text}})
	w.Close()
	if err := w.Err(); err != nil {
		t.Fatal(err)
	}
	_, entries, err := Load(w.Path)
	if err != nil || len(entries) != 1 {
		t.Fatalf("load long entry: %d entries, %v", len(entries), err)
	}
	if entries[0].Message.Content != text {
		t.Fatal("long entry changed")
	}
}

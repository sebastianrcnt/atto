package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/session"
)

func TestArchiveRPC(t *testing.T) {
	h := newHarness(t)
	h.call("input/submit", map[string]any{"input": "save before archiving"})
	h.completed()
	old, err := session.Find(h.id)
	if err != nil {
		t.Fatal(err)
	}
	out := h.call("thread/archive", nil)
	path := out["path"].(string)
	if !strings.HasPrefix(path, config.ArchivedDir()+string(filepath.Separator)) {
		t.Fatalf("archive path: %q", path)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("active file remains: %v", err)
	}
	if _, _, err := session.Load(path); err != nil {
		t.Fatal(err)
	}
	if h.s.Loaded(h.id) {
		t.Fatal("archive retained runtime/writer")
	}
}

func TestListIncludesLiveEmptySessions(t *testing.T) {
	h := newHarness(t)
	h.call("thread/setName", map[string]any{"name": "Empty workspace"})
	value, err := h.s.listThreads(threadParams{})
	if err != nil {
		t.Fatal(err)
	}
	rows := value.(map[string]any)["threads"].([]map[string]any)
	found := false
	for _, row := range rows {
		if row["threadId"] == h.id {
			found = true
			if row["name"] != "Empty workspace" || row["loaded"] != true || row["busy"] != false {
				t.Fatalf("live metadata: %#v", row)
			}
		}
	}
	if !found {
		t.Fatalf("empty live session missing: %#v", rows)
	}
	value, err = h.s.listThreads(threadParams{Cwd: t.TempDir()})
	if err != nil || len(value.(map[string]any)["threads"].([]map[string]any)) != 0 {
		t.Fatalf("cwd filter: %#v %v", value, err)
	}
}

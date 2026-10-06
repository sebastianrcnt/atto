package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/session"
)

func TestCurrentSessionArchivePreservesWriterLease(t *testing.T) {
	for _, busy := range []bool{true, false} {
		t.Run(map[bool]string{true: "running", false: "failed"}[busy], func(t *testing.T) {
			a := treeApp(t)
			t.Cleanup(a.closeSession)
			a.record("user", "kept")
			path := a.sess.Path
			a.busy = busy
			if !busy {
				rel, err := filepath.Rel(config.SessionsDir(), path)
				if err != nil {
					t.Fatal(err)
				}
				dst := filepath.Join(config.ArchivedDir(), rel)
				if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(dst, []byte("occupied"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			a.cmdSessions("")
			p := a.modal.(*resumePicker)
			p.onArchive(session.Summary{Path: path})
			if a.sess.Path != path {
				t.Fatal("refused archive switched session")
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal(err)
			}
			if _, locked := session.LockedBy(path); !locked || a.unlock == nil {
				t.Fatal("refused archive lost writer lease")
			}
		})
	}
}

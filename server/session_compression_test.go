package server

import (
	"os"
	"testing"

	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

func TestResumeRestoresCompressedArchive(t *testing.T) {
	work := setup(t)
	w := session.New(work)
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "archived question"}})
	w.Close()
	dst, err := session.Archive(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	s := New("test", work)
	defer s.Close()
	if _, err := s.resumeThread("", threadParams{ThreadID: w.ID}); err != nil {
		t.Fatal("resume archive", err)
	}
	if p, err := session.Find(w.ID); err != nil || p != w.Path {
		t.Fatal("resume did not restore", p, err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("archive remains", err)
	}
	if _, locked := session.LockedBy(w.Path); !locked {
		t.Fatal("restored writer has no lease")
	}
	s.threads[w.ID].sess.Append(session.Entry{Type: session.TypeName, Name: "resumed name"})
	if err := s.threads[w.ID].sess.Err(); err != nil {
		t.Fatal(err)
	}
	if _, entries, err := session.Load(w.Path); err != nil || entries[len(entries)-1].Name != "resumed name" {
		t.Fatal(entries, err)
	}
}

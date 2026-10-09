package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/session"
)

func compressedCLISession(t *testing.T, agent bool) *session.Writer {
	t.Helper()
	w := session.New(t.TempDir())
	if agent {
		w = session.NewAgent(t.TempDir(), "parent")
	}
	w.Append(session.Entry{Type: session.TypeName, Name: "Archived title"})
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "compressed history needle"}})
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: "Agent answer"}})
	w.Close()
	return w
}

func TestCompressedSessionsCLIAndAgentTranscript(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	t.Setenv("ATTO_SESSION_ID", "")
	t.Setenv(config.EnvAgent, "")
	w := compressedCLISession(t, true)
	var out bytes.Buffer
	run := func(args ...string) {
		t.Helper()
		out.Reset()
		if err := RunSessions(args, &out); err != nil {
			t.Fatal(args, err)
		}
	}
	run("archive", w.ID)
	if !strings.Contains(out.String(), w.ID) || strings.Contains(out.String(), ".zst") {
		t.Fatal(out.String())
	}
	dst, err := session.Find(w.ID)
	if err != nil || !strings.HasSuffix(dst, ".zst") {
		t.Fatal(dst, err)
	}
	run("show", w.ID[:6])
	if !strings.Contains(out.String(), "Archived title") || !strings.Contains(out.String(), "compressed history needle") || !strings.Contains(out.String(), "archived") {
		t.Fatal(out.String())
	}
	out.Reset()
	if err := RunHistory([]string{"grep", "-session", w.ID[:6], "needle"}, &out); err != nil || !strings.Contains(out.String(), "compressed history needle") {
		t.Fatal(out.String(), err)
	}
	out.Reset()
	if err := RunHistory([]string{"show", "-session", w.ID, "2"}, &out); err != nil || !strings.Contains(out.String(), "compressed history needle") {
		t.Fatal(out.String(), err)
	}
	// This is exactly the archived command center's public transcript API.
	info, err := server.ReadOffline(w.ID)
	if err != nil || !info.Offline || info.Name != "Archived title" || len(info.Items) == 0 {
		t.Fatalf("center transcript %+v %v", info, err)
	}
	run("unarchive", w.ID)
	if p, err := session.Find(w.ID); err != nil || p != w.Path {
		t.Fatal(p, err)
	}
	run("archive", w.ID)
	run("delete", "-y", w.ID[:6])
	if !strings.Contains(out.String(), "Deleted session "+w.ID) {
		t.Fatal(out.String())
	}
	for _, p := range []string{w.Path, dst, session.LogPath(dst), session.LockPath(dst)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatal("deleted path remains", p, err)
		}
	}
}

func TestSessionsCompressCLI(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	t.Setenv(config.EnvAgent, "")
	w := compressedCLISession(t, false)
	original, _ := os.ReadFile(w.Path)
	rel, _ := filepath.Rel(config.SessionsDir(), w.Path)
	plain := filepath.Join(config.ArchivedDir(), rel)
	if err := os.MkdirAll(filepath.Dir(plain), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(w.Path, plain); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunSessions([]string{"compress"}, &out); err != nil || !strings.Contains(out.String(), "Compressed 1 archived session(s):") || !strings.Contains(out.String(), "bytes") {
		t.Fatal(out.String(), err)
	}
	if _, err := os.Stat(plain); !os.IsNotExist(err) {
		t.Fatal("plain still exists", err)
	}
	if _, err := os.Stat(plain + ".zst"); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := RunSessions([]string{"compress"}, &out); err != nil || !strings.Contains(out.String(), "Compressed 0 archived session(s): 0 -> 0 bytes") {
		t.Fatal(out.String(), err)
	}
	out.Reset()
	if err := RunSessions([]string{"list", "-all", "-archived", "-json"}, &out); err != nil || !strings.Contains(out.String(), w.ID) || !strings.Contains(out.String(), "Archived title") {
		t.Fatal(out.String(), err)
	}
	if err := RunSessions([]string{"unarchive", w.ID}, &out); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(w.Path); err != nil || !bytes.Equal(got, original) {
		t.Fatal("migration/restore changed bytes", err)
	}
}

func TestCompressedImageReferencesAndDeleteDuplicates(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	t.Setenv("ATTO_SESSION_ID", "")
	name := strings.Repeat("a", 64) + ".png"
	w := compressedCLISession(t, false)
	h, _, err := session.Load(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	r := session.Resume(w.Path, h)
	r.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "images/" + name}})
	r.Close()
	data, _ := os.ReadFile(w.Path)
	dst, err := session.Archive(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := imageRefs(dst)
	if err != nil || !refs[name] {
		t.Fatal(refs, err)
	}
	// Simulate a crash-left duplicate; live wins, and delete removes both.
	if err := os.WriteFile(w.Path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunSessions([]string{"delete", "-y", w.ID}, &out); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dst, w.Path} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatal(p, err)
		}
	}
}

func TestDeletePreservesImagesReferencedByCompressedArchives(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	t.Setenv("ATTO_SESSION_ID", "")
	t.Setenv(config.EnvAgent, "")
	name := strings.Repeat("b", 64) + ".png"
	if err := os.MkdirAll(images.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	imagePath := filepath.Join(images.Dir(), name)
	if err := os.WriteFile(imagePath, []byte("stored image"), 0o600); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for range 2 {
		w := session.New(t.TempDir())
		w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "user", Content: "images/" + name}})
		w.Close()
		if _, err := session.Archive(w.Path); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, w.ID)
	}
	var out bytes.Buffer
	if err := RunSessions([]string{"delete", "-y", ids[0]}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(imagePath); err != nil {
		t.Fatal("deleted shared image", err)
	}
	if err := RunSessions([]string{"delete", "-y", ids[1]}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(imagePath); !os.IsNotExist(err) {
		t.Fatal("unshared image remained", err)
	}
}

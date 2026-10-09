package session

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/provider"
)

func TestImageReferencesRoundTrip(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	w := New("/work")
	im := provider.Image{File: "ab12.png", MIME: "image/png", Width: 640, Height: 480, Data: []byte("pixels")}
	w.Append(Entry{Type: TypeMessage, Message: &provider.Message{Role: "user", Content: "look [image 1: 640x480 PNG]", Images: []provider.Image{im}}})
	w.Append(Entry{Type: TypeCompaction, Replacement: []provider.Message{{Role: "user", Content: "kept", Images: []provider.Image{im}}}})
	w.Close()

	raw, _ := os.ReadFile(w.Path)
	if strings.Contains(string(raw), "pixels") || strings.Contains(string(raw), "cGl4ZWxz") {
		t.Fatalf("image bytes written to the session: %s", raw)
	}
	_, entries, err := Load(w.Path)
	if err != nil || len(entries) != 2 {
		t.Fatal(err)
	}
	ref := im
	ref.Data = nil
	for _, got := range [][]provider.Image{entries[0].Message.Images, entries[1].Replacement[0].Images} {
		if len(got) != 1 || !reflect.DeepEqual(got[0], ref) {
			t.Fatalf("reference %+v, want %+v", got, ref)
		}
	}
}

func TestWriterLoadList(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	w := New("/work")
	if _, err := os.Stat(w.Path); !os.IsNotExist(err) {
		t.Fatal("file should be created lazily")
	}
	w.Append(Entry{Type: TypeMessage, Message: &provider.Message{Role: "user", Content: "hello there"}})
	w.Append(Entry{Type: TypeMessage, Message: &provider.Message{Role: "assistant", Content: "hi"}, Usage: &provider.Usage{PromptTokens: 5}})
	w.Close()

	// A crash mid-write leaves a partial line; it must be ignored.
	f, _ := os.OpenFile(w.Path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"message","mess`)
	f.Close()

	h, entries, err := Load(w.Path)
	if err != nil || h.ID != w.ID || h.Cwd != "/work" || len(entries) != 2 {
		t.Fatalf("load: %+v %d %v", h, len(entries), err)
	}
	if entries[1].Usage.PromptTokens != 5 {
		t.Fatalf("usage lost: %+v", entries[1])
	}

	// Resume appends to the same file.
	r := Resume(w.Path, h)
	r.Append(Entry{Type: TypeMessage, Message: &provider.Message{Role: "user", Content: "again"}})
	r.Close()

	l, err := List("/work", false)
	if err != nil || len(l) != 1 {
		t.Fatalf("list: %v %v", l, err)
	}
	if l[0].Preview != "hello there" || l[0].Messages != 3 {
		t.Fatalf("summary %+v", l[0])
	}
	if other, _ := List("/elsewhere", false); len(other) != 0 {
		t.Fatal("cwd filter failed")
	}
	if filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(w.Path)))) != filepath.Join(os.Getenv("ATTO_DIR"), "sessions") {
		t.Fatalf("unexpected layout %s", w.Path)
	}
}

func TestArchiveAndName(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	w := New("/w")
	w.Append(Entry{Type: TypeMessage, Message: &provider.Message{Role: "user", Content: "x"}})
	w.Append(Entry{Type: TypeName, Name: "first"})
	w.Append(Entry{Type: TypeName, Name: "renamed"})
	w.Close()
	if l, _ := List("", false); len(l) != 1 || l[0].Name != "renamed" {
		t.Fatalf("name: %+v", l)
	}
	p, err := Archive(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	if l, _ := List("", false); len(l) != 0 {
		t.Fatal("still active after archive")
	}
	if l, _ := List("", true); len(l) != 1 || !l[0].Archived || l[0].Path != p {
		t.Fatalf("archived list %+v", l)
	}
	back, err := Unarchive(p)
	if err != nil || back != w.Path {
		t.Fatalf("unarchive %s %v", back, err)
	}
}

func TestSameDir(t *testing.T) {
	if !SameDir("/a/b/", "/a/b") || SameDir("/a/b", "/a/c") {
		t.Fatal("clean paths compare")
	}
	if got := SameDir(`C:\Users\me\Desktop`, `C:\Users\me\desktop`); got != (runtime.GOOS == "windows") {
		t.Fatalf("case-insensitive only on Windows, got %v", got)
	}
}

// A write after Close (an extension's session_end, say) still lands in the
// file but leaves no handle open: Windows can't remove an open file.
func TestWriteAfterCloseKeepsNoHandle(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	w := New(t.TempDir())
	w.Append(Entry{Type: TypeName, Name: "a"})
	w.Close()
	w.Append(Entry{Type: TypeName, Name: "b"})
	if w.f != nil {
		t.Fatal("a write after Close left the file open")
	}
	if _, entries, err := Load(w.Path); err != nil || len(entries) != 2 || entries[1].Name != "b" {
		t.Fatalf("entries %+v, %v", entries, err)
	}
	if err := os.Remove(w.Path); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerSessionsAreNotListed(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	w := NewAgent("/work", "parent1")
	w.Append(Entry{Type: TypeMessage, Message: &provider.Message{Role: "user", Content: "task"}})
	w.Close()
	h, _, err := Load(w.Path)
	if err != nil || h.AgentOf != "parent1" {
		t.Fatalf("header %+v %v", h, err)
	}
	if l, _ := List("", false); len(l) != 0 {
		t.Fatalf("listed: %+v", l)
	}
	if p, err := Find(w.ID); err != nil || p != w.Path {
		t.Fatalf("find: %s %v", p, err)
	}
}

func TestFindNamesCaseInsensitiveAndAmbiguous(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	a := New(t.TempDir())
	a.Append(Entry{Type: TypeName, Name: "Fix parser"})
	a.Close()
	if path, err := Find("FIX PARSER"); err != nil || path != a.Path {
		t.Fatalf("name: %s %v", path, err)
	}
	b := New(t.TempDir())
	b.Append(Entry{Type: TypeName, Name: "fix parser"})
	b.Close()
	if _, err := Find("Fix parser"); err == nil || !strings.Contains(err.Error(), a.ID) || !strings.Contains(err.Error(), b.ID) {
		t.Fatalf("ambiguous names: %v", err)
	}
	if path, err := Find(a.ID); err != nil || path != a.Path {
		t.Fatalf("exact ID: %s %v", path, err)
	}
}

func TestFindUniquePrefixAndAmbiguity(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	makeSession := func(id string) *Writer {
		w := New(t.TempDir())
		old := w.ID
		w.ID = id
		w.Path = strings.Replace(w.Path, old+".jsonl", id+".jsonl", 1)
		w.Append(Entry{Type: TypeName, Name: id})
		w.Close()
		return w
	}
	a := makeSession("abc123")
	if path, err := Find("abc"); err != nil || path != a.Path {
		t.Fatalf("prefix %s %v", path, err)
	}
	b := makeSession("abc456")
	if _, err := Find("abc"); err == nil || !strings.Contains(err.Error(), a.ID) || !strings.Contains(err.Error(), b.ID) {
		t.Fatalf("ambiguous prefix: %v", err)
	}
	if path, err := Find(a.ID); err != nil || path != a.Path {
		t.Fatalf("exact %s %v", path, err)
	}
}

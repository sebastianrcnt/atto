package outputs

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
)

// isolated points ~/.atto at a temporary directory and restores the
// package's globals afterwards.
func isolated(t *testing.T) {
	t.Helper()
	t.Setenv(config.EnvDir, t.TempDir())
	oldFree, oldNow := freeSpace, now
	freeSpace = func(string) (uint64, error) { return 1 << 40, nil }
	mu.Lock()
	lastBudget = time.Time{}
	mu.Unlock()
	t.Cleanup(func() {
		freeSpace, now = oldFree, oldNow
		SetLimits(Limits{})
	})
}

func readAll(t *testing.T, path string) []byte {
	t.Helper()
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// lines is n lines of varying width.
func lines(n int) []byte {
	var b bytes.Buffer
	for i := range n {
		fmt.Fprintf(&b, "line %d %s\n", i, strings.Repeat("x", i%37))
	}
	return b.Bytes()
}

// writeChunks writes data in chunks of the given sizes, cycling.
func writeChunks(w *Writer, data []byte, sizes ...int) {
	for i := 0; len(data) > 0; i++ {
		n := min(sizes[i%len(sizes)], len(data))
		w.Write(data[:n])
		data = data[n:]
	}
}

// want is what a file holds for data under head and tail caps.
func want(data []byte, head, tail int) []byte {
	if len(data) <= head+tail {
		return data
	}
	mid := data[head : len(data)-tail]
	marker := fmt.Sprintf("[... %d bytes (%d lines) omitted ...]\n", len(mid), bytes.Count(mid, []byte{'\n'}))
	if data[head-1] != '\n' {
		marker = "\n" + marker
	}
	return append(append(append([]byte{}, data[:head]...), marker...), data[len(data)-tail:]...)
}

func TestShortOutputWritesNothing(t *testing.T) {
	isolated(t)
	w := New(Options{Session: "s", Name: "c1", Keep: 1000})
	w.Write([]byte("hello\n"))
	got, err := w.Save()
	if err != nil || got.Path != "" {
		t.Fatalf("saved %+v, %v", got, err)
	}
	if _, err := os.Stat(Root()); err == nil {
		if ents, _ := os.ReadDir(SessionDir("s")); len(ents) != 0 {
			t.Fatalf("files for a short output: %v", ents)
		}
	}
	if s := w.Snapshot(); s.Head != "hello\n" || s.Tail != "hello\n" || s.Bytes != 6 || s.Lines != 1 {
		t.Fatalf("%+v", s)
	}
}

func TestFileIsHeadMarkerTail(t *testing.T) {
	isolated(t)
	lim := &Limits{FileHead: 1000, FileTail: 500, Total: -1, MinFree: -1}
	data := lines(2000)
	for _, n := range []int{1001, 1500, 1501, 1502, 2500, 7000, len(data)} {
		for _, sizes := range [][]int{{1}, {7, 100}, {4096}, {1 << 20}, {999, 1, 2, 3000}} {
			if n == len(data) && sizes[0] == 1 {
				continue // slow
			}
			name := fmt.Sprintf("n%d-s%d-%d", n, sizes[0], len(sizes))
			w := New(Options{Session: "s", Name: name, Keep: 100, Limits: lim})
			writeChunks(w, data[:n], sizes...)
			saved, err := w.Save()
			if err != nil || !strings.HasSuffix(saved.Path, name+".log.zst") {
				t.Fatalf("%s: %+v, %v", name, saved, err)
			}
			got := readAll(t, saved.Path)
			exp := want(data[:n], 1000, 500)
			if !bytes.Equal(got, exp) {
				t.Fatalf("%s: file differs: %d bytes, want %d\n%q\n%q", name, len(got), len(exp), got[max(0, 990):min(len(got), 1100)], exp[max(0, 990):min(len(exp), 1100)])
			}
			if wantOmit := max(0, n-1500); saved.Omitted != int64(wantOmit) {
				t.Fatalf("%s: omitted %d, want %d", name, saved.Omitted, wantOmit)
			}
		}
	}
	// Nothing but the finished files is left behind.
	ents, _ := os.ReadDir(SessionDir("s"))
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".log.zst") {
			t.Errorf("left behind: %s", e.Name())
		}
	}
}

func TestHeadEndingInNewline(t *testing.T) {
	isolated(t)
	lim := &Limits{FileHead: 4, FileTail: 4, Total: -1, MinFree: -1}
	w := New(Options{Session: "s", Name: "x", Keep: 2, Limits: lim})
	w.Write([]byte("ab\ncdefgh\nij\nkl\n"))
	saved, err := w.Save()
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("ab\ncdefgh\nij\nkl\n")
	if got := readAll(t, saved.Path); !bytes.Equal(got, want(data, 4, 4)) {
		t.Fatalf("%q", got)
	}
	w = New(Options{Session: "s", Name: "y", Keep: 2, Limits: lim})
	w.Write([]byte("abc\ndefghijk\nlmn\n"))
	saved, _ = w.Save()
	if got := string(readAll(t, saved.Path)); !strings.HasPrefix(got, "abc\n[... ") || strings.HasPrefix(got, "abc\n\n") {
		t.Fatalf("no blank line wanted: %q", got)
	}
}

func TestSnapshotNormalizesCRLF(t *testing.T) {
	isolated(t)
	text := "a\r\nb\r\r\nc\r\n\r\nlast\r"
	for _, size := range []int{1, 2, 3, 5, len(text)} {
		w := New(Options{Session: "s", Keep: 1000})
		writeChunks(w, []byte(text), size)
		s := w.Snapshot()
		exp := strings.ReplaceAll(text, "\r\n", "\n")
		if s.Head != exp || s.Tail != exp || s.Bytes != int64(len(exp)) || s.Lines != int64(strings.Count(exp, "\n")) {
			t.Fatalf("chunks of %d: %+v, want %q", size, s, exp)
		}
	}
}

func TestSnapshotKeepsStartAndEnd(t *testing.T) {
	isolated(t)
	data := lines(5000)
	w := New(Options{Session: "s", Name: "k", Keep: 300, Limits: &Limits{Total: -1, MinFree: -1}})
	writeChunks(w, data, 77, 5000, 1)
	defer w.Discard()
	s := w.Snapshot()
	if s.Head != string(data[:300]) || s.Tail != string(data[len(data)-300:]) || s.Bytes != int64(len(data)) || s.Lines != 5000 {
		t.Fatalf("snapshot differs: %d/%d", len(s.Head), len(s.Tail))
	}
	// One write larger than the memory kept.
	w = New(Options{Session: "s", Name: "k2", Keep: 300, Limits: &Limits{Total: -1, MinFree: -1}})
	w.Write(data)
	if s := w.Snapshot(); s.Head != string(data[:300]) || s.Tail != string(data[len(data)-300:]) {
		t.Fatal("big write")
	}
	w.Discard()
}

func TestDiscardRemovesEverything(t *testing.T) {
	isolated(t)
	w := New(Options{Session: "s", Name: "d", Keep: 10, Limits: &Limits{FileHead: 100, FileTail: 100, Total: -1, MinFree: -1}})
	w.Write(lines(500))
	if ents, _ := os.ReadDir(SessionDir("s")); len(ents) == 0 {
		t.Fatal("expected unfinished files")
	}
	w.Discard()
	if ents, _ := os.ReadDir(SessionDir("s")); len(ents) != 0 {
		t.Fatalf("left: %v", ents)
	}
	if got, err := w.Save(); err != nil || got.Path != "" {
		t.Fatalf("save after discard: %+v %v", got, err)
	}
	mu.Lock()
	n := len(active)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("%d files still marked active", n)
	}
}

func TestNameCollisionAndOddNames(t *testing.T) {
	isolated(t)
	lim := &Limits{Total: -1, MinFree: -1}
	var paths []string
	for _, name := range []string{"call_1", "call_1", "../../evil", "a/b|c", ""} {
		w := New(Options{Session: "../sess", Name: name, Keep: 5, Limits: lim})
		w.Write([]byte("0123456789\n"))
		s, err := w.Save()
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Dir(s.Path) != SessionDir("../sess") {
			t.Fatalf("%q escaped to %s", name, s.Path)
		}
		paths = append(paths, s.Path)
	}
	seen := map[string]bool{}
	for _, p := range paths {
		if seen[p] {
			t.Fatalf("duplicate %s", p)
		}
		seen[p] = true
	}
	if !strings.HasPrefix(SessionDir("../sess"), Root()) {
		t.Fatal(SessionDir("../sess"))
	}
}

func TestLowDiskWritesNoFile(t *testing.T) {
	isolated(t)
	var free uint64 = 100 << 20
	freeSpace = func(string) (uint64, error) { return free, nil }
	lim := &Limits{FileHead: 100, FileTail: 100, Total: -1, MinFree: 1 << 30}
	data := lines(300)
	w := New(Options{Session: "s", Name: "low", Keep: 50, Limits: lim})
	writeChunks(w, data, 64)
	if _, err := w.Save(); !errors.Is(err, ErrLowDisk) {
		t.Fatalf("err = %v", err)
	}
	if s := w.Snapshot(); s.Head != string(data[:50]) || s.Tail != string(data[len(data)-50:]) {
		t.Fatal("the start and end are still remembered")
	}
	if ents, _ := os.ReadDir(SessionDir("s")); len(ents) != 0 {
		t.Fatalf("files: %v", ents)
	}
	// Room again: a file.
	free = 2 << 30
	w = New(Options{Session: "s", Name: "ok", Keep: 50, Limits: lim})
	writeChunks(w, data, 64)
	if s, err := w.Save(); err != nil || s.Path == "" {
		t.Fatalf("%+v %v", s, err)
	}
	// A failing probe does not stop the file.
	freeSpace = func(string) (uint64, error) { return 0, errors.New("unknown") }
	w = New(Options{Session: "s", Name: "unknown", Keep: 50, Limits: lim})
	writeChunks(w, data, 64)
	if s, err := w.Save(); err != nil || s.Path == "" {
		t.Fatalf("%+v %v", s, err)
	}
	// And MinFree below zero turns the check off.
	free = 0
	freeSpace = func(string) (uint64, error) { return 0, nil }
	lim.MinFree = -1
	w = New(Options{Session: "s", Name: "off", Keep: 50, Limits: lim})
	writeChunks(w, data, 64)
	if s, err := w.Save(); err != nil || s.Path == "" {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestBudgetDeletesOldestFirst(t *testing.T) {
	isolated(t)
	base := time.Now().Add(-48 * time.Hour)
	mk := func(session, name string, size int, age time.Duration) string {
		dir := SessionDir(session)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		mt := base.Add(age)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
		return p
	}
	var files []string
	for i := range 10 {
		files = append(files, mk(fmt.Sprintf("s%d", i%3), fmt.Sprintf("f%d.log.zst", i), 100, time.Duration(i)*time.Minute))
	}
	running := mk("s0", "running.log.zst.part", 100, -time.Hour) // the oldest, but being written
	setActive(running)
	young := mk("s1", "young.log.tail", 100, 47*time.Hour) // another process's, recent
	old := mk("s1", "orphan.log.tail", 100, -2*time.Hour)  // left by a crash, 2 days old

	enforceBudget(Limits{Total: 1000}.normalized(), true) // 1300 bytes: down to 900
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	if !exists(running) || !exists(young) {
		t.Fatal("deleted a file of a running command")
	}
	if exists(old) {
		t.Fatal("an old unfinished file is evictable")
	}
	var gone int
	for i, p := range files {
		if !exists(p) {
			gone++
			if i >= gone {
				t.Fatalf("file %d deleted before an older one", i)
			}
		}
	}
	var total int64
	filepath.WalkDir(Root(), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			info, _ := d.Info()
			total += info.Size()
		}
		return nil
	})
	if total > 900 {
		t.Fatalf("%d bytes left", total)
	}
	if gone != 3 {
		t.Fatalf("%d finished files deleted, want 3 (with the orphan, 400 bytes)", gone)
	}
	clearActive(running)

	// Under the cap nothing goes, and a cap below zero is none.
	n := len(files) - gone
	enforceBudget(Limits{Total: 1 << 20}.normalized(), true)
	enforceBudget(Limits{Total: -1}.normalized(), true)
	left := 0
	for _, p := range files {
		if exists(p) {
			left++
		}
	}
	if left != n {
		t.Fatalf("%d files left, want %d", left, n)
	}
}

func TestBudgetRunsOncePerMinute(t *testing.T) {
	isolated(t)
	clock := time.Now()
	now = func() time.Time { return clock }
	dir := SessionDir("s")
	os.MkdirAll(dir, 0o700)
	p := filepath.Join(dir, "a.log.zst")
	os.WriteFile(p, make([]byte, 100), 0o600)
	lim := Limits{Total: 10}.normalized()
	enforceBudget(lim, false) // scans and deletes
	if _, err := os.Stat(p); err == nil {
		t.Fatal("not deleted")
	}
	os.WriteFile(p, make([]byte, 100), 0o600)
	clock = clock.Add(30 * time.Second)
	enforceBudget(lim, false)
	if _, err := os.Stat(p); err != nil {
		t.Fatal("scanned twice within a minute")
	}
	clock = clock.Add(31 * time.Second)
	enforceBudget(lim, false)
	if _, err := os.Stat(p); err == nil {
		t.Fatal("not scanned after a minute")
	}
}

func TestWriterStartsBudgetAndSparesItself(t *testing.T) {
	isolated(t)
	dir := SessionDir("old")
	os.MkdirAll(dir, 0o700)
	old := filepath.Join(dir, "a.log.zst")
	os.WriteFile(old, make([]byte, 5000), 0o600)
	past := time.Now().Add(-time.Hour)
	os.Chtimes(old, past, past)
	lim := &Limits{FileHead: 100, FileTail: 100, Total: 1000, MinFree: -1}
	w := New(Options{Session: "new", Name: "n", Keep: 10, Limits: lim})
	w.Write(lines(100))
	if _, err := os.Stat(old); err == nil {
		t.Fatal("the old file should have been evicted when the new one began")
	}
	saved, err := w.Save()
	if err != nil || saved.Path == "" {
		t.Fatalf("%+v %v", saved, err)
	}
}

func TestSetLimitsAndSettings(t *testing.T) {
	defer SetLimits(Limits{})
	SetLimits(Limits{})
	d := CurrentLimits()
	if d.FileHead != 32<<20 || d.FileTail != 32<<20 || d.Total != 1<<30 || d.MinFree != 1<<30 {
		t.Fatalf("%+v", d)
	}
	if FromSettings(nil) != (Limits{}) || FromSettings(&config.ToolOutputSettings{}) != (Limits{}) {
		t.Fatal("absent is zero")
	}
	l := FromSettings(&config.ToolOutputSettings{FileHeadMB: 4, FileTailMB: 8, TotalMB: 100, MinFreeMB: 50})
	if l != (Limits{FileHead: 4 << 20, FileTail: 8 << 20, Total: 100 << 20, MinFree: 50 << 20}) {
		t.Fatalf("%+v", l)
	}
	SetLimits(FromSettings(&config.ToolOutputSettings{FileHeadMB: -3, TotalMB: -1, MinFreeMB: -1}))
	if got := CurrentLimits(); got.FileHead != DefaultFileHead || got.Total >= 0 || got.MinFree >= 0 {
		t.Fatalf("%+v", got)
	}
}

func TestSettingsJSON(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	if err := os.WriteFile(config.SettingsPath(), []byte(`{"toolOutput": {"fileHeadMB": 2, "fileTailMB": 3, "totalMB": 10, "minFreeMB": 20}, "toolOutputTokenLimit": 5}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := config.LoadSettings()
	if err != nil || s.ToolOutput == nil || *s.ToolOutput != (config.ToolOutputSettings{FileHeadMB: 2, FileTailMB: 3, TotalMB: 10, MinFreeMB: 20}) || s.ToolOutputTokenLimit != 5 {
		t.Fatalf("%+v %v", s, err)
	}
	if got := FromSettings(s.ToolOutput); got != (Limits{FileHead: 2 << 20, FileTail: 3 << 20, Total: 10 << 20, MinFree: 20 << 20}) {
		t.Fatalf("%+v", got)
	}
}

func TestSessionDirsAndRemoval(t *testing.T) {
	isolated(t)
	if got := SessionDir(""); filepath.Base(got) != "_nosession" || filepath.Dir(got) != Root() {
		t.Fatal(got)
	}
	for _, id := range []string{"..", ".", "a/b", `a\b`, "../x"} {
		if d := SessionDir(id); filepath.Dir(d) != Root() {
			t.Errorf("%q: %s", id, d)
		}
	}
	w := New(Options{Session: "gone", Name: "a", Keep: 5, Limits: &Limits{Total: -1, MinFree: -1}})
	w.Write([]byte("0123456789\n"))
	saved, _ := w.Save()
	w2 := New(Options{Session: "", Name: "b", Keep: 5, Limits: &Limits{Total: -1, MinFree: -1}})
	w2.Write([]byte("0123456789\n"))
	other, _ := w2.Save()
	if err := RemoveSession("gone"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(saved.Path); err == nil {
		t.Fatal("the session's output survived")
	}
	if err := RemoveSession("gone"); err != nil {
		t.Fatalf("removing twice: %v", err)
	}
	if err := RemoveSession(""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(other.Path); err != nil {
		t.Fatal("RemoveSession(\"\") must keep the shared directory")
	}
}

func TestOpenReadsPlainAndCompressed(t *testing.T) {
	isolated(t)
	plain := filepath.Join(t.TempDir(), "atto-bash-1.log")
	os.WriteFile(plain, []byte("plain text\nsecond\n"), 0o600)
	if got := readAll(t, plain); string(got) != "plain text\nsecond\n" {
		t.Fatalf("%q", got)
	}
	short := filepath.Join(t.TempDir(), "x")
	os.WriteFile(short, []byte("ab"), 0o600)
	if got := readAll(t, short); string(got) != "ab" {
		t.Fatalf("%q", got)
	}
	if _, err := Open(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing file")
	}
}

func TestConcurrentWriters(t *testing.T) {
	isolated(t)
	lim := &Limits{FileHead: 1 << 10, FileTail: 1 << 10, Total: 1 << 20, MinFree: -1}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			w := New(Options{Session: "c", Name: fmt.Sprint("n", i), Keep: 100, Limits: lim})
			data := lines(300 + i)
			writeChunks(w, data, 100)
			s, err := w.Save()
			if err != nil || !bytes.Equal(readAll(t, s.Path), want(data, 1<<10, 1<<10)) {
				t.Errorf("writer %d: %v", i, err)
			}
		})
	}
	wg.Wait()
}

// gen is a deterministic output of fixed-width lines.
type gen struct{ lineLen int }

func (g gen) fill(buf []byte, off int64) {
	for i := 0; i < len(buf); {
		line, col := off/int64(g.lineLen), int(off%int64(g.lineLen))
		text := fmt.Sprintf("%012d %0*d", line, g.lineLen-14, line*2654435761%1000000007)
		text += "\n"
		i += copy(buf[i:], text[col:])
		off += int64(len(text) - col)
	}
}

func (g gen) hashRange(h io.Writer, from, to int64) {
	buf := make([]byte, 1<<20)
	for off := from; off < to; {
		n := min(int64(len(buf)), to-off)
		g.fill(buf[:n], off)
		h.Write(buf[:n])
		off += n
	}
}

// TestHugeOutputStaysSmall streams 200 MB through a Writer with the default
// caps: memory stays flat, and the file is exactly the first 32 MiB, a
// marker and the last 32 MiB.
func TestHugeOutputStaysSmall(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("heavy")
	}
	isolated(t)
	const total = 200_000_000
	g := gen{lineLen: 100}
	w := New(Options{Session: "huge", Name: "big", Keep: 40_000})

	runtime.GC()
	var base, ms runtime.MemStats
	runtime.ReadMemStats(&base)
	var peak uint64
	buf := make([]byte, 32<<10)
	for off, n := int64(0), 0; off < total; n++ {
		m := min(int64(len(buf)), total-off)
		g.fill(buf[:m], off)
		w.Write(buf[:m])
		off += m
		if n%1500 == 0 {
			runtime.GC() // what is live, not the test's own garbage
			runtime.ReadMemStats(&ms)
			peak = max(peak, ms.HeapAlloc)
		}
	}
	saved, err := w.Save()
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	runtime.ReadMemStats(&ms)
	peak = max(peak, ms.HeapAlloc)
	grown := int64(peak) - int64(base.HeapAlloc)
	t.Logf("200 MB written: heap grew by %.0f KiB at most; file %d KiB", float64(grown)/1024, fileSize(t, saved.Path)/1024)
	if grown > 1<<20 {
		t.Errorf("heap grew by %d bytes", grown)
	}
	if want := int64(total - 64<<20); saved.Omitted != want {
		t.Errorf("omitted %d, want %d", saved.Omitted, want)
	}

	snap := w.Snapshot()
	wantTail := make([]byte, 40_000)
	g.fill(wantTail, total-40_000)
	if snap.Bytes != total || snap.Tail != string(wantTail) || len(snap.Head) != 40_000 {
		t.Error("snapshot differs")
	}

	// The file: head, marker, tail, checked by hash.
	head, tail := int64(32<<20), int64(32<<20)
	exp := sha256.New()
	g.hashRange(exp, 0, head)
	var mid bytes.Buffer
	last := make([]byte, 1)
	g.fill(last, head-1)
	lineFeeds := countLineFeeds(g, head, total-tail)
	if last[0] != '\n' {
		mid.WriteString("\n")
	}
	fmt.Fprintf(&mid, "[... %d bytes (%d lines) omitted ...]\n", total-head-tail, lineFeeds)
	exp.Write(mid.Bytes())
	g.hashRange(exp, total-tail, total)

	r, err := Open(saved.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got := sha256.New()
	n, err := io.Copy(got, r)
	if err != nil {
		t.Fatal(err)
	}
	if n != head+tail+int64(mid.Len()) || !bytes.Equal(got.Sum(nil), exp.Sum(nil)) {
		t.Fatalf("file has %d bytes, want %d, or differs", n, head+tail+int64(mid.Len()))
	}
}

func countLineFeeds(g gen, from, to int64) int64 {
	buf := make([]byte, 1<<20)
	var n int64
	for off := from; off < to; {
		m := min(int64(len(buf)), to-off)
		g.fill(buf[:m], off)
		n += int64(bytes.Count(buf[:m], []byte{'\n'}))
		off += m
	}
	return n
}

func fileSize(t *testing.T, p string) int64 {
	t.Helper()
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

// TestRandomChunking compares files against the reference for random
// content and write sizes.
func TestRandomChunking(t *testing.T) {
	isolated(t)
	rng := rand.New(rand.NewSource(1))
	lim := &Limits{FileHead: 700, FileTail: 900, Total: -1, MinFree: -1}
	for i := range 40 {
		data := make([]byte, rng.Intn(6000))
		for j := range data {
			data[j] = "ab\n\r"[rng.Intn(4)]
		}
		w := New(Options{Session: "r", Name: fmt.Sprint("r", i), Keep: 1 + rng.Intn(400), Limits: lim})
		d := data
		for len(d) > 0 {
			n := min(1+rng.Intn(300), len(d))
			w.Write(d[:n])
			d = d[n:]
		}
		saved, err := w.Save()
		if err != nil {
			t.Fatal(err)
		}
		if saved.Path == "" {
			if len(data) > w.keep {
				t.Fatalf("%d bytes, keep %d, and no file", len(data), w.keep)
			}
			continue
		}
		if got := readAll(t, saved.Path); !bytes.Equal(got, want(data, 700, 900)) {
			t.Fatalf("case %d (%d bytes): file differs", i, len(data))
		}
	}
}

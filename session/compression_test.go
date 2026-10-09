package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/sebastianrcnt/atto/provider"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/sebastianrcnt/atto/config"
)

func compressionSession(t *testing.T, agent bool) *Writer {
	t.Helper()
	w := New("/work")
	if agent {
		w = NewAgent("/work", "parent")
	}
	w.Append(Entry{Type: TypeName, Name: "Compression test"})
	w.Append(Entry{Type: TypeMessage, Message: &provider.Message{Role: "user", Content: "find the archived needle"}})
	w.Append(Entry{Type: TypeMessage, Message: &provider.Message{Role: "assistant", Content: "  archived answer  "}})
	w.Close()
	if err := w.Err(); err != nil {
		t.Fatal(err)
	}
	return w
}

func readSessionBytes(t *testing.T, path string) []byte {
	t.Helper()
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(r)
	closeErr := r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	return b
}

func TestCompressedArchiveRoundTripAndReaders(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	w := compressionSession(t, true)
	original, err := os.ReadFile(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	active, err := ReadActive(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	context, err := ReadContext(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	dst, err := Archive(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(dst, ".jsonl.zst") {
		t.Fatal(dst)
	}
	raw, err := os.ReadFile(dst)
	if err != nil || !bytes.HasPrefix(raw, []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		t.Fatalf("frame: %x %v", raw, err)
	}
	if !bytes.Equal(readSessionBytes(t, dst), original) {
		t.Fatal("archive changed bytes")
	}
	list, err := ListAll("", true)
	if err != nil || len(list) != 1 {
		t.Fatalf("list %v %v", list, err)
	}
	s := list[0]
	if s.ID != w.ID || s.Name != "Compression test" || s.Preview != "find the archived needle" || s.LastMessage != "archived answer" || s.Messages != 2 || s.AgentOf != "parent" || !s.Archived {
		t.Fatalf("summary %+v", s)
	}
	reads := summaryReads.Load()
	if _, err := ListAll("", true); err != nil || summaryReads.Load() != reads {
		t.Fatal("unchanged compressed summary was reread", err)
	}
	for _, id := range []string{w.ID, w.ID[:6]} {
		if p, err := Find(id); err != nil || p != dst {
			t.Fatalf("find %s: %s %v", id, p, err)
		}
	}
	if got, err := ReadActive(dst); err != nil || !reflect.DeepEqual(got, active) {
		t.Fatalf("active transcript changed: %v", err)
	}
	if got, err := ReadContext(dst); err != nil || !reflect.DeepEqual(got, context) {
		t.Fatalf("context changed: %v", err)
	}
	// ReadActive is the API used by server.ReadOffline, the command center.
	if err := Rename(dst, "not writable"); err == nil {
		t.Fatal("wrote compressed archive")
	}
	restored, err := Unarchive(dst)
	if err != nil || restored != w.Path {
		t.Fatalf("restore %s %v", restored, err)
	}
	got, err := os.ReadFile(restored)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("restore bytes: %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("archive remained", err)
	}
}

func TestLegacyArchiveRestoreAndMagic(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	w := compressionSession(t, false)
	data, _ := os.ReadFile(w.Path)
	rel, _ := filepath.Rel(config.SessionsDir(), w.Path)
	plain := filepath.Join(config.ArchivedDir(), rel)
	if err := os.MkdirAll(filepath.Dir(plain), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(w.Path, plain); err != nil {
		t.Fatal(err)
	}
	if err := Rename(plain, "no"); err == nil {
		t.Fatal("wrote legacy archive")
	}
	if restored, err := Unarchive(plain); err != nil || restored != w.Path {
		t.Fatal(restored, err)
	}
	if got, _ := os.ReadFile(w.Path); !bytes.Equal(got, data) {
		t.Fatal("legacy restore changed bytes")
	}
	dst, err := Archive(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	// Magic detection also works with legacy-looking names (Open and summary).
	if err := os.Rename(dst, plain); err != nil {
		t.Fatal(err)
	}
	if got := readSessionBytes(t, plain); !bytes.Equal(got, data) {
		t.Fatal("magic detection")
	}
	if s, err := Summarize(plain); err != nil || s.Preview != "find the archived needle" {
		t.Fatal(s, err)
	}
	if restored, err := Unarchive(plain); err != nil || restored != w.Path {
		t.Fatal(restored, err)
	}
	if got, _ := os.ReadFile(w.Path); !bytes.Equal(got, data) {
		t.Fatal("magic restore changed bytes")
	}
}

func TestArchiveCrashCheckpoints(t *testing.T) {
	for _, point := range []string{"written", "renamed"} {
		t.Run(point, func(t *testing.T) {
			t.Setenv("ATTO_DIR", t.TempDir())
			w := compressionSession(t, false)
			data, _ := os.ReadFile(w.Path)
			rel, _ := filepath.Rel(config.SessionsDir(), w.Path)
			dst := filepath.Join(config.ArchivedDir(), rel) + ".zst"
			failure := errors.New("simulated crash")
			_, err := transferSession(w.Path, dst, true, func(p string) error {
				if p == point {
					return failure
				}
				return nil
			})
			if !errors.Is(err, failure) {
				t.Fatal(err)
			}
			if got, _ := os.ReadFile(w.Path); !bytes.Equal(got, data) {
				t.Fatal("lost source")
			}
			if point == "renamed" && !bytes.Equal(readSessionBytes(t, dst), data) {
				t.Fatal("invalid destination")
			}
			if live, _ := ListAll("", false); len(live) != 1 {
				t.Fatal("lost live listing", live)
			}
			if archived, _ := ListAll("", true); len(archived) != 0 {
				t.Fatal("duplicate listing", archived)
			}
			if got, err := Find(w.ID[:6]); err != nil || got != w.Path {
				t.Fatal("live prefix must win", got, err)
			}
			if _, err := Archive(w.Path); err != nil {
				t.Fatal("retry", err)
			}
			if got := readSessionBytes(t, dst); !bytes.Equal(got, data) {
				t.Fatal("retry changed bytes")
			}
		})
	}
}

func TestUnarchiveCrashCheckpoints(t *testing.T) {
	for _, point := range []string{"written", "renamed"} {
		t.Run(point, func(t *testing.T) {
			t.Setenv("ATTO_DIR", t.TempDir())
			w := compressionSession(t, false)
			data, _ := os.ReadFile(w.Path)
			dst, err := Archive(w.Path)
			if err != nil {
				t.Fatal(err)
			}
			failure := errors.New("simulated crash")
			_, err = transferSession(dst, w.Path, false, func(p string) error {
				if p == point {
					return failure
				}
				return nil
			})
			if !errors.Is(err, failure) {
				t.Fatal(err)
			}
			if !bytes.Equal(readSessionBytes(t, dst), data) {
				t.Fatal("lost source")
			}
			if point == "written" {
				if _, err := Unarchive(dst); err != nil {
					t.Fatal("restore retry", err)
				}
			} else {
				got, _ := os.ReadFile(w.Path)
				if !bytes.Equal(got, data) {
					t.Fatal("invalid restored destination")
				}
				if archived, _ := ListAll("", true); len(archived) != 0 {
					t.Fatal("duplicate archived listing", archived)
				}
				if p, err := Find(w.ID); err != nil || p != w.Path {
					t.Fatal("live must win", p, err)
				}
				// The live transcript might have been resumed; never overwrite it.
				if _, err := Unarchive(dst); err == nil {
					t.Fatal("overwrote restored live file")
				}
				if err := RemoveCopies(w.Path); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(dst); !os.IsNotExist(err) {
					t.Fatal("stale copy remains", err)
				}
			}
		})
	}
}

func TestCompressArchivesMigration(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	var originals = map[string][]byte{}
	var before int64
	for range 3 {
		w := compressionSession(t, false)
		data, _ := os.ReadFile(w.Path)
		rel, _ := filepath.Rel(config.SessionsDir(), w.Path)
		plain := filepath.Join(config.ArchivedDir(), rel)
		if err := os.MkdirAll(filepath.Dir(plain), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(w.Path, plain); err != nil {
			t.Fatal(err)
		}
		originals[plain+".zst"] = data
		before += int64(len(data))
	}
	stats, err := CompressArchives()
	if err != nil || stats.Count != 3 || stats.Before != before || stats.After <= 0 {
		t.Fatal(stats, err)
	}
	for path, want := range originals {
		if !bytes.Equal(readSessionBytes(t, path), want) {
			t.Fatal("migration bytes", path)
		}
		if _, err := os.Stat(strings.TrimSuffix(path, ".zst")); !os.IsNotExist(err) {
			t.Fatal("plain remained", err)
		}
	}
	if stats, err := CompressArchives(); err != nil || stats != (CompressionStats{}) {
		t.Fatal("not idempotent", stats, err)
	}
	if list, err := ListAll("", true); err != nil || len(list) != 3 {
		t.Fatal(list, err)
	}
}

func BenchmarkSessionCompression(b *testing.B) {
	// A coding transcript: changing source snippets, commands, and compiler
	// output wrapped in JSONL, rather than a single repeated lorem ipsum line.
	var data bytes.Buffer
	files, _ := filepath.Glob("*.go")
	for round := range 8 {
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			source, _ := os.ReadFile(path)
			line := Entry{Type: TypeMessage, ID: strings.Repeat("a", round+1) + path, Message: &provider.Message{Role: "tool", Content: "$ sed -n '1,200p' " + path + "\n" + string(source)}}
			raw, _ := json.Marshal(line)
			data.Write(raw)
			data.WriteByte('\n')
		}
	}
	raw := data.Bytes()
	if path := os.Getenv("ATTO_BENCHMARK_SESSION"); path != "" {
		var err error
		raw, err = os.ReadFile(path)
		if err != nil {
			b.Fatal(err)
		}
	}
	for _, level := range []zstd.EncoderLevel{zstd.SpeedDefault, zstd.SpeedBetterCompression} {
		b.Run(level.String(), func(b *testing.B) {
			w, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(level), zstd.WithEncoderConcurrency(1))
			if err != nil {
				b.Fatal(err)
			}
			defer w.Close()
			b.SetBytes(int64(len(raw)))
			b.ResetTimer()
			var size int
			for range b.N {
				var dst bytes.Buffer
				w.Reset(&dst)
				if _, err := io.Copy(w, bytes.NewReader(raw)); err != nil {
					b.Fatal(err)
				}
				if err := w.Close(); err != nil {
					b.Fatal(err)
				}
				size = dst.Len()
			}
			b.ReportMetric(float64(len(raw))/float64(size), "ratio")
			b.ReportMetric(float64(len(raw)), "input-bytes")
			b.ReportMetric(float64(size), "output-bytes")
		})
	}
}

func TestCompressedSummaryCacheAndBranches(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	resetSummaryCache()
	defer resetSummaryCache()
	w := New("/work")
	w.Append(smsg("user", "first question"))
	w.Append(smsg("assistant", "first answer"))
	leaf := w.Leaf()
	w.Append(smsg("user", "abandoned question"))
	w.Append(smsg("assistant", "abandoned answer"))
	w.SetLeaf(leaf)
	w.Append(smsg("user", "active question"))
	w.Append(smsg("assistant", "  active answer  "))
	w.Append(Entry{Type: TypeName, Name: "late title"})
	w.Close()
	dst, err := Archive(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	want := referenceSummary(t, dst)
	got, err := Summarize(dst)
	if err != nil || got != want {
		t.Fatalf("compressed summary\ngot %+v\nwant %+v\n%v", got, want, err)
	}
	saveDiskSummaries(true)
	resetSummaryCache()
	reads := summaryReads.Load()
	if got, err := Summarize(dst); err != nil || got != want || summaryReads.Load() != reads {
		t.Fatal("disk cache reread compressed stream", got, err)
	}
	// Replacing the archive changes its compressed size/mtime and invalidates
	// the immutable cached summary; no append offsets from compressed bytes.
	restored, err := Unarchive(dst)
	if err != nil {
		t.Fatal(err)
	}
	h, _, _ := Load(restored)
	r := Resume(restored, h)
	r.Append(Entry{Type: TypeName, Name: "a different late title"})
	r.Close()
	if _, err := Archive(restored); err != nil {
		t.Fatal(err)
	}
	if got, err := Summarize(dst); err != nil || got.Name != "a different late title" {
		t.Fatal("stale compressed cache", got, err)
	}
}

func TestCompressedHiddenAgentOnlyReadsHeader(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	w := compressionSession(t, true)
	h, _, _ := Load(w.Path)
	r := Resume(w.Path, h)
	// Force multiple blocks so the corrupted tail is not needed for a header.
	r.Append(smsg("tool", strings.Repeat("long tool output with interesting tokens 123456789\n", 200000)))
	r.Close()
	dst, err := Archive(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(dst)
	if err := os.WriteFile(dst, data[:len(data)-8], 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := listSummaryMode(dst, false); err != nil || s.AgentOf != "parent" {
		t.Fatal("read beyond hidden agent header", s, err)
	}
	if _, err := listSummaryMode(dst, true); err == nil {
		t.Fatal("corrupt full archive accepted")
	}
	if _, err := Unarchive(dst); err == nil {
		t.Fatal("restored corrupt archive")
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatal("failed restore removed source", err)
	}
	if _, err := os.Stat(w.Path); !os.IsNotExist(err) {
		t.Fatal("published partial restore", err)
	}
}

func TestCompressArchivesHonorsLease(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	w := compressionSession(t, false)
	rel, _ := filepath.Rel(config.SessionsDir(), w.Path)
	plain := filepath.Join(config.ArchivedDir(), rel)
	if err := os.MkdirAll(filepath.Dir(plain), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(w.Path, plain); err != nil {
		t.Fatal(err)
	}
	release, err := Lock(plain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CompressArchives(); !errors.Is(err, ErrLocked) {
		t.Fatal("compressed leased archive", err)
	}
	release()
	if stats, err := CompressArchives(); err != nil || stats.Count != 1 {
		t.Fatal(stats, err)
	}
}

func TestArchiveRestoreWithOpenReaders(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	w := compressionSession(t, false)
	original, _ := os.ReadFile(w.Path)
	reader, err := Open(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	dst, err := Archive(w.Path)
	if err != nil {
		t.Fatal("archive with reader handle", err)
	}
	archivedReader, err := Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer archivedReader.Close()
	if _, err := Unarchive(dst); err != nil {
		t.Fatal("restore with reader handle", err)
	}
	if got, err := io.ReadAll(reader); err != nil || !bytes.Equal(got, original) {
		t.Fatal("live reader interrupted", err)
	}
	if got, err := io.ReadAll(archivedReader); err != nil || !bytes.Equal(got, original) {
		t.Fatal("archive reader interrupted", err)
	}
}

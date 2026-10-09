package maint

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/session"
)

func put(t *testing.T, p string, b []byte) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(p), 0o700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, b, 0o600); e != nil {
		t.Fatal(e)
	}
}
func fixture(t *testing.T) string {
	root := t.TempDir()
	t.Setenv(config.EnvDir, root)
	for p, b := range map[string]string{"sessions/2026/10/09/abcdef12.jsonl": "{\"type\":\"session\",\"version\":1,\"id\":\"abcdef12\"}\n{\"type\":\"message\",\"role\":\"user\",\"content\":\"hi\"}\n", "archived_sessions/old.jsonl": "legacy\n", "images/pic.png": "\x00\xffimage", "agent-state/parent/a.json": "{\"name\":\"a\",\"parent\":\"parent\",\"session\":\"child\"}", "agent-state/_up/child.json": "{\"parent\":\"parent\",\"name\":\"a\"}", "agent-state/_closed/old.json": "{\"session\":\"old\"}", "settings.json": "{\"model\":\"fake/m\"}", "settings.json.bak": "{}", "models.json": "[]", "auth.json": "secret", "auth.json.bak": "secret backup", "server-token": "token", "cache/catalog.json": "cache", "run/socket": "runtime", "logs/debug.log": "log", "debug/dump": "dump", "worktrees/parent/a/foo": "checkout", "settings.json.lock": "lock", "update-check.json": "update", "outputs/abcdef12/log.zst": "output", "bin/helper": "helper", "skills/custom/SKILL.md": "skill", "extensions/app.ts": "extension", "mcp-approvals.json": "{}"} {
		put(t, filepath.Join(root, filepath.FromSlash(p)), []byte(b))
	}
	enc, _ := zstd.NewWriter(nil)
	put(t, filepath.Join(root, "archived_sessions", "compressed.jsonl.zst"), enc.EncodeAll([]byte("compressed archive"), nil))
	enc.Close()
	return root
}
func TestBackupRestoreRoundTrip(t *testing.T) {
	for _, secrets := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "secrets"}[secrets], func(t *testing.T) {
			root := fixture(t)
			archive := filepath.Join(t.TempDir(), "backup.tar.zst")
			var out bytes.Buffer
			m, e := Backup(BackupOptions{Root: root, Output: archive, Version: "test-version", WithSecrets: secrets, IncludeCache: secrets, Out: &out})
			if e != nil {
				t.Fatal(e)
			}
			if m.Version != "test-version" || m.Formats != supported() || m.Created.IsZero() || m.Hostname == "" || m.OS != runtime.GOOS || m.Source != root || m.Files < 10 || m.Bytes < 100 {
				t.Fatalf("manifest: %+v", m)
			}
			if secrets && !strings.Contains(out.String(), "WARNING") {
				t.Fatal("missing warning")
			}
			info, _ := os.Stat(archive)
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
				t.Fatal(info.Mode())
			}
			dest := filepath.Join(t.TempDir(), "restored")
			restored, e := Restore(archive, RestoreOptions{Root: dest, Out: &out})
			if e != nil {
				t.Fatal(e)
			}
			if restored.Files != m.Files {
				t.Fatal("counts")
			}
			_ = filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
				if e != nil || d.IsDir() {
					return e
				}
				rel, _ := filepath.Rel(root, p)
				rel = filepath.ToSlash(rel)
				if excluded(rel, BackupOptions{WithSecrets: secrets, IncludeCache: secrets}) {
					if _, e := os.Stat(filepath.Join(dest, filepath.FromSlash(rel))); !os.IsNotExist(e) {
						t.Errorf("excluded %s restored", rel)
					}
					return nil
				}
				a, e := os.ReadFile(p)
				if e != nil {
					t.Fatal(e)
				}
				b, e := os.ReadFile(filepath.Join(dest, filepath.FromSlash(rel)))
				if e != nil || !bytes.Equal(a, b) {
					t.Errorf("%s differs: %v", rel, e)
				}
				return nil
			})
			if !strings.Contains(out.String(), "atto sessions compress") {
				t.Fatal("no compression suggestion")
			}
			if _, e = Backup(BackupOptions{Root: dest, Output: filepath.Join(t.TempDir(), "again.tar.zst")}); e != nil {
				t.Fatal("backing up restored dir:", e)
			}
		})
	}
}
func makeArchive(t *testing.T, m Manifest, headers []*tar.Header, bodies [][]byte) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "crafted.tar.zst")
	f, e := os.Create(file)
	if e != nil {
		t.Fatal(e)
	}
	z, _ := zstd.NewWriter(f)
	tw := tar.NewWriter(z)
	b, _ := json.Marshal(m)
	tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o600, Size: int64(len(b)), Typeflag: tar.TypeReg})
	tw.Write(b)
	for i, h := range headers {
		if e = tw.WriteHeader(h); e != nil {
			t.Fatal(e)
		}
		if i < len(bodies) {
			tw.Write(bodies[i])
		}
	}
	tw.Close()
	z.Close()
	f.Close()
	return file
}
func simpleManifest() Manifest {
	return Manifest{Version: "test", Formats: supported(), Created: time.Now(), Files: 1, Bytes: 1}
}
func TestRestoreRejectsNewerFormats(t *testing.T) {
	for _, field := range []string{"archive", "session", "agent", "daemon"} {
		m := simpleManifest()
		switch field {
		case "archive":
			m.Formats.Archive++
		case "session":
			m.Formats.Session++
		case "agent":
			m.Formats.AgentState++
		case "daemon":
			m.Formats.Daemon++
		}
		file := makeArchive(t, m, nil, nil)
		_, e := Restore(file, RestoreOptions{Root: filepath.Join(t.TempDir(), "data")})
		if e == nil || !strings.Contains(e.Error(), "newer data formats") {
			t.Fatal(field, e)
		}
	}
}
func TestRestoreRejectsHostilePaths(t *testing.T) {
	for _, name := range []string{"../outside", "/absolute", "C:/absolute", "a\\..\\outside", "a/../outside", "./x"} {
		t.Run(name, func(t *testing.T) {
			file := makeArchive(t, simpleManifest(), []*tar.Header{{Name: name, Typeflag: tar.TypeReg, Mode: 0o600, Size: 1}}, [][]byte{[]byte("x")})
			dest := filepath.Join(t.TempDir(), "data")
			_, e := Restore(file, RestoreOptions{Root: dest})
			if e == nil {
				t.Fatal("accepted", name)
			}
			if _, e = os.Stat(dest); !os.IsNotExist(e) {
				t.Fatal("invalid restore committed")
			}
		})
	}
	for _, target := range []string{"../../outside", "/outside", "C:\\outside", "..\\outside"} {
		file := makeArchive(t, simpleManifest(), []*tar.Header{{Name: "link", Typeflag: tar.TypeSymlink, Linkname: target}}, nil)
		if _, e := Restore(file, RestoreOptions{Root: filepath.Join(t.TempDir(), "data")}); e == nil {
			t.Fatal("accepted symlink", target)
		}
	}
	m := simpleManifest()
	m.Files = 2
	m.Bytes = 1
	file := makeArchive(t, m, []*tar.Header{{Name: "dir", Typeflag: tar.TypeSymlink, Linkname: "safe"}, {Name: "dir/file", Typeflag: tar.TypeReg, Mode: 0o600, Size: 1}}, [][]byte{nil, []byte("x")})
	if _, e := Restore(file, RestoreOptions{Root: filepath.Join(t.TempDir(), "data")}); e == nil {
		t.Fatal("accepted symlink traversal")
	}
}
func TestRestoreNonEmptyAndForce(t *testing.T) {
	root := fixture(t)
	file := filepath.Join(t.TempDir(), "backup.tar.zst")
	if _, e := Backup(BackupOptions{Root: root, Output: file}); e != nil {
		t.Fatal(e)
	}
	dest := filepath.Join(t.TempDir(), "data")
	put(t, filepath.Join(dest, "precious"), []byte("keep"))
	if _, e := Restore(file, RestoreOptions{Root: dest}); e == nil {
		t.Fatal("overwrote nonempty root")
	}
	if _, e := Restore(file, RestoreOptions{Root: dest, Force: true}); e != nil {
		t.Fatal(e)
	}
	old, _ := filepath.Glob(dest + ".before-restore-*")
	if len(old) != 1 {
		t.Fatal(old)
	}
	if b, e := os.ReadFile(filepath.Join(old[0], "precious")); e != nil || string(b) != "keep" {
		t.Fatal("old data lost")
	}
}
func TestBackupRefusesLockAndAllowsForce(t *testing.T) {
	root := fixture(t)
	p := filepath.Join(root, "sessions/2026/10/09/abcdef12.jsonl")
	release, e := session.Lock(p)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	if _, e = Backup(BackupOptions{Root: root, Output: filepath.Join(t.TempDir(), "refuse.tar.zst")}); e == nil {
		t.Fatal("active data accepted")
	}
	var out bytes.Buffer
	if _, e = Backup(BackupOptions{Root: root, Output: filepath.Join(t.TempDir(), "force.tar.zst"), Force: true, Out: &out}); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(out.String(), "WARNING") {
		t.Fatal(out.String())
	}
}
func TestBackupOutputInsideRoot(t *testing.T) {
	root := fixture(t)
	if _, e := Backup(BackupOptions{Root: root, Output: filepath.Join(root, "bad.tar.zst")}); e == nil {
		t.Fatal("accepted internal backup")
	}
}

func TestRestoreRejectsHardlinkDuplicateAndCountMismatch(t *testing.T) {
	cases := []struct {
		name     string
		headers  []*tar.Header
		bodies   [][]byte
		manifest Manifest
	}{
		{"hardlink", []*tar.Header{{Name: "hard", Typeflag: tar.TypeLink, Linkname: "../outside"}}, nil, simpleManifest()},
		{"duplicate", []*tar.Header{{Name: "file", Typeflag: tar.TypeReg, Size: 1}, {Name: "file", Typeflag: tar.TypeReg, Size: 1}}, [][]byte{[]byte("a"), []byte("b")}, simpleManifest()},
		{"count-mismatch", []*tar.Header{{Name: "file", Typeflag: tar.TypeReg, Size: 2}}, [][]byte{[]byte("ab")}, simpleManifest()},
		{"special-file", []*tar.Header{{Name: "pipe", Typeflag: tar.TypeFifo}}, nil, simpleManifest()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			file := makeArchive(t, tc.manifest, tc.headers, tc.bodies)
			root := filepath.Join(t.TempDir(), "data")
			if _, e := Restore(file, RestoreOptions{Root: root}); e == nil {
				t.Fatal("accepted hostile archive")
			}
			if _, e := os.Stat(root); !os.IsNotExist(e) {
				t.Fatal("invalid restore committed")
			}
		})
	}
}

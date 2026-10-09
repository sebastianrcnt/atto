// Package maint implements portable, conservative maintenance of atto's data.
package maint

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/daemon"
	"github.com/sebastianrcnt/atto/session"
)

const ArchiveVersion = 1

// AgentLayoutVersion is the layout of agent-state: 2 is one file per agent
// session ID (agentstate.FormatVersion); 1 was one directory per parent.
const AgentLayoutVersion = agentstate.FormatVersion

type Formats struct {
	Archive    int `json:"archive"`
	Session    int `json:"session_header"`
	AgentState int `json:"agent_state_layout"`
	Daemon     int `json:"daemon_protocol"`
}
type Manifest struct {
	Version     string     `json:"atto_version"`
	Formats     Formats    `json:"formats"`
	Created     time.Time  `json:"created"`
	OS          string     `json:"os"`
	Arch        string     `json:"arch"`
	Hostname    string     `json:"hostname"`
	Source      string     `json:"source_atto_dir"`
	Files       int        `json:"file_count"`
	Bytes       int64      `json:"bytes"`
	Excluded    []string   `json:"excluded"`
	Worktrees   []Worktree `json:"worktrees"`
	WithSecrets bool       `json:"with_secrets"`
}

func supported() Formats {
	return Formats{ArchiveVersion, session.Version, AgentLayoutVersion, daemon.Proto}
}
func (m Manifest) Validate() error {
	s := supported()
	if m.Formats.Archive < 1 || m.Formats.Session < 1 || m.Formats.AgentState < 1 || m.Formats.Daemon < 1 {
		return errors.New("backup manifest has missing or invalid format versions")
	}
	if m.Formats.Archive > s.Archive || m.Formats.Session > s.Session || m.Formats.AgentState > s.AgentState || m.Formats.Daemon > s.Daemon {
		return fmt.Errorf("backup uses newer data formats %+v; this atto understands %+v: upgrade atto before restoring", m.Formats, s)
	}
	if m.Files < 0 || m.Bytes < 0 || m.Created.IsZero() {
		return errors.New("invalid backup manifest counts or creation time")
	}
	return nil
}

type BackupOptions struct {
	Root, Output, Version            string
	WithSecrets, IncludeCache, Force bool
	Out                              io.Writer
}

func secret(name string) bool {
	b := path.Base(name)
	return b == "server-token" || b == "auth.json" || strings.HasPrefix(b, "auth.json.")
}
func excluded(name string, o BackupOptions) bool {
	top, _, _ := strings.Cut(name, "/")
	switch top {
	case "run", "debug", "logs", "worktrees", "backups":
		return true
	case "cache":
		return !o.IncludeCache
	}
	return strings.HasSuffix(name, ".lock") || (name == "update-check.json" || name == "restore-manifest.json") || (!o.WithSecrets && secret(name))
}
func writer(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

// BackupsDir is where backups go that atto takes itself (before migrating
// agent data), under ATTO_DIR. Archives never contain it.
const BackupsDir = "backups"

func DefaultBackupName(now time.Time) string {
	h, _ := os.Hostname()
	h = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.' {
			return r
		}
		return '-'
	}, h)
	if h == "" {
		h = "host"
	}
	return "atto-backup-" + h + "-" + now.Format("20060102-1504") + ".tar.zst"
}

// Backup writes one streaming archive; metadata is inventoried before copying.
func Backup(o BackupOptions) (m Manifest, err error) {
	if o.Root == "" {
		o.Root = config.Dir()
	}
	o.Root, err = filepath.Abs(o.Root)
	if err != nil {
		return m, err
	}
	if o.Output == "" {
		o.Output = DefaultBackupName(time.Now())
	}
	output, e := filepath.Abs(o.Output)
	if e != nil {
		return m, e
	}
	if within(o.Root, output) && !within(filepath.Join(o.Root, BackupsDir), output) {
		return m, errors.New("backup output must be outside ATTO_DIR (or in its backups/ directory)")
	}
	active, e := Activity(o.Root)
	if e != nil {
		return m, e
	}
	if len(active) > 0 {
		if !o.Force {
			return m, fmt.Errorf("atto data is in use (%s); stop workers/jobs first, or use -force", strings.Join(active, ", "))
		}
		fmt.Fprintln(writer(o.Out), "WARNING: forced backup of active data may be inconsistent:", strings.Join(active, ", "))
	}
	if o.WithSecrets {
		fmt.Fprintln(writer(o.Out), "WARNING: backup includes credentials and server-token. Keep it private; anyone with it may access your accounts.")
	}
	m = Manifest{Version: o.Version, Formats: supported(), Created: time.Now().UTC(), OS: runtime.GOOS, Arch: runtime.GOARCH, Source: o.Root, WithSecrets: o.WithSecrets}
	m.Hostname, _ = os.Hostname()
	if _, e := os.Stat(filepath.Join(o.Root, "agent-state", ".format")); e != nil {
		m.Formats.AgentState = 1 // the data still has the layout of one directory per parent
	}
	m.Excluded = []string{"run/", "debug/", "logs/", "backups/", "*.lock", "update-check.json", "worktrees/ (records only)"}
	if !o.IncludeCache {
		m.Excluded = append(m.Excluded, "cache/")
	}
	if !o.WithSecrets {
		m.Excluded = append(m.Excluded, "auth.json (including backups)", "server-token")
	}
	m.Worktrees, e = Worktrees(o.Root)
	if e != nil {
		return m, e
	}
	var names []string
	err = filepath.WalkDir(o.Root, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if p == o.Root {
			return nil
		}
		rel, _ := filepath.Rel(o.Root, p)
		rel = filepath.ToSlash(rel)
		if excluded(rel, o) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == "manifest.json" {
			return errors.New("ATTO_DIR contains reserved manifest.json")
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() && !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, e := os.Readlink(p)
			if e != nil {
				return e
			}
			if !safeLink(rel, link) {
				return fmt.Errorf("unsafe symlink %s -> %s", rel, link)
			}
		}
		names = append(names, rel)
		m.Files++
		if info.Mode().IsRegular() {
			m.Bytes += info.Size()
		}
		return nil
	})
	if err != nil {
		return m, err
	}
	f, e := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if e != nil {
		return m, e
	}
	success := false
	defer func() {
		f.Close()
		if !success {
			os.Remove(output)
		}
	}()
	z, e := zstd.NewWriter(f, zstd.WithEncoderConcurrency(1))
	if e != nil {
		return m, e
	}
	defer z.Close()
	t := tar.NewWriter(z)
	defer t.Close()
	b, _ := json.MarshalIndent(m, "", "  ")
	if e = t.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o600, Size: int64(len(b)), Typeflag: tar.TypeReg}); e != nil {
		return m, e
	}
	if _, e = t.Write(b); e != nil {
		return m, e
	}
	for _, name := range names {
		p := filepath.Join(o.Root, filepath.FromSlash(name))
		var release func()
		if session.IsSessionFile(name) {
			release, e = session.Lock(p)
			if e != nil {
				if !o.Force {
					return m, e
				}
				fmt.Fprintf(writer(o.Out), "WARNING: copying unlocked session %s: %v\n", name, e)
			}
		}
		e = copyEntry(t, p, name)
		if release != nil {
			release()
		}
		if e != nil {
			return m, e
		}
	}
	if e = t.Close(); e != nil {
		return m, e
	}
	if e = z.Close(); e != nil {
		return m, e
	}
	if e = f.Close(); e != nil {
		return m, e
	}
	success = true
	st, _ := os.Stat(output)
	fmt.Fprintf(writer(o.Out), "Backup: %s (%d bytes; %d entries, %d source bytes)\nExcluded: %s\n", output, st.Size(), m.Files, m.Bytes, strings.Join(m.Excluded, ", "))
	return m, nil
}
func copyEntry(t *tar.Writer, p, name string) error {
	info, e := os.Lstat(p)
	if e != nil {
		return e
	}
	link := ""
	if info.Mode()&os.ModeSymlink != 0 {
		link, e = os.Readlink(p)
		if e != nil {
			return e
		}
	}
	h, e := tar.FileInfoHeader(info, link)
	if e != nil {
		return e
	}
	h.Name = name
	if secret(name) {
		h.Mode = 0o600
	}
	if e = t.WriteHeader(h); e != nil {
		return e
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	f, e := os.Open(p)
	if e != nil {
		return e
	}
	defer f.Close()
	_, e = io.CopyN(t, f, info.Size())
	return e
}
func within(root, p string) bool {
	rel, e := filepath.Rel(canonical(root), canonical(p))
	return e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Reject Windows spellings even when reading on Unix, for portable archives.
func safeName(n string) bool {
	return n != "" && n != "." && !strings.Contains(n, "\\") && !strings.Contains(n, ":") && !strings.HasPrefix(n, "/") && path.Clean(n) == strings.TrimSuffix(n, "/") && n != ".." && !strings.HasPrefix(n, "../")
}
func safeLink(name, link string) bool {
	return link != "" && !strings.ContainsAny(link, "\\:") && !path.IsAbs(link) && safeName(path.Clean(path.Join(path.Dir(name), link)))
}

type RestoreOptions struct {
	Root             string
	Force, Worktrees bool
	Out              io.Writer
}

// Restore validates and extracts into a fresh sibling, then commits by rename.
func Restore(file string, o RestoreOptions) (m Manifest, err error) {
	if o.Root == "" {
		o.Root = config.Dir()
	}
	o.Root, err = filepath.Abs(o.Root)
	if err != nil {
		return m, err
	}
	if e := safeRoot(o.Root); e != nil {
		return m, e
	}
	if file == "" && o.Worktrees {
		b, e := os.ReadFile(filepath.Join(o.Root, "restore-manifest.json"))
		if e != nil {
			return m, e
		}
		if e = json.Unmarshal(b, &m); e != nil {
			return m, e
		}
		if e = m.Validate(); e != nil {
			return m, e
		}
		return m, RecreateWorktrees(o.Root, m.Worktrees, writer(o.Out))
	}
	f, e := os.Open(file)
	if e != nil {
		return m, e
	}
	defer f.Close()
	z, e := zstd.NewReader(f, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(256<<20))
	if e != nil {
		return m, e
	}
	defer z.Close()
	t := tar.NewReader(z)
	h, e := t.Next()
	if e != nil {
		return m, e
	}
	if h.Name != "manifest.json" || h.Typeflag != tar.TypeReg || h.Size > 4<<20 {
		return m, errors.New("manifest.json must be the first regular entry (at most 4 MiB)")
	}
	if e = json.NewDecoder(io.LimitReader(t, 4<<20)).Decode(&m); e != nil {
		return m, e
	}
	if e = m.Validate(); e != nil {
		return m, e
	}
	if active, e := Activity(o.Root); e != nil {
		return m, e
	} else if len(active) > 0 {
		return m, fmt.Errorf("restore destination is in use: %s", strings.Join(active, ", "))
	}
	entries, e := os.ReadDir(o.Root)
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return m, e
	}
	if len(entries) > 0 && !o.Force {
		return m, errors.New("ATTO_DIR is non-empty; use -force to preserve it under .before-restore-<time> first")
	}
	if e = os.MkdirAll(filepath.Dir(o.Root), 0o700); e != nil {
		return m, e
	}
	stage, e := os.MkdirTemp(filepath.Dir(o.Root), ".atto-restore-")
	if e != nil {
		return m, e
	}
	defer removeTree(stage)
	seen := map[string]bool{}
	count := 0
	var bytes int64
	var dirs []*tar.Header
	for {
		h, e = t.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return m, e
		}
		n := strings.TrimSuffix(h.Name, "/")
		if !safeName(h.Name) || n == "manifest.json" || n == "restore-manifest.json" || seen[n] {
			return m, fmt.Errorf("unsafe or duplicate archive path %q", h.Name)
		}
		seen[n] = true
		p := filepath.Join(stage, filepath.FromSlash(n))
		if !within(stage, p) {
			return m, errors.New("archive path escapes destination")
		}
		for parent := filepath.Dir(p); parent != stage; parent = filepath.Dir(parent) {
			if st, e := os.Lstat(parent); e == nil && st.Mode()&os.ModeSymlink != 0 {
				return m, fmt.Errorf("archive entry traverses symlink: %s", n)
			}
		}
		if e = os.MkdirAll(filepath.Dir(p), 0o700); e != nil {
			return m, e
		}
		mode := os.FileMode(h.Mode) & 0o777
		switch h.Typeflag {
		case tar.TypeDir:
			if e = os.MkdirAll(p, 0o700); e == nil {
				cp := *h
				dirs = append(dirs, &cp)
			}
		case tar.TypeReg, tar.TypeRegA:
			var out *os.File
			if secret(n) {
				mode = 0o600
			}
			out, e = os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if e == nil {
				_, e = io.CopyN(out, t, h.Size)
				if ce := out.Close(); e == nil {
					e = ce
				}
				if e == nil {
					e = os.Chmod(p, mode)
				}
				if e == nil {
					e = os.Chtimes(p, h.ModTime, h.ModTime)
				}
			}
			bytes += h.Size
		case tar.TypeSymlink:
			if !safeLink(n, h.Linkname) {
				return m, fmt.Errorf("unsafe symlink %s -> %s", n, h.Linkname)
			}
			e = os.Symlink(filepath.FromSlash(h.Linkname), p)
		default:
			return m, fmt.Errorf("unsupported archive entry type %d for %s", h.Typeflag, n)
		}
		if e != nil {
			return m, e
		}
		count++
	}
	if _, e = io.Copy(io.Discard, z); e != nil {
		return m, fmt.Errorf("invalid compressed archive trailer: %w", e)
	}
	if count != m.Files || bytes != m.Bytes {
		return m, fmt.Errorf("manifest count mismatch: got %d entries/%d bytes, expected %d/%d", count, bytes, m.Files, m.Bytes)
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	if e = os.WriteFile(filepath.Join(stage, "restore-manifest.json"), b, 0o600); e != nil {
		return m, e
	}
	// Migration is the existing lazy layout hook, triggered only after commit.
	for _, h := range slices.Backward(dirs) {

		if e = os.Chmod(filepath.Join(stage, filepath.FromSlash(h.Name)), os.FileMode(h.Mode)&0o777); e != nil {
			return m, e
		}
	}
	previous := ""
	if _, e = os.Lstat(o.Root); e == nil {
		if len(entries) > 0 {
			previous = o.Root + ".before-restore-" + time.Now().Format("20060102-150405.000000000")
			if e = os.Rename(o.Root, previous); e != nil {
				return m, e
			}
		} else if e = os.Remove(o.Root); e != nil {
			return m, e
		}
	}
	if e = os.Rename(stage, o.Root); e != nil {
		if previous != "" {
			_ = os.Rename(previous, o.Root)
		}
		return m, e
	}
	fmt.Fprintf(writer(o.Out), "Restored %d entries (%d bytes) to %s\n", count, bytes, o.Root)
	if previous != "" {
		fmt.Fprintln(writer(o.Out), "Previous data preserved:", previous)
	}
	fmt.Fprintln(writer(o.Out), "Legacy archives are unchanged; optionally run atto sessions compress.")
	if o.Worktrees {
		err = RecreateWorktrees(o.Root, m.Worktrees, writer(o.Out))
	} else if len(m.Worktrees) > 0 {
		fmt.Fprintf(writer(o.Out), "%d worktrees recorded; use atto restore -worktrees -into %s to recreate those with available repositories and branches.\n", len(m.Worktrees), o.Root)
	}
	return m, err
}
func safeRoot(root string) error {
	if root == filepath.VolumeName(root)+string(filepath.Separator) {
		return errors.New("refusing filesystem root")
	}
	if st, e := os.Lstat(root); e == nil && st.Mode()&os.ModeSymlink != 0 {
		return errors.New("ATTO_DIR must not be a symlink")
	}
	return nil
}

// canonical resolves existing ancestors too, so missing paths under /var and
// /private/var (or equivalent user directory aliases) compare consistently.
func canonical(p string) string {
	p, e := filepath.Abs(p)
	if e != nil {
		return p
	}
	if resolved, e := filepath.EvalSymlinks(p); e == nil {
		return resolved
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p
	}
	return filepath.Join(canonical(parent), filepath.Base(p))
}

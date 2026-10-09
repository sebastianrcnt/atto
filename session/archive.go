package session

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/fsutil"
)

func underRoot(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// archiveShadowed hides duplicate copies left by an interrupted transfer.
// Live JSONL always wins, then a legacy plain archive, then a zstd archive.
func archiveShadowed(path string) bool {
	rel, err := filepath.Rel(config.ArchivedDir(), strings.TrimSuffix(path, ".zst"))
	if err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(config.SessionsDir(), rel)); err == nil {
		return true
	}
	if plain, ok := strings.CutSuffix(path, ".zst"); ok {
		if _, err := os.Stat(plain); err == nil {
			return true
		}
	}
	return false
}

// Archive stream-compresses a session under its writer lease. A complete,
// flushed destination is installed before removing the original JSONL.
func Archive(path string) (string, error) {
	if !underRoot(path, config.SessionsDir()) || !strings.HasSuffix(path, ".jsonl") {
		return "", fmt.Errorf("%s is not a live session", path)
	}
	rel, _ := filepath.Rel(config.SessionsDir(), path)
	plain := filepath.Join(config.ArchivedDir(), rel)
	if _, err := os.Stat(plain); err == nil {
		return "", fmt.Errorf("session already exists at %s", plain)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return transferSession(path, filepath.Join(config.ArchivedDir(), rel)+".zst", true, nil)
}

// Unarchive restores compressed or legacy plain archives to live JSONL.
func Unarchive(path string) (string, error) {
	if !underRoot(path, config.ArchivedDir()) || !IsSessionFile(path) {
		return "", fmt.Errorf("%s is not an archived session", path)
	}
	rel, _ := filepath.Rel(config.ArchivedDir(), strings.TrimSuffix(path, ".zst"))
	return transferSession(path, filepath.Join(config.SessionsDir(), rel), false, nil)
}

// RestoreForWrite returns a live path, restoring an archive if necessary.
// Frontends call this before taking the live writer lease. Read-only viewers
// use Load/ReadActive directly and never restore an archive.
func RestoreForWrite(path string) (string, error) {
	if underRoot(path, config.ArchivedDir()) {
		return Unarchive(path)
	}
	return path, nil
}

// transferSession's optional checkpoint is a failure-injection seam for tests.
// All handles to source and temp are closed before rename/remove (Windows).
func transferSession(path, dst string, compress bool, checkpoint func(string) error) (string, error) {
	release, err := Lock(path)
	if err != nil {
		return "", err
	}
	defer release()
	if err := fsutil.PrivateDirs(config.Dir(), filepath.Dir(dst)); err != nil {
		return "", err
	}
	if LockPath(path) != LockPath(dst) {
		dstRelease, err := Lock(dst)
		if err != nil {
			return "", err
		}
		defer dstRelease()
	}
	// Retrying archive/migration replaces an interrupted destination. Restore
	// must never overwrite a live session that may have gained new entries.
	if !compress {
		if _, err := os.Stat(dst); err == nil {
			return "", fmt.Errorf("session already exists at %s", dst)
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	src, err := Open(path)
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".atto-archive-*")
	if err != nil {
		src.Close()
		return "", err
	}
	defer os.Remove(tmp.Name())
	err = copySession(tmp, src, compress)
	closeErr := src.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr = tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if checkpoint != nil {
		if err := checkpoint("written"); err != nil {
			return "", err
		}
	}
	if err := archiveRename(tmp.Name(), dst); err != nil {
		return "", err
	}
	if err := syncSessionDir(filepath.Dir(dst)); err != nil {
		return dst, err
	}
	if checkpoint != nil {
		if err := checkpoint("renamed"); err != nil {
			return dst, err
		}
	}
	if LogPath(path) != LogPath(dst) {
		if err := archiveRename(LogPath(path), LogPath(dst)); err != nil && !os.IsNotExist(err) {
			return dst, err
		}
	}
	if err := archiveRemove(path); err != nil {
		return dst, err
	}
	if err := syncSessionDir(filepath.Dir(path)); err != nil {
		return dst, err
	}
	if LockPath(path) != LockPath(dst) {
		if err := archiveRemove(LockPath(path)); err != nil && !os.IsNotExist(err) {
			return dst, err
		}
	}
	// Remove older representations too, so restore cannot leave a stale archive.
	if !compress {
		other := strings.TrimSuffix(path, ".zst")
		if other == path {
			other += ".zst"
		}
		if err := archiveRemove(other); err != nil && !os.IsNotExist(err) {
			return dst, err
		}
	}
	return dst, nil
}

func copySession(dst io.Writer, src io.Reader, compress bool) error {
	if !compress {
		_, err := io.Copy(dst, src)
		return err
	}
	w, err := zstd.NewWriter(dst, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return err
	}
	_, err = io.Copy(w, src)
	closeErr := w.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// CompressionStats describes files migrated from legacy plain archives.
type CompressionStats struct {
	Count         int
	Before, After int64
}

// CompressArchives migrates plain archived JSONL explicitly, never at startup.
// Each file uses the same leases and durable transfer as Archive. On error the
// returned statistics include files already completed; rerunning is safe.
func CompressArchives() (stats CompressionStats, err error) {
	err = filepath.WalkDir(config.ArchivedDir(), func(path string, d fs.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		before, err := d.Info()
		if err != nil {
			return err
		}
		dst, err := transferSession(path, path+".zst", true, nil)
		if err != nil {
			return err
		}
		after, err := os.Stat(dst)
		if err != nil {
			return err
		}
		stats.Count++
		stats.Before += before.Size()
		stats.After += after.Size()
		return nil
	})
	return stats, err
}

// RemoveCopies deletes all representations, including duplicates left after
// a crash. The caller holds the selected session's lease and handles sidecars.
func RemoveCopies(path string) error {
	root, other := config.SessionsDir(), config.ArchivedDir()
	if underRoot(path, other) {
		root, other = other, root
	}
	if !underRoot(path, root) {
		return fmt.Errorf("not a session path: %s", path)
	}
	plain := strings.TrimSuffix(path, ".zst")
	rel, _ := filepath.Rel(root, plain)
	// The other root may contain a crash-left copy or a concurrently resumed
	// writer. Take its lease too before deleting anything from either root.
	counterpart := filepath.Join(other, rel)
	release, err := Lock(counterpart)
	if err != nil {
		return err
	}
	defer release()
	paths := []string{plain, plain + ".zst", filepath.Join(other, rel), filepath.Join(other, rel) + ".zst"}
	for _, p := range paths {
		if err := archiveRemove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	for _, sidecar := range []string{LockPath(counterpart), LogPath(counterpart)} {
		if err := archiveRemove(sidecar); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

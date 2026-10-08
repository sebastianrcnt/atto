package agentstate

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/fsutil"
)

// legacyDir is retained for old installs and daemons using ~/.atto/subagents/.
func legacyDir() string { return filepath.Join(config.Dir(), "subagents") }

// stateRoot migrates once, under a persistent lock outside either layout.
// Busy old daemons/turns keep their directory until a later idle access. The
// compatibility symlink keeps old writers and their lock inodes on the same
// files after the rename; without symlink privileges we keep the old layout.
func stateRoot() string {
	current, old := config.AgentStateDir(), legacyDir()
	link := filepath.Join(config.Dir(), ".agent-state-compat")
	if _, err := os.Stat(current); !errors.Is(err, os.ErrNotExist) {
		// The temporary alias marks the rename/install window. New processes
		// must wait for it to finish before choosing coordination lock paths.
		if _, err := os.Lstat(link); errors.Is(err, os.ErrNotExist) {
			return current
		}
	}
	if _, err := os.Stat(old); errors.Is(err, os.ErrNotExist) {
		if _, err := os.Lstat(link); errors.Is(err, os.ErrNotExist) {
			return current
		}
	}
	_ = fsutil.WithFileLock(filepath.Join(config.Dir(), ".agent-state-migration"), func() error {
		if _, err := os.Stat(current); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				// Recover a crash after rename but before alias installation.
				if _, err := os.Lstat(old); errors.Is(err, os.ErrNotExist) {
					_ = os.Rename(link, old)
				}
				_ = os.Remove(link)
			}
			return err
		}
		var held []*os.File
		defer func() {
			for _, f := range held {
				unlock(f)
				f.Close()
			}
		}()
		guard := func(path string) error {
			f, err := openStateGuard(path)
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if !tryLock(f) {
				f.Close()
				return errors.New("agent state is in use")
			}
			held = append(held, f)
			return nil
		}
		if err := guard(filepath.Join(config.Dir(), "run", "daemon.lock")); err != nil {
			return err
		}
		if err := filepath.WalkDir(old, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && (strings.HasSuffix(path, ".lock") || filepath.Base(filepath.Dir(path)) == "slots") {
				return guard(path)
			}
			return nil
		}); err != nil {
			return err
		}
		_ = os.Remove(link) // a prior interrupted migration's temporary link
		if err := stateAlias("agent-state", link); err != nil {
			return err
		}
		defer os.Remove(link)
		if err := os.Rename(old, current); err != nil {
			return err
		}
		// An old process might have recreated old meanwhile. Leave both
		// layouts intact in that case; reads below merge them by agent id.
		return os.Rename(link, old)
	})
	if _, err := os.Stat(current); errors.Is(err, os.ErrNotExist) {
		return old
	}
	return current
}

func stateRoots() []string {
	root := stateRoot()
	if root == legacyDir() {
		return []string{root}
	}
	return []string{root, legacyDir()}
}

// existingPath prefers the new layout, falling back only for missing ids.
// Writes to an old-only record stay beside its old daemon's turn and locks.
func existingPath(parts ...string) string {
	roots := stateRoots()
	for _, root := range roots {
		path := filepath.Join(append([]string{root}, parts...)...)
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			return path
		}
	}
	return filepath.Join(append([]string{roots[0]}, parts...)...)
}

func parentIDs() []string {
	seen := map[string]bool{}
	var out []string
	for _, root := range stateRoots() {
		dirs, _ := os.ReadDir(root)
		for _, d := range dirs {
			if d.IsDir() && d.Name() != "_up" && !seen[d.Name()] {
				seen[d.Name()] = true
				out = append(out, d.Name())
			}
		}
	}
	return out
}

// coordinationDir keeps tree/slot/turn locks with an old daemon when both
// layouts exist, even when some of that parent's records use the new layout.
func coordinationDir(parent string) string {
	root := stateRoot()
	old := filepath.Join(legacyDir(), parent)
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		return old
	}
	return filepath.Join(root, parent)
}

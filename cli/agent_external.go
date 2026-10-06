package cli

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/fsutil"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/subagent"
)

func externalParentPath() (string, string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", err
	}
	root := agent.ProjectRoot(cwd)
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	key := root
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	return filepath.Join(config.Dir(), "external_parents", fmt.Sprintf("%x", sha256.Sum256([]byte(key)))), root, nil
}

// externalParent serializes creation and start/rm against the project mapping.
// Without create it only looks one up, and returns "" when there is none: a
// command that reads (list, wait, report) has nothing to show without one.
func externalParent(out io.Writer, create bool) (string, func(), error) {
	path, root, err := externalParentPath()
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", nil, err
	}
	var release func()
	for {
		release, err = session.Lock(path)
		if !errors.Is(err, session.ErrLocked) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		return "", nil, err
	}
	fail := func(err error) (string, func(), error) { release(); return "", nil, err }
	b, err := os.ReadFile(path)
	if err == nil {
		id := strings.TrimSpace(string(b))
		if p, err := session.Find(id); err == nil && !isArchived(p) {
			return id, release, nil
		}
	} else if !os.IsNotExist(err) {
		return fail(err)
	}
	if !create {
		return "", release, nil
	}
	w := session.NewExternal(root)
	w.Append(session.Entry{Type: session.TypeName, Name: "atto agent (external)"})
	w.Close()
	if err := w.Err(); err != nil {
		return fail(err)
	}
	if err := fsutil.WriteAtomic(path, []byte(w.ID+"\n"), 0o644); err != nil {
		return fail(err)
	}
	// Diagnostics must not contaminate a machine-readable report.
	fmt.Fprintf(out, "external parent created: %s\n", w.ID)
	return w.ID, release, nil
}

func forgetExternalParent(parent string) error {
	if len(subagent.List(parent)) != 0 {
		return nil
	}
	p, err := session.Find(parent)
	if err != nil {
		return nil
	} // explicit parents need not have a session file
	h, _, err := session.Load(p)
	if err != nil {
		return err
	}
	if !h.External {
		return nil
	}
	if !isArchived(p) {
		if _, err := session.Archive(p); err != nil {
			return err
		}
	}
	// -session can select an external parent from another project.
	paths, err := filepath.Glob(filepath.Join(config.Dir(), "external_parents", "*"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		if len(filepath.Base(path)) != sha256.Size*2 {
			continue
		}
		b, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(b)) == parent {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	return nil
}

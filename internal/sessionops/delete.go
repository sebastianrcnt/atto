package sessionops

import (
	"fmt"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/outputs"
	"github.com/sebastianrcnt/atto/session"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Delete permanently removes a session and its resources, after acquiring its
// writer lease. Callers must obtain user confirmation before calling it.
func Delete(out io.Writer, path string) error {
	h, err := session.ReadHeader(path)
	if err != nil {
		return err
	}
	id := h.ID
	if id == "" || strings.ContainsAny(id, `/\.`) {
		return fmt.Errorf("unexpected session id %q", id)
	}
	release, err := session.Lock(path)
	if err != nil {
		return err
	}
	defer release()
	active := jobs.ActiveCount(id)
	// Stop jobs first: a supervisor still writing into the directory would
	// recreate it after the removal.
	if active > 0 {
		jobs.KillAll(id)
	}

	// Images this session used; scanned before the file is gone.
	used, err := ImageRefs(path)
	if err != nil {
		return err
	}
	if err := session.RemoveCopies(path); err != nil {
		return err
	}
	for _, sidecar := range []string{session.LockPath(path), session.LogPath(path)} {
		if err := os.Remove(sidecar); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(out, "warning: %v\n", err)
		}
	}
	for _, dir := range []string{jobs.Root(id), events.Dir(id)} {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintf(out, "warning: %v\n", err)
		}
	}
	if err := os.Remove(goal.Path(id)); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(out, "warning: %v\n", err)
	}
	if err := outputs.RemoveSession(id); err != nil {
		fmt.Fprintf(out, "warning: %v\n", err)
	}
	fmt.Fprintf(out, "Deleted session %s.\n", id)

	n, err := pruneImages(used)
	if err != nil {
		fmt.Fprintf(out, "warning: images not cleaned up: %v\n", err)
	} else if n > 0 {
		fmt.Fprintf(out, "Removed %d image(s) no other session uses.\n", n)
	}
	return nil
}

// imageName matches a stored image's file name: sha256 hex plus extension.
var imageName = regexp.MustCompile(`[0-9a-f]{64}\.[A-Za-z0-9]{2,5}`)

// imageRefs returns the image file names a session file mentions. It scans
// the raw text rather than decoding entries, so an image is counted wherever
// it appears (a message, a compaction's replacement history, a future entry
// type): keeping an image too long is harmless, deleting a used one is not.
func ImageRefs(path string) (map[string]bool, error) {
	r, err := session.Open(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	refs := map[string]bool{}
	for _, m := range imageName.FindAll(data, -1) {
		refs[string(m)] = true
	}
	return refs, nil
}

// pruneImages deletes the candidate images that no remaining session,
// active or archived, references. If any session can't be read the answer
// is unknown, so nothing is deleted. Only candidates (the deleted session's
// own images) are considered: other orphans, and images a running session
// has saved but not yet written to its file, are left alone.
func pruneImages(candidates map[string]bool) (int, error) {
	if len(candidates) == 0 {
		return 0, nil
	}
	for _, root := range []string{config.SessionsDir(), config.ArchivedDir()} {
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if d.IsDir() || !session.IsSessionFile(p) {
				return nil
			}
			refs, err := ImageRefs(p)
			if err != nil {
				return err
			}
			for name := range refs {
				delete(candidates, name)
			}
			return nil
		})
		if err != nil {
			return 0, err
		}
	}
	n := 0
	for name := range candidates {
		err := os.Remove(filepath.Join(images.Dir(), name))
		if err == nil {
			n++
		} else if !os.IsNotExist(err) {
			return n, err
		}
	}
	return n, nil
}

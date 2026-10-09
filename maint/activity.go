package maint

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/daemon"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/shell"
)

func sortWorktrees(w []Worktree) { sort.Slice(w, func(i, j int) bool { return w[i].Path < w[j].Path }) }
func socketLive(p string) bool {
	c, e := net.DialTimeout("unix", p, 150*time.Millisecond)
	if e == nil {
		c.Close()
		return true
	}
	return !os.IsNotExist(e) && !connectionRefused(e)
}
func jobActive(j jobs.Job) bool {
	return j.Active() && (shell.Alive(j.SupervisorPID) || shell.Alive(j.PID) || j.SupervisorPID == 0 && (j.Started.IsZero() || time.Since(j.Started) < time.Minute))
}
func readJob(p string) (jobs.Job, error) {
	var j jobs.Job
	b, e := os.ReadFile(p)
	if e != nil {
		return j, e
	}
	e = json.Unmarshal(b, &j)
	return j, e
}

// Activity never rewrites job metadata. Locks are tested with a nonblocking OS probe.
func Activity(root string) ([]string, error) {
	var out []string
	if a, _ := filepath.Abs(root); a != "" {
		b, _ := filepath.Abs(config.Dir())
		if a == b {
			workers, e := daemon.Status()
			if e != nil && e != daemon.ErrUnavailable {
				return nil, e
			}
			for _, w := range workers {
				out = append(out, "worker "+w.Session)
			}
		}
	}
	for _, dir := range []string{"sessions", "archived_sessions", "agent-state", "subagents", "jobs", "run"} {
		base := filepath.Join(root, dir)
		if st, e := os.Lstat(base); e == nil && st.Mode()&os.ModeSymlink != 0 {
			continue
		}
		e := filepath.WalkDir(base, func(p string, d fs.DirEntry, e error) error {
			if os.IsNotExist(e) {
				return nil
			}
			if e != nil {
				return e
			}
			if d.IsDir() {
				return nil
			}
			switch {
			case session.IsSessionFile(p):
				if _, busy := session.LockedBy(p); busy {
					out = append(out, "session "+filepath.Base(p))
				}
			case strings.HasSuffix(p, ".lock") || filepath.Base(filepath.Dir(p)) == "slots":
				if busyLock(p) {
					out = append(out, "lock "+p)
				}
			case filepath.Base(p) == "job.json":
				j, e := readJob(p)
				if e != nil {
					return e
				}
				if jobActive(j) {
					out = append(out, fmt.Sprintf("job %s/%d", j.Session, j.ID))
				}
			case strings.HasSuffix(p, ".sock"):
				if socketLive(p) {
					out = append(out, "socket "+p)
				}
			}
			return nil
		})
		if e != nil {
			return nil, e
		}
	}
	return out, nil
}
func sessionPaths(root string) (map[string]string, error) {
	out := map[string]string{}
	for _, dir := range []string{"sessions", "archived_sessions"} {
		e := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, e error) error {
			if os.IsNotExist(e) {
				return nil
			}
			if e != nil {
				return e
			}
			if !d.IsDir() && session.IsSessionFile(p) {
				id := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(p), ".zst"), ".jsonl")
				out[id] = p
			}
			return nil
		})
		if e != nil {
			return nil, e
		}
	}
	return out, nil
}
func configRoot() string { return config.Dir() }

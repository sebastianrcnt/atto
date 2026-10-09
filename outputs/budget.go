package outputs

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// budgetEvery is the least time between two scans of the directory.
	budgetEvery = time.Minute
	// orphanAge is how old an unfinished file of no running command in this
	// process must be to count as left behind by a crashed one.
	orphanAge = 24 * time.Hour
)

var (
	// freeSpace and now are replaced in tests.
	freeSpace = diskFree
	now       = time.Now

	mu         sync.Mutex
	lastBudget time.Time
	active     = map[string]int{} // files being written by this process
)

func setActive(path string) { mu.Lock(); active[path]++; mu.Unlock() }
func clearActive(path string) {
	mu.Lock()
	if active[path]--; active[path] <= 0 {
		delete(active, path)
	}
	mu.Unlock()
}

func isActive(path string) bool {
	mu.Lock()
	defer mu.Unlock()
	return active[path] > 0
}

// unfinished says whether name is a file a command is still writing: the
// compressed file before it is renamed, and the ring of its last bytes.
func unfinished(name string) bool {
	return strings.HasSuffix(name, ".part") || strings.HasSuffix(name, ".tail")
}

// enforceBudget deletes the oldest files until the directory is under 90%
// of lim.Total, if it is over it. It scans at most once a minute unless
// force is set, and leaves alone every file a command is writing.
func enforceBudget(lim Limits, force bool) {
	if lim.Total < 0 {
		return
	}
	mu.Lock()
	t := now()
	if !force && !lastBudget.IsZero() && t.Sub(lastBudget) < budgetEvery {
		mu.Unlock()
		return
	}
	lastBudget = t
	mu.Unlock()

	type file struct {
		path string
		size int64
		mod  time.Time
	}
	var files []file
	var total int64
	_ = filepath.WalkDir(Root(), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		files = append(files, file{path, info.Size(), info.ModTime()})
		total += info.Size()
		return nil
	})
	if total <= lim.Total {
		return
	}
	target := lim.Total / 10 * 9
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, f := range files {
		if total <= target {
			break
		}
		if isActive(f.path) || (unfinished(f.path) && t.Sub(f.mod) < orphanAge) {
			continue
		}
		if os.Remove(f.path) == nil {
			total -= f.size
		}
	}
}

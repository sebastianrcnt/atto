package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/fsutil"
	"github.com/sebastianrcnt/atto/shell"
)

func Root(session string) string {
	if !fsutil.ValidID(session) {
		session = ".invalid-session"
	}
	return filepath.Join(config.Dir(), "jobs", session)
}

func dirOf(session string, id int) string {
	return filepath.Join(Root(session), strconv.Itoa(id))
}

func OutputPath(session string, id int) string {
	return filepath.Join(dirOf(session, id), "output.log")
}

var saveMu sync.Mutex

func save(dir string, j Job) error {
	saveMu.Lock()
	defer saveMu.Unlock()
	path := filepath.Join(dir, "job.json")
	data, err := json.MarshalIndent(j, "", "  ")
	if err == nil {
		err = fsutil.WriteAtomic(path, data, 0o600)
	}
	if err != nil {
		return fmt.Errorf("saving job %d state to %s: %w", j.ID, path, err)
	}
	return nil
}

func load(dir string) (Job, error) {
	var j Job
	data, err := fsutil.ReadFile(filepath.Join(dir, "job.json"))
	if err != nil {
		return j, err
	}
	return j, json.Unmarshal(data, &j)
}

// startGrace is how long a job may wait for its supervisor to start.
const startGrace = time.Minute

// Get loads a job, marking it lost if its supervisor is gone.
func Get(session string, id int) (Job, error) {
	if !fsutil.ValidID(session) || id <= 0 {
		return Job{}, fmt.Errorf("invalid session or job id")
	}
	j, err := load(dirOf(session, id))
	if errors.Is(err, os.ErrNotExist) {
		return j, fmt.Errorf("no job %d", id)
	}
	if err == nil && j.Status == Starting && j.SupervisorPID == 0 && time.Since(j.Started) > startGrace {
		// Its supervisor never came up (or died before saying so).
		now := time.Now()
		j.Status, j.Ended = Lost, &now
		err = save(dirOf(session, id), j)
	}
	if err == nil && j.Active() && j.SupervisorPID > 0 && !shell.Alive(j.SupervisorPID) {
		// Re-read: the supervisor may have written its final state just now.
		if j2, err2 := load(dirOf(session, id)); err2 == nil && j2.Active() {
			now := time.Now()
			j2.Status, j2.Ended = Lost, &now
			err = save(dirOf(session, id), j2)
			j = j2
		} else if err2 == nil {
			j = j2
		} else {
			err = err2
		}
	}
	return j, err
}

// List returns a session's jobs, oldest first, on a best-effort basis. It
// retains a job whose lost state could not be saved. Use ListWithErrors when
// the caller can report read or reconciliation errors.
func List(session string) []Job {
	out, _ := ListWithErrors(session)
	return out
}

// ListWithErrors returns the available jobs and any errors encountered while
// reading or reconciling them. A partial list must not be mistaken for no jobs.
func ListWithErrors(session string) ([]Job, error) {
	ents, err := os.ReadDir(Root(session))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Job
	var errs []error
	for _, e := range ents {
		id, err := strconv.Atoi(e.Name())
		if err != nil || !e.IsDir() {
			continue
		}
		j, err := Get(session, id)
		if err != nil {
			errs = append(errs, fmt.Errorf("job %d: %w", id, err))
		}
		if err == nil || j.ID == id && j.Status == Lost {
			out = append(out, j)
		}
	}
	sort.Slice(out, func(i, k int) bool { return out[i].ID < out[k].ID })
	return out, errors.Join(errs...)
}

// ActiveCount counts active jobs.
func ActiveCount(session string) int {
	n := 0
	for _, j := range List(session) {
		if j.Active() {
			n++
		}
	}
	return n
}

// newDir reserves the next job ID by creating its directory.
func newDir(session string) (int, string, error) {
	if err := fsutil.PrivateDirs(config.Dir(), Root(session)); err != nil {
		return 0, "", err
	}
	next := 1
	for _, j := range List(session) {
		next = max(next, j.ID+1)
	}
	for ; ; next++ {
		dir := dirOf(session, next)
		if err := os.Mkdir(dir, 0o700); err == nil {
			return next, dir, nil
		} else if !os.IsExist(err) {
			return 0, "", err
		}
	}
}

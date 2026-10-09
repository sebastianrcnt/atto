package outputs

import (
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/sebastianrcnt/atto/config"
)

const (
	mib = 1 << 20

	// DefaultFileHead and DefaultFileTail are how much of the start and of
	// the end of a command's output its file keeps.
	DefaultFileHead = 32 * mib
	DefaultFileTail = 32 * mib
	// DefaultTotal is the size of ~/.atto/outputs above which the oldest
	// files are deleted.
	DefaultTotal = 1024 * mib
	// DefaultMinFree is the free disk space below which no file is written.
	DefaultMinFree = 1024 * mib
)

// Limits are the caps on saved output. In a Limits passed to SetLimits a
// zero field means the default; for Total and MinFree a negative one turns
// the limit off.
type Limits struct {
	// FileHead and FileTail are the uncompressed bytes a file keeps from
	// the start and from the end of the output. A longer output has its
	// middle replaced by one marker line.
	FileHead, FileTail int64
	// Total caps the size of all saved output on disk.
	Total int64
	// MinFree is the free disk space needed to write a file at all.
	MinFree int64
}

func (l Limits) normalized() Limits {
	if l.FileHead <= 0 {
		l.FileHead = DefaultFileHead
	}
	if l.FileTail <= 0 {
		l.FileTail = DefaultFileTail
	}
	if l.Total == 0 {
		l.Total = DefaultTotal
	}
	if l.MinFree == 0 {
		l.MinFree = DefaultMinFree
	}
	return l
}

var limits atomic.Pointer[Limits]

// SetLimits sets the caps for the files written from now on.
func SetLimits(l Limits) {
	n := l.normalized()
	limits.Store(&n)
}

// CurrentLimits are the caps in force.
func CurrentLimits() Limits {
	if l := limits.Load(); l != nil {
		return *l
	}
	return Limits{}.normalized()
}

// FromSettings converts settings.json's "toolOutput".
func FromSettings(s *config.ToolOutputSettings) Limits {
	if s == nil {
		return Limits{}
	}
	mb := func(n int) int64 {
		switch {
		case n == 0:
			return 0
		case n < 0:
			return -1
		}
		return int64(n) * mib
	}
	l := Limits{FileHead: mb(s.FileHeadMB), FileTail: mb(s.FileTailMB), Total: mb(s.TotalMB), MinFree: mb(s.MinFreeMB)}
	// A negative size for a file's start or end is not "off": the default.
	l.FileHead, l.FileTail = max(l.FileHead, 0), max(l.FileTail, 0)
	return l
}

// Root is the directory holding every session's saved output.
func Root() string { return filepath.Join(config.Dir(), "outputs") }

const noSession = "_nosession"

// SessionDir is where the saved output of a session's commands goes.
func SessionDir(id string) string {
	name := safeName(id)
	if name == "" {
		name = noSession
	}
	return filepath.Join(Root(), name)
}

// RemoveSession deletes the saved output of a session. It does nothing for
// an empty id: sessions without one share a directory.
func RemoveSession(id string) error {
	if safeName(id) == "" {
		return nil
	}
	err := os.RemoveAll(SessionDir(id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// safeName keeps the letters, numbers and . _ - of s, so it is a single
// path element; "" if nothing is left.
func safeName(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s) && len(b) < 80; i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
			b = append(b, c)
		default:
			b = append(b, '_')
		}
	}
	name := string(b)
	if name == "" || name == "." || name == ".." {
		return ""
	}
	return name
}

package app

import (
	"strings"
	"sync"
	"testing"

	"github.com/sebastianrcnt/atto/session"
)

// recTerm records what is written to the terminal.
type recTerm struct {
	mu  sync.Mutex
	out strings.Builder
}

func (*recTerm) Start(func(string), func()) error { return nil }
func (*recTerm) Stop()                            {}
func (*recTerm) Size() (int, int)                 { return 80, 24 }
func (r *recTerm) Write(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.out.WriteString(s)
}

func (r *recTerm) take() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.out.String()
	r.out.Reset()
	return s
}

// recordedApp runs a session with a recording terminal.
func recordedApp(t *testing.T) (*App, *recTerm) {
	t.Helper()
	cwd, _ := testEnv(t)
	rec := &recTerm{}
	a := startAppTerm(t, cwd, rec)
	return a, rec
}

// quitting reports whether the App was asked to quit.
func quitting(a *App) bool {
	select {
	case <-a.quit:
		return true
	default:
		return false
	}
}

func menuText(a *App) string {
	if a.modal == nil {
		return ""
	}
	return plainLines(a.modal.Render(100))
}

func TestAppFixturesReleaseSessionLease(t *testing.T) {
	for _, tt := range []struct {
		name string
		make func(*testing.T) *App
	}{
		{"tree", func(t *testing.T) *App { return treeApp(t) }},
		{"recorded", func(t *testing.T) *App { a, _ := recordedApp(t); return a }},
		{"loaded", loadedApp},
	} {
		var path string
		t.Run(tt.name, func(t *testing.T) {
			a := tt.make(t)
			path = a.sessPath
			if _, held := session.LockedBy(path); !held {
				t.Fatal("fixture has no session lease")
			}
		})
		if _, held := session.LockedBy(path); held {
			t.Errorf("%s fixture retained lease", tt.name)
		}
	}
}

//go:build windows

package jobs

import (
	"os"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/sebastianrcnt/atto/internal/consoletest"
	"github.com/sebastianrcnt/atto/shell"
)

// The test binary doubles as the shell host, which StartHost runs as
// os.Executable() "_shell".
func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "_shell" {
		if err := ServeHost(os.Stdin, os.Stdout, os.Stderr); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *lockedBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

// PowerShell sets the code page of the console it runs on. Commands run on
// a console of their own, so the user's terminal keeps its code page (and
// with it, under conhost, its font).
func TestHostLeavesConsoleCodePage(t *testing.T) {
	consoletest.Lock(t)
	cp, err := windows.GetConsoleOutputCP()
	if err != nil || cp == 0 {
		t.Skip("no console")
	}
	if err := windows.SetConsoleOutputCP(437); err != nil {
		t.Skip("cannot set the code page:", err)
	}
	defer windows.SetConsoleOutputCP(cp)
	t.Setenv("ATTO_DIR", t.TempDir())
	var out lockedBuf
	h, err := StartHost(shell.Default(), t.TempDir(), nil, "chcp.com 65001 > $null; Write-Output '한글'", &out)
	if err != nil {
		t.Fatal(err)
	}
	if st := <-h.Status(); st.Exit == nil {
		t.Fatalf("status %+v", st)
	}
	_ = h.Wait()
	if got, _ := windows.GetConsoleOutputCP(); got != 437 {
		t.Fatalf("console code page changed to %d", got)
	}
	if s := out.b.String(); !strings.Contains(s, "한글") {
		t.Fatalf("output %q", s)
	}
}

//go:build windows

package hooks

import (
	"context"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/sebastianrcnt/atto/internal/consoletest"
)

// A hook runs PowerShell, whose prelude sets the console's code pages. It
// gets a console of its own, so the user's terminal keeps its code pages.
func TestHookLeavesConsoleCodePage(t *testing.T) {
	consoletest.Lock(t)
	cp, err := windows.GetConsoleOutputCP()
	if err != nil || cp == 0 {
		t.Skip("no console")
	}
	in, _ := windows.GetConsoleCP()
	if windows.SetConsoleOutputCP(437) != nil || windows.SetConsoleCP(437) != nil {
		t.Skip("cannot set the code page")
	}
	defer windows.SetConsoleOutputCP(cp)
	defer windows.SetConsoleCP(in)
	r := New(cmd("UserPromptSubmit", "", "chcp.com 65001 > $null; exit 0"), t.TempDir())
	r.UserPromptSubmit(context.Background(), "hi")
	out, _ := windows.GetConsoleOutputCP()
	inp, _ := windows.GetConsoleCP()
	if out != 437 || inp != 437 {
		t.Fatalf("console code pages changed to %d/%d", out, inp)
	}
}

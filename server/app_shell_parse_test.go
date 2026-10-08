package server

import "testing"

func TestParseShell(t *testing.T) {
	for _, c := range []struct {
		in      string
		cmd     string
		exclude bool
		ok      bool
	}{
		{"!ls", "ls", false, true},
		{"! ls -la ", "ls -la", false, true},
		{"!!ls", "ls", true, true},
		{"!! ls", "ls", true, true},
		{"!!!x", "!x", true, true},
		{"!", "", false, false},
		{"!!", "", true, false},
		{"!  ", "", false, false},
		{"hello !ls", "", false, false},
		{"/help", "", false, false},
	} {
		cmd, ex, ok := parseShell(c.in)
		if cmd != c.cmd || ok != c.ok || (ok && ex != c.exclude) {
			t.Errorf("parseShell(%q) = %q %v %v; want %q %v %v", c.in, cmd, ex, ok, c.cmd, c.exclude, c.ok)
		}
	}
}

func TestCtrlBRequestsUserShellBackground(t *testing.T) {
	h := newHarness(t)
	th, _ := h.s.thread(h.id)
	th.call(func() error {
		th.shell = &shellRun{background: make(chan struct{}, 1)}
		if !th.backgroundShell() {
			t.Fatal("Ctrl+B refused a user shell")
		}
		select {
		case <-th.shell.background:
		default:
			t.Fatal("Ctrl+B did not reach the command")
		}
		th.shell = nil
		return nil
	})
}

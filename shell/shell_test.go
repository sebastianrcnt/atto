package shell

import (
	"errors"
	"slices"
	"testing"
)

func TestDetect(t *testing.T) {
	t.Setenv(EnvOverride, "")
	none := func(string) (string, error) { return "", errors.New("not found") }
	onPath := func(names ...string) func(string) (string, error) {
		return func(n string) (string, error) {
			if slices.Contains(names, n) {
				return `C:\bin\` + n + ".exe", nil
			}
			return "", errors.New("not found")
		}
	}
	existing := func(paths ...string) func(string) bool {
		return func(p string) bool {
			return slices.Contains(paths, p)
		}
	}

	cases := []struct {
		name   string
		goos   string
		look   func(string) (string, error)
		exists func(string) bool
		want   Shell
	}{
		{"windows pwsh on PATH", "windows", onPath("pwsh", "powershell"), existing(), Shell{PowerShell, `C:\bin\pwsh.exe`}},
		{"windows pwsh fallback path", "windows", none, existing(`C:\Program Files\PowerShell\7\pwsh.exe`), Shell{PowerShell, `C:\Program Files\PowerShell\7\pwsh.exe`}},
		{"windows only built-in powershell", "windows", none, existing(`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`), Shell{PowerShell, `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`}},
		{"windows nothing", "windows", none, existing(), Shell{Cmd, "cmd.exe"}},
		{"unix /bin/bash", "linux", none, existing("/bin/bash"), Shell{Bash, "/bin/bash"}},
		{"unix sh only", "linux", none, existing(), Shell{Sh, "/bin/sh"}},
	}
	for _, c := range cases {
		if got := detect(c.goos, c.look, c.exists); got != c.want {
			t.Errorf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}

	t.Setenv(EnvOverride, `D:\tools\pwsh.exe`)
	if got := detect("windows", none, existing()); got.Kind != PowerShell {
		t.Errorf("override kind %v", got.Kind)
	}
}

func TestArgs(t *testing.T) {
	ps := Shell{PowerShell, "pwsh"}.Args("Get-Date")
	if ps[1] != "-NoProfile" || ps[3] != "-Command" || ps[4] != utf8Prelude+"Get-Date" {
		t.Fatalf("powershell args %q", ps)
	}
	if b := (Shell{Bash, "/bin/bash"}).Args("ls"); b[1] != "-c" || b[2] != "ls" {
		t.Fatalf("bash args %q", b)
	}
	if n := (Shell{PowerShell, "x"}).ToolName(); n != "powershell" {
		t.Fatal(n)
	}
}

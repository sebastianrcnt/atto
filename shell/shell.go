// Package shell picks the one shell atto runs commands with, following
// codex-rs (shell-command/src/shell_detect.rs): bash on Unix; PowerShell on
// Windows (pwsh, then Windows PowerShell 5.1, which ships with Windows),
// with cmd.exe as the last resort. The model's single tool is named after
// the shell, and hooks and status line commands use the same shell.
package shell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

type Kind string

const (
	Bash       Kind = "bash"
	Sh         Kind = "sh"
	PowerShell Kind = "powershell"
	Cmd        Kind = "cmd"
)

// Shell is a resolved shell executable.
type Shell struct {
	Kind Kind
	Path string
}

// EnvOverride names an environment variable that forces a shell path.
const EnvOverride = "ATTO_SHELL"

var (
	once     sync.Once
	detected Shell
)

// Default returns the shell for this machine, detected once.
func Default() Shell {
	once.Do(func() { detected = detect(runtime.GOOS, exec.LookPath, fileExists) })
	return detected
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// detect resolves the shell. lookPath and exists are injected for tests.
func detect(goos string, lookPath func(string) (string, error), exists func(string) bool) Shell {
	if p := os.Getenv(EnvOverride); p != "" {
		return Shell{Kind: kindOf(p), Path: p}
	}
	find := func(kind Kind, name string, fallbacks ...string) (Shell, bool) {
		if p, err := lookPath(name); err == nil {
			return Shell{kind, p}, true
		}
		for _, f := range fallbacks {
			if exists(f) {
				return Shell{kind, f}, true
			}
		}
		return Shell{}, false
	}
	if goos == "windows" {
		if s, ok := find(PowerShell, "pwsh", `C:\Program Files\PowerShell\7\pwsh.exe`); ok {
			return s
		}
		if s, ok := find(PowerShell, "powershell", `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`); ok {
			return s
		}
		return Shell{Cmd, "cmd.exe"}
	}
	if exists("/bin/bash") {
		return Shell{Bash, "/bin/bash"}
	}
	if s, ok := find(Bash, "bash"); ok {
		return s
	}
	return Shell{Sh, "/bin/sh"}
}

// kindOf classifies a shell path by its file name.
func kindOf(path string) Kind {
	base := strings.ToLower(filepath.Base(strings.ReplaceAll(path, `\`, "/")))
	base = strings.TrimSuffix(base, ".exe")
	switch base {
	case "pwsh", "powershell":
		return PowerShell
	case "cmd":
		return Cmd
	case "sh", "dash":
		return Sh
	}
	return Bash
}

// ToolName is what the model calls the tool: "bash" or "powershell".
func (s Shell) ToolName() string {
	switch s.Kind {
	case PowerShell:
		return "powershell"
	case Cmd:
		return "cmd"
	}
	return "bash"
}

// utf8Prelude makes Windows PowerShell read and write UTF-8 on pipes; by
// default it uses the OEM code page (e.g. CP949 on Korean Windows),
// garbling output and any JSON a hook reads from stdin.
const utf8Prelude = "[Console]::InputEncoding = [System.Text.Encoding]::UTF8; [Console]::OutputEncoding = [System.Text.Encoding]::UTF8; $OutputEncoding = [System.Text.Encoding]::UTF8; "

// Args returns the argv that runs script with this shell. On Windows, cmd
// scripts must use Command, which also sets their raw command line.
func (s Shell) Args(script string) []string {
	switch s.Kind {
	case PowerShell:
		return []string{s.Path, "-NoProfile", "-NonInteractive", "-Command", utf8Prelude + script}
	case Cmd:
		return []string{s.Path, "/d", "/s", "/c", script}
	}
	return []string{s.Path, "-c", script}
}

// Command builds an exec.Cmd running script with this shell. On Windows
// it runs on a console of its own (see PrivateConsole).
func (s Shell) Command(ctx context.Context, script string) *exec.Cmd {
	argv := s.Args(script)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	ownConsole(cmd)
	setCmdLine(cmd, s.Kind, script)
	return cmd
}

// PrivateConsole says this process's console is not the user's terminal
// (the shell host runs on a hidden one), so the commands it starts can
// share it. Otherwise each command gets a hidden console of its own on
// Windows: utf8Prelude sets the console's code pages, and on the user's
// terminal that would outlast atto.
var PrivateConsole bool

// Command runs script with the default shell.
func Command(ctx context.Context, script string) *exec.Cmd { return Default().Command(ctx, script) }

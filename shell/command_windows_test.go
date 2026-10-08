//go:build windows

package shell

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCmdEchoHelper(t *testing.T) {
	if os.Getenv("ATTO_CMD_ECHO_TEST") == "1" {
		fmt.Fprintln(os.Stdout, os.Args[len(os.Args)-1])
		os.Exit(0)
	}
}

func TestCmdCommandQuotes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "path with spaces")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	path := filepath.Join(dir, "echo helper.exe")
	dst, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatal(copyErr, closeErr)
	}
	cases := []struct{ script, want string }{
		{`echo "a b"`, `"a b"`},
		{`echo "x" & echo "y"`, "\"x\"\n\"y\""},
		{fmt.Sprintf(`"%s" -test.run=TestCmdEchoHelper -- "spaced arg"`, path), "spaced arg"},
	}
	for _, tree := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("%v/%s", tree, tc.script), func(t *testing.T) {
				cmd := (Shell{Cmd, "cmd.exe"}).Command(t.Context(), tc.script)
				cmd.Env = append(os.Environ(), "ATTO_CMD_ECHO_TEST=1")
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				var err error
				if tree {
					err = Run(cmd)
				} else {
					err = cmd.Run()
				}
				lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(stdout.String(), "\r\n", "\n")), "\n")
				for i := range lines {
					lines[i] = strings.TrimSpace(lines[i])
				}
				got := strings.Join(lines, "\n")
				if err != nil || got != tc.want {
					t.Fatalf("stdout %q, stderr %q: %v; want %q", got, stderr.String(), err, tc.want)
				}
			})
		}
	}
}

func TestCmdLineSurvivesProcessAttributes(t *testing.T) {
	script := `echo "a b" & echo "c"`
	path := filepath.Join(t.TempDir(), "shell with spaces", "cmd.exe")
	for _, mode := range []string{"console", "tree", "detach", "isolate"} {
		t.Run(mode, func(t *testing.T) {
			cmd := (Shell{Cmd, path}).Command(t.Context(), script)
			want := `"` + cmd.Path + `" /d /s /c "` + script + `"`
			cmd.SysProcAttr.CreationFlags |= windows.CREATE_UNICODE_ENVIRONMENT
			switch mode {
			case "console":
				ownConsole(cmd)
			case "tree":
				tree := NewTree(cmd)
				defer tree.Close()
				if cmd.SysProcAttr.CreationFlags&windows.CREATE_NEW_PROCESS_GROUP == 0 || cmd.SysProcAttr.CreationFlags&windows.CREATE_UNICODE_ENVIRONMENT == 0 {
					t.Fatal("tree overwrote creation flags")
				}
			case "detach":
				Detach(cmd)
			case "isolate":
				Isolate(cmd)
			}
			if cmd.SysProcAttr.CmdLine != want {
				t.Fatalf("raw command line %q, want %q", cmd.SysProcAttr.CmdLine, want)
			}
		})
	}
}

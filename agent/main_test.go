package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/outputs"
)

// The test binary doubles as atto for the processes commands run under:
// shell hosts (`_shell`) and job supervisors (`_supervise <dir>`), which
// are started by re-executing os.Executable(). With them, commands run
// as in the atto binary, under shell hosts.
//
// New scans the home directory for skills; tests keep off the real one.
func TestMain(m *testing.M) {
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "_shell":
			if err := jobs.ServeHost(os.Stdin, os.Stdout, os.Stderr); err != nil {
				os.Exit(1)
			}
			os.Exit(0)
		case "_fakeatto": // TestBackgroundedCommandOutlivesAtto
			ShellHost = true
			res := RunBash(context.Background(), os.Getenv("FAKEATTO_DIR"), []string{"ATTO_SESSION_ID=" + os.Getenv("FAKEATTO_SESSION")},
				BashArgs{Description: "outlive", Command: os.Args[2], Timeout: 1}, nil)
			fmt.Println(res.ForModel(BashArgs{Timeout: 1}))
			os.Exit(0) // without waiting for the job
		case "_view": // view_test.go: what atto view does, without package cli
			for _, path := range os.Args[2:] {
				im, err := images.ReadFile(path)
				if err == nil {
					err = images.Drop(os.Getenv(config.EnvView), filepath.Base(path), im)
				}
				if err != nil {
					fmt.Println(err)
					os.Exit(1)
				}
				fmt.Println("attached", filepath.Base(path))
			}
			os.Exit(0)
		case "_supervise":
			if len(os.Args) != 3 || jobs.Supervise(os.Args[2]) != nil {
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	ShellHost = true
	outputs.SetLimits(testLimits)
	home, err := os.MkdirTemp("", "atto-home")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	os.Setenv("USERPROFILE", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

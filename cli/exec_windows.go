//go:build windows

package cli

import (
	"errors"
	"os"
	"os/exec"
)

// execSelf runs exe (argv[0] first) on this terminal and exits with its
// status: Windows cannot replace a process image.
func execSelf(exe string, argv []string) error {
	cmd := exec.Command(exe, argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		os.Exit(exit.ExitCode())
	}
	if err != nil {
		return err
	}
	os.Exit(0)
	return nil
}

//go:build !windows

package session

import (
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"strconv"
)

func inheritFile(cmd *exec.Cmd, f *os.File) (string, error) {
	fd := 3 + len(cmd.ExtraFiles)
	cmd.ExtraFiles = append(cmd.ExtraFiles, f)
	return strconv.Itoa(fd), nil
}
func prepareTransfer(*os.File) error    { return nil }
func claimTransferred(f *os.File) error { return tryFileLock(f) }
func cancelTransfer(*os.File) error     { return nil }
func finishTransfer(f *os.File) error {
	// flock is shared by the inherited open file description. Explicitly
	// unlocking here would also unlock the child's lease.
	return f.Close()
}

func stopInheritance(f *os.File) { unix.CloseOnExec(int(f.Fd())) }

func finishClaim(*os.File) error { return nil }

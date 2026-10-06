package session

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

func inheritFile(cmd *exec.Cmd, f *os.File) (string, error) {
	h := windows.Handle(f.Fd())
	if err := windows.SetHandleInformation(h, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
		return "", err
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.AdditionalInheritedHandles = append(cmd.SysProcAttr.AdditionalInheritedHandles, syscall.Handle(h))
	return strconv.FormatUint(uint64(h), 10), nil
}

// Windows byte-range locks are not inherited. Transfer ownership with two
// guard bytes instead: parent retains A while child acquires B; only after
// its acknowledgement does parent release A. Normal contenders need BOTH
// bytes, so there is never a moment in which another writer can acquire.
func prepareTransfer(f *os.File) error  { return unlockByte(f, 1) }
func claimTransferred(f *os.File) error { return lockByte(f, 1) }
func cancelTransfer(f *os.File) error   { return lockByte(f, 1) }
func finishTransfer(f *os.File) error   { return f.Close() }

func stopInheritance(f *os.File) {
	_ = windows.SetHandleInformation(windows.Handle(f.Fd()), windows.HANDLE_FLAG_INHERIT, 0)
}

// B already protects the session. Once parent closes A, restore the usual
// two-byte lease so this child can itself perform a later handoff.
func finishClaim(f *os.File) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := lockByte(f, 0)
		if err == nil {
			return nil
		}
		if !fileLockBusy(err) || time.Now().After(deadline) {
			return fmt.Errorf("finish inherited session lease: %w", err)
		}
		time.Sleep(time.Millisecond)
	}
}

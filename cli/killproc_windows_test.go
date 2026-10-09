//go:build windows

package cli

import "golang.org/x/sys/windows"

// killProcess ends pid at once, as a crash would.
func killProcess(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}

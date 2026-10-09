//go:build !windows

package cli

import "syscall"

// killProcess ends pid at once, as a crash would.
func killProcess(pid int) error { return syscall.Kill(pid, syscall.SIGKILL) }

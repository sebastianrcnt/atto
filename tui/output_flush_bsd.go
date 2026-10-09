//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package tui

import "golang.org/x/sys/unix"

func flushTerminalOutput(fd int) { _ = unix.IoctlSetPointerInt(fd, unix.TIOCFLUSH, 2) } // FWRITE

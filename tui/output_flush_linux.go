//go:build linux

package tui

import "golang.org/x/sys/unix"

func flushTerminalOutput(fd int) { _ = unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCOFLUSH) }

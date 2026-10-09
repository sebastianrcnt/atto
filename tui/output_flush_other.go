//go:build !windows && !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package tui

func flushTerminalOutput(int) {}

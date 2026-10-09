//go:build windows

package app

func watchTerminalExit(func()) func() { return func() {} }

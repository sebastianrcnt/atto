//go:build windows

package tui

import "os"

type processOutput struct{}

func (*processOutput) start(*os.File)               {}
func (*processOutput) interrupt()                   {}
func (*processOutput) close()                       {}
func (*processOutput) write(out *os.File, s string) { _, _ = out.WriteString(s) }

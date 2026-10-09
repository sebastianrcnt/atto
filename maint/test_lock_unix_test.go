//go:build !windows

package maint

import (
	"golang.org/x/sys/unix"
	"os"
)

func holdTestLock(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) }
func releaseTestLock(f *os.File)    { unix.Flock(int(f.Fd()), unix.LOCK_UN) }

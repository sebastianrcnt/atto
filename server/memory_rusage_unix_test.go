//go:build !race && (darwin || linux)

package server

import (
	"runtime"
	"syscall"
)

func maxRSS() uint64 {
	var usage syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &usage) != nil {
		return 0
	}
	n := uint64(usage.Maxrss)
	if runtime.GOOS == "linux" {
		n *= 1024
	}
	return n
}

//go:build !windows

package cli

import (
	"os"
	"syscall"
)

// execSelf replaces this process with exe (argv[0] first).
func execSelf(exe string, argv []string) error {
	return syscall.Exec(exe, argv, os.Environ())
}

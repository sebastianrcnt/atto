//go:build !windows

package fsutil

import "os"

func renameAtomic(old, new string) error { return os.Rename(old, new) }

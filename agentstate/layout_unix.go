//go:build !windows

package agentstate

import "os"

func stateAlias(target, link string) error { return os.Symlink(target, link) }

func openStateGuard(path string) (*os.File, error) { return os.OpenFile(path, os.O_RDWR, 0) }

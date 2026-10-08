//go:build !windows

package agentstate

import "os"

func stateAlias(target, link string) error { return os.Symlink(target, link) }

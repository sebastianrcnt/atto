//go:build !unix && !windows

package outputs

import "errors"

func diskFree(string) (uint64, error) { return 0, errors.New("free space is unknown on this system") }

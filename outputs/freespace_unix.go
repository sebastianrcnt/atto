//go:build unix

package outputs

import "syscall"

// diskFree is the space available to unprivileged writers on the file
// system holding dir.
func diskFree(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

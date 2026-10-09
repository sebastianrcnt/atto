//go:build !windows

package session

import "os"

func openSessionFile(path string) (*os.File, error) { return os.Open(path) }
func archiveRename(from, to string) error           { return os.Rename(from, to) }
func archiveRemove(path string) error               { return os.Remove(path) }
func syncSessionDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

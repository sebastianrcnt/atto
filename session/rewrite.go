package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// RewriteHeader changes the header line of the session file at path with
// edit, leaving every other line as it is. It holds the session's lease
// meanwhile (an error if a process writes the session) and replaces the
// file atomically. Archives that are compressed cannot be rewritten.
func RewriteHeader(path string, edit func(*Entry)) error {
	if strings.HasSuffix(path, ".zst") {
		return fmt.Errorf("%s is a compressed archive", path)
	}
	release, err := Lock(path)
	if err != nil {
		return err
	}
	defer release()
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	r := bufio.NewReaderSize(src, 64*1024)
	first, err := r.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	var h Entry
	if err := json.Unmarshal(first, &h); err != nil || h.Type != TypeSession {
		return fmt.Errorf("%s: not an atto session", path)
	}
	edit(&h)
	line, err := json.Marshal(h)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".header-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	w := bufio.NewWriter(tmp)
	_, _ = w.Write(append(line, '\n'))
	if _, err := io.Copy(w, r); err != nil {
		tmp.Close()
		return err
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return err
	}
	// Windows cannot replace a file that is still open.
	if err := src.Close(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if info, err := os.Stat(path); err == nil {
		_ = os.Chmod(tmp.Name(), info.Mode().Perm())
	}
	return os.Rename(tmp.Name(), path)
}

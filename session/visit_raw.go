package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
)

// visitRaw streams validated JSONL without decoding payload fields a caller
// does not need. Plain-file long lines use one offset read, not growth copies.
func visitRaw(path string, visit func(int, string, string, []byte) error) error {
	f, err := Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64*1024)
	var off int64
	next := func() ([]byte, error) {
		if sr := f.(*sessionReader); sr.decoder == nil {
			return readLineAt(sr.file, r, off)
		}
		return readStreamLine(r)
	}
	line, err := next()
	var header Entry
	if len(line) == 0 {
		return fmt.Errorf("%s: missing session header", path)
	}
	if e := json.Unmarshal(line, &header); e != nil {
		return fmt.Errorf("%s: invalid session header: %w", path, e)
	}
	if header.Type != TypeSession {
		return fmt.Errorf("%s: not an atto session", path)
	}
	if err != nil && err != io.EOF {
		return err
	}
	off += int64(len(line))
	n, prev := 0, ""
	for {
		line, err := next()
		if err != nil && err != io.EOF {
			return err
		}
		var meta activeLine
		if len(line) > 0 && json.Unmarshal(line, &meta) == nil {
			n++
			if meta.ID == "" {
				meta.ID, meta.Parent = "#"+strconv.Itoa(n), prev
			}
			prev = meta.ID
			if err := visit(n, meta.ID, meta.Parent, line); err != nil {
				return err
			}
		}
		off += int64(len(line))
		if err == io.EOF {
			return nil
		}
	}
}

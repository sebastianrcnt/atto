package session

import (
	"bufio"
	"encoding/json"
	"io"
)

// readSelected reads entries in the requested order. The metadata contains no
// message text. A compressed reader scans forward once instead of spooling an
// archive; only selected entries survive each iteration.
func readSelected(path string, nodes []summaryNode) ([]Entry, error) {
	var entries []Entry
	err := visitSelected(path, nodes, func(e Entry) error { entries = append(entries, e); return nil })
	return entries, err
}

func visitSelected(path string, nodes []summaryNode, visit func(Entry) error) error {
	if len(nodes) == 0 {
		return nil
	}
	f, err := Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	decode := func(n summaryNode, line []byte) error {
		var e Entry
		if json.Unmarshal(line, &e) != nil {
			return nil
		}
		e.ID, e.Parent = n.ID, n.Parent
		return visit(e)
	}
	if sr := f.(*sessionReader); sr.decoder == nil {
		for _, n := range nodes {
			line := make([]byte, n.Len)
			if _, err := sr.file.ReadAt(line, n.Off); err != nil && err != io.EOF {
				return err
			}
			if err := decode(n, line); err != nil {
				return err
			}
		}
		return nil
	}
	// Well-formed append-only paths have increasing offsets. Handle malformed
	// forward-parent paths without retaining all decoded strings: restart the
	// stream only when the requested position goes backwards.
	r := bufio.NewReaderSize(f, 64*1024)
	var off int64
	for _, n := range nodes {
		if n.Off < off {
			if err := f.Close(); err != nil {
				return err
			}
			f, err = Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			r.Reset(f)
			off = 0
		}
		for off <= n.Off {
			line, err := readStreamLine(r)
			if err != nil && err != io.EOF {
				return err
			}
			pos := off
			off += int64(len(line))
			if pos == n.Off {
				if err := decode(n, line); err != nil {
					return err
				}
				break
			}
			if err == io.EOF {
				return io.ErrUnexpectedEOF
			}
		}
	}
	return nil
}

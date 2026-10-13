package jobs

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// Tail returns the last n lines of a job's output.
func Tail(session string, id, n int) (string, error) {
	if n <= 0 {
		return "", nil
	}
	data, err := readTail(OutputPath(session, id), 256<<10)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimRight(data, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n"), nil
}

// Head returns the first n lines of a job's output.
func Head(session string, id, n int) (string, error) {
	f, err := os.Open(OutputPath(session, id))
	if err != nil {
		return "", err
	}
	defer f.Close()
	var b strings.Builder
	r := bufio.NewReader(f)
	for range n {
		line, err := r.ReadString('\n')
		b.WriteString(line)
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func readTail(path string, limit int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	off := max(0, st.Size()-limit)
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return "", err
	}
	data, err := io.ReadAll(f)
	s := string(data)
	if off > 0 {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
	}
	return s, err
}

// cappedFile writes to a file until maxLog, then drops the rest.
type cappedFile struct {
	mu      sync.Mutex
	f       *os.File
	n       int64
	noticed bool
}

func (c *cappedFile) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	room := maxLog - c.n
	if room > 0 {
		w, err := c.f.Write(p[:min(int64(len(p)), room)])
		c.n += int64(w)
		if err != nil {
			return len(p), err
		}
	}
	if int64(len(p)) > room && !c.noticed {
		c.noticed = true
		if _, err := c.f.WriteString(fmt.Sprintf("\n[atto: output beyond %d MiB dropped]\n", maxLog>>20)); err != nil {
			return len(p), err
		}
	}
	return len(p), nil
}

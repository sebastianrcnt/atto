package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

type cachedSummary struct {
	size     int64
	modified time.Time
	summary  Summary
}

var summaryCache = struct {
	sync.Mutex
	paths map[string]cachedSummary
}{paths: make(map[string]cachedSummary)}

// listSummary caches disk metadata, but reads the writer lease each time.
func listSummary(path string) (Summary, error) {
	st, err := os.Stat(path)
	if err != nil {
		return Summary{}, err
	}
	summaryCache.Lock()
	cached, ok := summaryCache.paths[path]
	summaryCache.Unlock()
	if !ok || cached.size != st.Size() || !cached.modified.Equal(st.ModTime()) {
		h, err := summaryHeader(path)
		if err != nil {
			return Summary{}, err
		}
		s := Summary{Path: path, ID: h.ID, Cwd: h.Cwd, Created: h.Time, Updated: h.Time, AgentOf: h.AgentOf, External: h.External}
		if h.AgentOf == "" {
			s, err = summarize(path)
			if err != nil {
				return Summary{}, err
			}
		}
		s.Size, s.Running = st.Size(), 0
		cached = cachedSummary{size: st.Size(), modified: st.ModTime(), summary: s}
		summaryCache.Lock()
		summaryCache.paths[path] = cached
		summaryCache.Unlock()
	}
	s := cached.summary
	if l, ok := LockedBy(path); ok {
		s.Running = l.PID
	}
	return s, nil
}

func summaryHeader(path string) (Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return Entry{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var h Entry
	if !sc.Scan() {
		return h, fmt.Errorf("%s: missing session header", path)
	}
	if err := json.Unmarshal(sc.Bytes(), &h); err != nil {
		return h, err
	}
	if h.Type != TypeSession {
		return h, fmt.Errorf("%s: not an atto session", path)
	}
	return h, nil
}

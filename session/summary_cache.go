package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/fsutil"
)

// Session lists are built often (the agent center rescans every two
// seconds) over files that can reach tens of megabytes. A summary is
// therefore never built from Load, which keeps every entry and its text:
//
//   - each line is decoded into summaryLine, which keeps only the type, IDs,
//     time, name, model and message role; message text is skipped in
//     place, not copied;
//   - files only grow, so a changed file is read from where the last scan
//     stopped (summaryState.Offset), not from the start;
//   - the two texts a summary shows (the first user message and the last
//     assistant answer) are read back by offset, and only their start is
//     decoded;
//   - summaries of unchanged files are kept on disk (summaryDiskPath), so a
//     new atto process does not read every session again.

// previewMax bounds Preview and LastMessage, in runes.
const previewMax = 4096

// summaryReads counts files read for summaries (tests).
var summaryReads atomic.Int64

// summaryNode is one entry, as much of it as a summary needs.
type summaryNode struct {
	ID, Parent string
	Role       byte   // 'u' user message, 'a' assistant message with text, 't' assistant without text, 0 other
	Text       string // bounded text for a streaming compressed summary
	Off, Len   int64  // where its line starts, and its length
}

// summaryState is the incremental parse of one file.
type summaryState struct {
	Size     int64
	Modified time.Time
	Offset   int64 // bytes of complete lines parsed, header included
	Header   Entry
	Nodes    []summaryNode
	Name     string
	Model    string
	Updated  time.Time
	byID     map[string]int
	summary  Summary
	complete bool // nodes parsed (not just the header)
}

// summaryLine is what a summary decodes of a line. Fields it does not name
// (tool output, compaction replacements) are skipped without copying.
type summaryLine struct {
	Type     string    `json:"type"`
	Time     time.Time `json:"time"`
	ID       string    `json:"id"`
	Parent   string    `json:"parentId"`
	Name     string    `json:"name"`
	Provider string    `json:"provider"`
	Model    string    `json:"model"`
	Message  *struct {
		Role    string   `json:"role"`
		Content textFlag `json:"content"`
	} `json:"message"`
}

// textFlag records whether a JSON string has anything but whitespace,
// without keeping it.
type textFlag bool

// UnmarshalJSONFrom reads the value in place: Unmarshal would copy it
// first for UnmarshalJSON.
func (t *textFlag) UnmarshalJSONFrom(d *jsontext.Decoder) error {
	raw, err := d.ReadValue()
	if err != nil {
		return err
	}
	return t.UnmarshalJSON(raw)
}

func (t *textFlag) UnmarshalJSON(raw []byte) error {
	*t = false
	if len(raw) < 2 || raw[0] != '"' {
		return nil
	}
	for i := 1; i < len(raw)-1; i++ {
		switch c := raw[i]; c {
		case ' ', '\t', '\n', '\r':
		case '\\':
			if i+1 < len(raw)-1 {
				switch raw[i+1] {
				case 'n', 't', 'r':
					i++
					continue
				}
			}
			*t = true
			return nil
		default:
			*t = true
			return nil
		}
	}
	return nil
}

var summaryCache = struct {
	sync.Mutex
	paths map[string]*summaryState
	// disk holds summaries loaded from summaryDiskPath, used for files
	// that have not changed since; dirty says the file needs writing.
	disk      map[string]diskSummary
	diskLoad  sync.Once
	dirty     bool
	lastWrite time.Time
}{paths: make(map[string]*summaryState)}

// diskSummary is a summary kept on disk with the file state it describes.
type diskSummary struct {
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Complete bool      `json:"complete"`
	Summary  Summary   `json:"summary"`
}

// summaryDiskVersion changes when Summary or its computation changes.
const summaryDiskVersion = 2

func summaryDiskPath() string { return filepath.Join(config.Dir(), "cache", "session-summaries.json") }

// summaryDiskWait spaces out rewrites of the disk cache while sessions change.
const summaryDiskWait = 30 * time.Second

// listSummary caches disk metadata, but reads the writer lease each time.
func listSummary(path string) (Summary, error) {
	return listSummaryMode(path, false)
}

func listSummaryMode(path string, agents bool) (Summary, error) {
	st, err := os.Stat(path)
	if err != nil {
		return Summary{}, err
	}
	s, err := cachedSummaryOf(path, st, agents)
	if err != nil {
		return Summary{}, err
	}
	s.Running = 0
	if l, ok := LockedBy(path); ok {
		s.Running = l.PID
	}
	return s, nil
}

func cachedSummaryOf(path string, st os.FileInfo, agents bool) (Summary, error) {
	summaryCache.Lock()
	defer summaryCache.Unlock()
	state := summaryCache.paths[path]
	if state != nil && state.Size == st.Size() && state.Modified.Equal(st.ModTime()) && (state.complete || !agents) {
		return state.summary, nil
	}
	loadDiskSummaries()
	if state == nil {
		if d, ok := summaryCache.disk[path]; ok && d.Size == st.Size() && d.Modified.Equal(st.ModTime()) && (d.Complete || !agents) {
			return d.Summary, nil
		}
	}
	if state == nil || !state.appendedTo(path, st) {
		state = &summaryState{}
	}
	if err := state.update(path, st, agents); err != nil {
		delete(summaryCache.paths, path)
		return Summary{}, err
	}
	summaryCache.paths[path] = state
	summaryCache.disk[path] = diskSummary{Size: state.Size, Modified: state.Modified, Complete: state.complete, Summary: state.summary}
	summaryCache.dirty = true
	return state.summary, nil
}

// appendedTo reports whether path still begins with what s has read, so
// that only the rest needs reading: it is not shorter, and the last line
// read still ends where it did. A rewritten file is read from the start.
func (s *summaryState) appendedTo(path string, st os.FileInfo) bool {
	if strings.HasSuffix(path, ".zst") || s.Offset == 0 || st.Size() < s.Offset {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	b := make([]byte, 1)
	if _, err := f.ReadAt(b, s.Offset-1); err != nil || b[0] != '\n' {
		return false
	}
	h, _, err := readHeader(path, f)
	return err == nil && h.ID == s.Header.ID
}

// update reads what was appended to path since the last call and
// recomputes the summary. Without agents, an agent session's header is
// all that is read, as listings leave those out.
func (s *summaryState) update(path string, st os.FileInfo, agents bool) error {
	summaryReads.Add(1)
	stream, err := Open(path)
	if err != nil {
		return err
	}
	defer stream.Close()
	sr := stream.(*sessionReader)
	if sr.decoder != nil {
		return s.updateCompressed(path, st, stream, agents)
	}
	f := sr.file
	if s.Offset == 0 {
		h, n, err := readHeader(path, f)
		if err != nil {
			return err
		}
		s.Header, s.Updated, s.Offset = h, h.Time, n
		s.byID = map[string]int{}
	}
	s.Size, s.Modified = st.Size(), st.ModTime()
	if s.Header.IsAgent() && !agents && !s.complete {
		s.summary = Summary{Path: path, ID: s.Header.ID, Cwd: s.Header.Cwd, Created: s.Header.Time, Updated: s.Header.Time,
			AgentOf: s.Header.AgentOf, Agent: s.Header.Agent, External: s.Header.External, Size: st.Size()}
		return nil
	}
	if _, err := f.Seek(s.Offset, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReaderSize(f, 64*1024)
	var tail *summaryNode // an unterminated last line: counted, not consumed
	var tailLine summaryLine
	for off := s.Offset; ; {
		line, err := readLineAt(f, r, off)
		if len(line) > 0 {
			var l summaryLine
			if json.Unmarshal(line, &l) == nil {
				if err == nil {
					s.add(l, off, int64(len(line)))
				} else {
					tail, tailLine = &summaryNode{Off: off, Len: int64(len(line))}, l
				}
			}
			if err == nil {
				off += int64(len(line))
				s.Offset = off
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	s.complete = true
	if tail != nil {
		// Summarize as if the line were complete, but read it again next
		// time: it may still be being written.
		cp := &summaryState{Header: s.Header, Size: s.Size, Nodes: slices.Clip(s.Nodes), byID: maps.Clone(s.byID), Name: s.Name, Model: s.Model, Updated: s.Updated}
		cp.add(tailLine, tail.Off, tail.Len)
		s.summary = cp.summarize(path, f)
		return nil
	}
	s.summary = s.summarize(path, f)
	return nil
}

// Compressed archives are immutable: cache by compressed path/mtime/size,
// and stream once on a cache miss. Retain only bounded previews, not tool text
// or the decompressed transcript, and stop after the header for hidden agents.
func (s *summaryState) updateCompressed(path string, st os.FileInfo, stream io.Reader, agents bool) error {
	*s = summaryState{Size: st.Size(), Modified: st.ModTime(), byID: map[string]int{}}
	r := bufio.NewReaderSize(stream, 64*1024)
	line, err := r.ReadBytes('\n')
	if err != nil && err != io.EOF {
		return err
	}
	if err := json.Unmarshal(line, &s.Header); err != nil {
		return fmt.Errorf("%s: invalid session header: %w", path, err)
	}
	if s.Header.Type != TypeSession {
		return fmt.Errorf("%s: not an atto session", path)
	}
	s.Updated = s.Header.Time
	if s.Header.IsAgent() && !agents {
		s.summary = s.summarize(path, nil)
		return nil
	}
	for {
		line, err = r.ReadBytes('\n')
		if err != nil && err != io.EOF {
			return err
		}
		if len(line) > 0 {
			var l summaryLine
			if json.Unmarshal(line, &l) == nil {
				s.add(l, 0, 0)
				n := &s.Nodes[len(s.Nodes)-1]
				if n.Role == 'u' || n.Role == 'a' {
					n.Text = messageText(line, n.Role == 'a')
				}
			}
		}
		if err == io.EOF {
			break
		}
	}
	s.complete = true
	s.summary = s.summarize(path, nil)
	return nil
}

// readHeader reads and checks the first line; n is its length.
func readHeader(path string, f *os.File) (h Entry, n int64, err error) {
	line, err := readLineAt(f, bufio.NewReader(io.NewSectionReader(f, 0, 1<<62)), 0)
	if len(line) == 0 {
		if err != nil && err != io.EOF {
			return Entry{}, 0, err
		}
		return Entry{}, 0, fmt.Errorf("%s: missing session header", path)
	}
	if err := json.Unmarshal(line, &h); err != nil {
		return Entry{}, 0, fmt.Errorf("%s: invalid session header: %w", path, err)
	}
	if h.Type != TypeSession {
		return Entry{}, 0, fmt.Errorf("%s: not an atto session", path)
	}
	return h, int64(len(line)), nil
}

// readLineAt returns the next line of r, which reads f from off, with its
// newline; at the end of the file a last line without one comes with
// io.EOF. The line is valid until the next read. A line longer than r's
// buffer is measured first and then read from f at once, into one
// allocation of its size.
func readLineAt(f *os.File, r *bufio.Reader, off int64) ([]byte, error) {
	line, err := r.ReadSlice('\n')
	if !errors.Is(err, bufio.ErrBufferFull) {
		return line, err
	}
	n := len(line)
	for errors.Is(err, bufio.ErrBufferFull) {
		line, err = r.ReadSlice('\n')
		n += len(line)
	}
	long := make([]byte, n)
	if _, rerr := f.ReadAt(long, off); rerr != nil && rerr != io.EOF {
		return nil, rerr
	}
	return long, err
}

// add records one entry, linking ID-less entries as link does.
func (s *summaryState) add(l summaryLine, off, length int64) {
	n := summaryNode{ID: l.ID, Parent: l.Parent, Off: off, Len: length}
	if n.ID == "" {
		n.ID = "#" + strconv.Itoa(len(s.Nodes)+1)
		if len(s.Nodes) > 0 {
			n.Parent = s.Nodes[len(s.Nodes)-1].ID
		}
	}
	if l.Type == TypeMessage && l.Message != nil {
		switch l.Message.Role {
		case "user":
			n.Role = 'u'
		case "assistant":
			n.Role = 't'
			if l.Message.Content {
				n.Role = 'a'
			}
		}
	}
	if _, ok := s.byID[n.ID]; !ok {
		s.byID[n.ID] = len(s.Nodes)
	}
	s.Nodes = append(s.Nodes, n)
	s.Updated = l.Time
	switch l.Type {
	case TypeName:
		s.Name = l.Name
	case TypeModel:
		s.Model = l.Provider + "/" + l.Model
	}
}

// summarize computes the summary from the nodes, reading the two texts it
// shows back from f.
func (s *summaryState) summarize(path string, f *os.File) Summary {
	h := s.Header
	sum := Summary{Path: path, ID: h.ID, Cwd: h.Cwd, Created: h.Time, Updated: s.Updated, Branch: h.GitBranch,
		AgentOf: h.AgentOf, Agent: h.Agent, External: h.External, Size: s.Size, Name: s.Name, Model: s.Model}
	// The active branch, from the last entry back to its root.
	var active []int
	seen := map[string]bool{}
	if len(s.Nodes) > 0 {
		for id := s.Nodes[len(s.Nodes)-1].ID; id != "" && !seen[id]; {
			i, ok := s.byID[id]
			if !ok {
				break
			}
			seen[id] = true
			active = append(active, i)
			id = s.Nodes[i].Parent
		}
	}
	firstUser, lastText := -1, -1
	for _, i := range slices.Backward(active) { // root first
		switch s.Nodes[i].Role {
		case 'u':
			sum.Messages++
			if firstUser < 0 {
				firstUser = i
			}
		case 'a':
			sum.Messages++
			lastText = i
		case 't':
			sum.Messages++
		}
	}
	if firstUser < 0 { // a session that went back to its start: any branch's
		firstUser = slices.IndexFunc(s.Nodes, func(n summaryNode) bool { return n.Role == 'u' })
	}
	if firstUser >= 0 {
		sum.Preview = messageTextAt(f, s.Nodes[firstUser], false)
	}
	if lastText >= 0 {
		sum.LastMessage = messageTextAt(f, s.Nodes[lastText], true)
	}
	return sum
}

// messageTextAt reads the message text of n's line, cut to previewMax runes
// (trimmed first with trim). The line is read once and only the start of
// the text is decoded, so a huge message is not copied again.
func messageTextAt(f *os.File, n summaryNode, trim bool) string {
	if f == nil {
		return n.Text
	}
	line := make([]byte, n.Len)
	if _, err := f.ReadAt(line, n.Off); err != nil && err != io.EOF {
		return ""
	}
	return messageText(line, trim)
}

func messageText(line []byte, trim bool) string {
	d := jsontext.NewDecoder(bytes.NewBuffer(line)) // decoded in place, not copied
	if !member(d, "message") || !member(d, "content") {
		return ""
	}
	raw, err := d.ReadValue()
	if err != nil || raw.Kind() != '"' {
		return ""
	}
	text := unquotePrefix(raw, 2*previewMax*utf8.UTFMax)
	if trim {
		text = strings.TrimSpace(text)
	}
	return clipRunes(text, previewMax)
}

// member enters the object d is at and reads up to the value of member
// name, skipping the others.
func member(d *jsontext.Decoder, name string) bool {
	if d.PeekKind() != '{' {
		return false
	}
	if _, err := d.ReadToken(); err != nil {
		return false
	}
	for d.PeekKind() == '"' {
		tok, err := d.ReadToken()
		if err != nil {
			return false
		}
		if tok.String() == name {
			return true
		}
		if d.SkipValue() != nil {
			return false
		}
	}
	return false
}

// unquotePrefix decodes the JSON string raw, or only about its first max
// bytes when it is longer: the cut is moved back off an escape sequence or
// a partial UTF-8 sequence, and the string closed there.
func unquotePrefix(raw jsontext.Value, max int) string {
	var s string
	if len(raw) <= max {
		_ = json.Unmarshal(raw, &s)
		return s
	}
	safe := 1
	for i := 1; i < max; {
		switch {
		case raw[i] == '\\' && raw[i+1] == 'u':
			i += 6
		case raw[i] == '\\':
			i += 2
		default:
			i++
		}
		if i <= max {
			safe = i
		}
	}
	for safe > 1 && !utf8.RuneStart(raw[safe]) {
		safe--
	}
	for range 2 { // a cut between the halves of a surrogate pair fails once
		if json.Unmarshal(append(slices.Clip(raw[:safe]), '"'), &s) == nil {
			return s
		}
		if safe -= 6; safe < 1 {
			break
		}
	}
	return ""
}

// clipRunes returns at most n runes of s, without converting all of s.
func clipRunes(s string, n int) string {
	if len(s) > n*utf8.UTFMax {
		s = strings.ToValidUTF8(s[:n*utf8.UTFMax], "")
	}
	r := []rune(s)
	return string(r[:min(len(r), n)])
}

// loadDiskSummaries reads the disk cache once per process. Callers hold
// summaryCache's lock.
func loadDiskSummaries() {
	summaryCache.diskLoad.Do(func() {
		summaryCache.disk = map[string]diskSummary{}
		b, err := fsutil.ReadFile(summaryDiskPath())
		if err != nil {
			return
		}
		var d struct {
			Version  int                    `json:"version"`
			Sessions map[string]diskSummary `json:"sessions"`
		}
		if json.Unmarshal(b, &d) == nil && d.Version == summaryDiskVersion && d.Sessions != nil {
			summaryCache.disk = d.Sessions
		}
	})
}

// saveDiskSummaries writes the disk cache when it changed, at most every
// summaryDiskWait unless force.
func saveDiskSummaries(force bool) {
	summaryCache.Lock()
	if !summaryCache.dirty || (!force && time.Since(summaryCache.lastWrite) < summaryDiskWait) {
		summaryCache.Unlock()
		return
	}
	b, err := json.Marshal(struct {
		Version  int                    `json:"version"`
		Sessions map[string]diskSummary `json:"sessions"`
	}{summaryDiskVersion, summaryCache.disk})
	summaryCache.dirty, summaryCache.lastWrite = false, time.Now()
	summaryCache.Unlock()
	if err != nil {
		return
	}
	path := summaryDiskPath()
	if os.MkdirAll(filepath.Dir(path), 0o700) == nil {
		_ = fsutil.WriteAtomic(path, b, 0o600)
	}
}

// forgetSummaries drops cached summaries of files under root that a scan
// did not see.
func forgetSummaries(root string, seen map[string]bool) {
	summaryCache.Lock()
	defer summaryCache.Unlock()
	prefix := root + string(filepath.Separator)
	for path := range summaryCache.paths {
		if strings.HasPrefix(path, prefix) && !seen[path] {
			delete(summaryCache.paths, path)
		}
	}
	loadDiskSummaries()
	for path := range summaryCache.disk {
		if strings.HasPrefix(path, prefix) && !seen[path] {
			delete(summaryCache.disk, path)
			summaryCache.dirty = true
		}
	}
}

// Package outputs keeps the full output of shell commands on disk.
//
// A command's output goes through a Writer. The Writer holds in memory only
// what the model and the screen need (the start and the end of the output,
// Snapshot) and, once the output outgrows that, streams everything to a
// zstd-compressed file under ~/.atto/outputs/<session>/. The file is capped:
// it keeps the first FileHead bytes and the last FileTail bytes, with a
// marker line for the middle. The directory as a whole is capped too, and
// nothing is written when the disk is nearly full.
package outputs

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/fsutil"
)

// ErrLowDisk means the output was not saved because the disk has less free
// space than Limits.MinFree.
var ErrLowDisk = errors.New("low disk space")

// Options say what a Writer is for.
type Options struct {
	// Session and Name place the file: <session>/<name>.log.zst. Name is
	// usually a tool call's id; empty is a unique name.
	Session, Name string
	// Keep is how many bytes of the start and of the end of the output
	// Snapshot holds. Output of at most Keep bytes is never written to disk.
	Keep int
	// Limits override CurrentLimits (tests).
	Limits *Limits
}

// Snapshot is what a Writer remembers of the output, with CRLF turned to
// LF: its first and last Keep bytes (all of it if short) and its size.
type Snapshot struct {
	Head, Tail string
	Bytes      int64
	Lines      int64 // line feeds
}

// Saved is a file written by Save.
type Saved struct {
	// Path is the file, "" if the output was short enough to need none.
	Path string
	// Omitted is how many bytes of the output's middle the file leaves out.
	Omitted int64
}

// Writer is an io.Writer for one command's output. It is safe for
// concurrent use. Call Save or Discard when the output is complete.
type Writer struct {
	mu   sync.Mutex
	opt  Options
	lim  Limits
	keep int

	head, tail []byte // what Snapshot returns
	bytes      int64
	lines      int64
	cr         bool // the last byte was a CR, which a LF may still absorb

	raw    int64
	pre    []byte // raw bytes seen before there is a file
	file   *sink
	noFile error // why there is no file
	closed bool
	saved  Saved
	failed error
}

// New returns a Writer for a command's output.
func New(opt Options) *Writer {
	w := &Writer{opt: opt, keep: max(opt.Keep, 1)}
	if opt.Limits != nil {
		w.lim = opt.Limits.normalized()
	} else {
		w.lim = CurrentLimits()
	}
	return w
}

// Write takes output. It never fails: when the file cannot be written the
// Writer goes on remembering the start and the end, and Save says why there
// is no file.
func (w *Writer) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.remember(p)
	if !w.closed {
		w.toFile(p)
	}
	return len(p), nil
}

func (w *Writer) remember(p []byte) {
	if w.cr || bytes.IndexByte(p, '\r') >= 0 {
		p = w.normalize(p)
	}
	w.add(p)
}

// add records p, which is already normalized.
func (w *Writer) add(p []byte) {
	w.bytes += int64(len(p))
	w.lines += int64(bytes.Count(p, []byte{'\n'}))
	if room := w.keep - len(w.head); room > 0 {
		w.head = append(w.head, p[:min(room, len(p))]...)
	}
	if len(p) >= w.keep {
		w.tail = append(w.tail[:0], p[len(p)-w.keep:]...)
		return
	}
	w.tail = append(w.tail, p...)
	if len(w.tail) > 2*w.keep {
		n := copy(w.tail, w.tail[len(w.tail)-w.keep:])
		w.tail = w.tail[:n]
	}
}

// normalize replaces CRLF by LF, holding back a CR that ends p.
func (w *Writer) normalize(p []byte) []byte {
	out := make([]byte, 0, len(p)+1)
	if w.cr {
		w.cr = false
		if p[0] != '\n' {
			out = append(out, '\r')
		}
	}
	for i, c := range p {
		if c == '\r' {
			if i == len(p)-1 {
				w.cr = true
				continue
			}
			if p[i+1] == '\n' {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

// toFile adds p to the file, starting one when the output outgrows Keep.
func (w *Writer) toFile(p []byte) {
	if w.file == nil {
		if w.noFile != nil {
			return
		}
		if w.raw+int64(len(p)) <= int64(w.keep) {
			w.raw += int64(len(p))
			w.pre = append(w.pre, p...)
			return
		}
		s, err := w.open()
		if err != nil {
			w.noFile, w.pre = err, nil
			return
		}
		w.file = s
		err = s.write(w.pre)
		w.pre = nil
		if err != nil {
			w.fail(err)
			return
		}
	}
	w.raw += int64(len(p))
	if err := w.file.write(p); err != nil {
		w.fail(err)
	}
}

func (w *Writer) fail(err error) {
	w.file.abort()
	w.file, w.noFile = nil, err
}

// open starts the file, if the disk has room for it.
func (w *Writer) open() (*sink, error) {
	dir := SessionDir(w.opt.Session)
	if err := fsutil.PrivateDirs(config.Dir(), dir); err != nil {
		return nil, err
	}
	if w.lim.MinFree > 0 {
		if free, err := freeSpace(dir); err == nil && free < uint64(w.lim.MinFree) {
			return nil, ErrLowDisk
		}
	}
	enforceBudget(w.lim, false)
	name := safeName(w.opt.Name)
	for range 8 {
		if name == "" {
			name = uniqueName()
		}
		s, err := newSink(dir, name, w.lim)
		if err == nil {
			return s, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		name = name + "-" + randHex()
	}
	return nil, errors.New("no free name for the output file")
}

func randHex() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func uniqueName() string { return time.Now().Format("20060102-150405") + "-" + randHex() }

// Snapshot is the start and the end of the output so far.
func (w *Writer) Snapshot() Snapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cr {
		w.cr = false
		w.add([]byte{'\r'})
	}
	return Snapshot{Head: string(w.head), Tail: string(w.tail[max(0, len(w.tail)-w.keep):]), Bytes: w.bytes, Lines: w.lines}
}

// Save completes the file. A Saved with no Path and no error means the
// output was short and nothing was written. Calling it again returns the
// same result; Write after Save is remembered but not saved.
func (w *Writer) Save() (Saved, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.saved, w.failed
	}
	w.closed, w.pre = true, nil
	switch {
	case w.file != nil:
		path, omitted, err := w.file.finish()
		if err != nil {
			w.file.abort()
			w.failed = err
		} else {
			w.saved = Saved{Path: path, Omitted: omitted}
		}
		w.file = nil
	case w.noFile != nil:
		w.failed = w.noFile
	}
	return w.saved, w.failed
}

// Discard drops the file, if there is one: the output is not needed.
func (w *Writer) Discard() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.closed, w.pre = true, nil
	if w.file != nil {
		w.file.abort()
		w.file = nil
	}
}

// ringBuf is how much of the end of the output is buffered before it is
// written to the ring file.
const ringBuf = 64 << 10

// sink writes the file: the first FileHead bytes straight into a zstd
// stream, then the last FileTail bytes of the rest kept in a ring file that
// is appended to the stream when the output is complete.
type sink struct {
	final, part, ringPath string
	f                     *os.File
	enc                   *zstd.Encoder
	headLeft, tailCap     int64
	lastHead              byte

	ring   *os.File
	rb     []byte
	ringN  int64 // bytes written to the ring over time
	ringNL int64
}

func newSink(dir, name string, lim Limits) (*sink, error) {
	final := filepath.Join(dir, name+".log.zst")
	if _, err := os.Lstat(final); err == nil {
		return nil, os.ErrExist
	}
	s := &sink{final: final, part: final + ".part", ringPath: filepath.Join(dir, name+".log.tail"),
		headLeft: lim.FileHead, tailCap: lim.FileTail}
	f, err := os.OpenFile(s.part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if err := fsutil.PrivateFile(f); err != nil {
		f.Close()
		os.Remove(s.part)
		return nil, err
	}
	// A small window and one thread keep the encoder to about half a megabyte.
	enc, err := zstd.NewWriter(f, zstd.WithEncoderConcurrency(1), zstd.WithWindowSize(128<<10),
		zstd.WithLowerEncoderMem(true), zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		f.Close()
		os.Remove(s.part)
		return nil, err
	}
	s.f, s.enc = f, enc
	setActive(s.part)
	setActive(s.ringPath)
	return s, nil
}

func (s *sink) write(p []byte) error {
	if s.headLeft > 0 && len(p) > 0 {
		n := int(min(int64(len(p)), s.headLeft))
		if _, err := s.enc.Write(p[:n]); err != nil {
			return err
		}
		s.headLeft -= int64(n)
		s.lastHead = p[n-1]
		p = p[n:]
	}
	for len(p) > 0 {
		if s.ring == nil {
			f, err := os.OpenFile(s.ringPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			if err := fsutil.PrivateFile(f); err != nil {
				f.Close()
				os.Remove(s.ringPath)
				return err
			}
			s.ring = f
			s.rb = make([]byte, 0, ringBuf)
		}
		n := min(len(p), ringBuf-len(s.rb))
		s.rb = append(s.rb, p[:n]...)
		s.ringN += int64(n)
		s.ringNL += int64(bytes.Count(p[:n], []byte{'\n'}))
		p = p[n:]
		if len(s.rb) == ringBuf {
			if err := s.flushRing(); err != nil {
				return err
			}
		}
	}
	return nil
}

// flushRing writes the buffered bytes at their place in the ring.
func (s *sink) flushRing() error {
	data := s.rb
	off := (s.ringN - int64(len(data))) % s.tailCap
	for len(data) > 0 {
		n := int(min(int64(len(data)), s.tailCap-off))
		if _, err := s.ring.WriteAt(data[:n], off); err != nil {
			return err
		}
		data = data[n:]
		off = (off + int64(n)) % s.tailCap
	}
	s.rb = s.rb[:0]
	return nil
}

// eachTail calls fn with the kept end of the output, oldest bytes first.
func (s *sink) eachTail(fn func([]byte) error) error {
	kept := min(s.ringN, s.tailCap)
	off := int64(0)
	if s.ringN > s.tailCap {
		off = s.ringN % s.tailCap
	}
	buf := make([]byte, ringBuf)
	for kept > 0 {
		n := min(kept, int64(len(buf)), s.tailCap-off)
		if _, err := s.ring.ReadAt(buf[:n], off); err != nil {
			return err
		}
		if err := fn(buf[:n]); err != nil {
			return err
		}
		kept -= n
		off = (off + n) % s.tailCap
	}
	return nil
}

// finish appends the end of the output to the stream and publishes the
// file under its final name.
func (s *sink) finish() (path string, omitted int64, err error) {
	if s.ring != nil {
		if err = s.flushRing(); err != nil {
			return
		}
		omitted = s.ringN - min(s.ringN, s.tailCap)
		if omitted > 0 {
			var keptNL int64
			if err = s.eachTail(func(b []byte) error { keptNL += int64(bytes.Count(b, []byte{'\n'})); return nil }); err != nil {
				return
			}
			marker := fmt.Sprintf("[... %d bytes (%d lines) omitted ...]\n", omitted, s.ringNL-keptNL)
			if s.lastHead != '\n' {
				marker = "\n" + marker
			}
			if _, err = s.enc.Write([]byte(marker)); err != nil {
				return
			}
		}
		if err = s.eachTail(func(b []byte) error { _, err := s.enc.Write(b); return err }); err != nil {
			return
		}
	}
	if err = s.enc.Close(); err != nil {
		return
	}
	if err = s.f.Close(); err != nil {
		return
	}
	s.f = nil
	s.dropRing()
	if err = os.Rename(s.part, s.final); err != nil {
		return
	}
	clearActive(s.part)
	return s.final, omitted, nil
}

func (s *sink) dropRing() {
	if s.ring != nil {
		s.ring.Close()
		s.ring = nil
		os.Remove(s.ringPath)
	}
	clearActive(s.ringPath)
	s.ringPath = ""
}

// abort removes everything the sink wrote.
func (s *sink) abort() {
	_ = s.enc.Close()
	if s.f != nil {
		s.f.Close()
		s.f = nil
	}
	if s.ringPath != "" {
		s.dropRing()
	}
	os.Remove(s.part)
	clearActive(s.part)
}

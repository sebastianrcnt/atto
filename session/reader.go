package session

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// IsSessionFile recognizes both current compressed archives and legacy JSONL.
func IsSessionFile(path string) bool {
	return strings.HasSuffix(path, ".jsonl") || strings.HasSuffix(path, ".jsonl.zst")
}

// Open reads a session transparently, detecting zstd by suffix or frame magic.
// The caller must close the reader, including when it only reads a preview.
func Open(path string) (io.ReadCloser, error) {
	f, err := openSessionFile(path)
	if err != nil {
		return nil, err
	}
	r := bufio.NewReader(f)
	magic, _ := r.Peek(4)
	if !strings.HasSuffix(path, ".zst") && !bytes.Equal(magic, []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		return &sessionReader{Reader: r, file: f}, nil
	}
	d, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1))
	if err != nil {
		f.Close()
		return nil, err
	}
	return &sessionReader{Reader: d, file: f, decoder: d}, nil
}

type sessionReader struct {
	io.Reader
	file    *os.File
	decoder *zstd.Decoder
}

func (r *sessionReader) Close() error {
	if r.decoder != nil {
		r.decoder.Close()
	}
	return r.file.Close()
}

// openSeekable is used by the offset-based active-branch reader. Compressed
// transcripts are spooled to a private temporary file, not held in memory.
// Listing uses a streaming summary instead and never needs this spool.
func openSeekable(path string) (*os.File, func(), error) {
	r, err := Open(path)
	if err != nil {
		return nil, nil, err
	}
	sr := r.(*sessionReader)
	if sr.decoder == nil {
		return sr.file, func() { _ = r.Close() }, nil
	}
	defer r.Close()
	f, err := os.CreateTemp("", "atto-transcript-*")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = f.Close(); _ = os.Remove(f.Name()) }
	if _, err := io.Copy(f, r); err != nil {
		cleanup()
		return nil, nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, nil, err
	}
	return f, cleanup, nil
}

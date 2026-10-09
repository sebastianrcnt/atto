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

// readStreamLine returns one JSONL entry. Most lines borrow the reader's
// buffer; exceptionally long entries allocate only their own bytes, never the
// rest of the transcript. It works identically for plain and zstd streams.
func readStreamLine(r *bufio.Reader) ([]byte, error) {
	line, err := r.ReadSlice('\n')
	if err != bufio.ErrBufferFull {
		return line, err
	}
	out := append([]byte(nil), line...)
	for err == bufio.ErrBufferFull {
		line, err = r.ReadSlice('\n')
		out = append(out, line...)
	}
	return out, err
}

package outputs

import (
	"bytes"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
)

var zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}

type zreader struct {
	dec *zstd.Decoder
	f   *os.File
}

func (z *zreader) Read(p []byte) (int, error) { return z.dec.Read(p) }

func (z *zreader) Close() error {
	z.dec.Close()
	return z.f.Close()
}

// Open opens a saved output for reading as text: a file written here is
// decompressed on the fly, and any other file (the plain logs of older
// sessions) is read as it is.
func Open(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	var magic [4]byte
	n, _ := io.ReadFull(f, magic[:])
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	if n < len(magic) || !bytes.Equal(magic[:], zstdMagic) {
		return f, nil
	}
	dec, err := zstd.NewReader(f, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true))
	if err != nil {
		f.Close()
		return nil, err
	}
	return &zreader{dec, f}, nil
}

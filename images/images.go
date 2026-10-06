// Package images prepares images for the model and stores them for
// sessions. Images are kept as content-addressed files under
// ~/.atto/images/<sha256>.<ext>, so a session line holds only the file
// name and the bytes sent for an earlier message never change.
package images

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // decoder
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // decoder

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/fsutil"
	"github.com/sebastianrcnt/atto/provider"
)

// MaxDimension bounds the longer side of an image sent to the model; larger
// ones are downscaled (codex-rs utils/image: MAX_DIMENSION, ResizeToFit).
const MaxDimension = 2048

// MaxFileBytes guards against reading huge files that are not plausible
// prompt images.
const MaxFileBytes = 64 << 20

// MaxPixels bounds decoded memory before allocating the image.
const MaxPixels = 64_000_000

// Prepare decodes data and returns it ready to send: PNG, JPEG and WebP
// that fit MaxDimension pass through byte for byte; larger images are
// downscaled (JPEG stays JPEG, the rest become PNG) and other formats are
// re-encoded as PNG. As in codex, only the first frame of a GIF is kept.
func Prepare(data []byte) (provider.Image, error) {
	if len(data) > MaxFileBytes {
		return provider.Image{}, fmt.Errorf("image is too large (%d MB)", len(data)>>20)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return provider.Image{}, fmt.Errorf("not a supported image (PNG, JPEG, GIF or WebP): %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > MaxPixels/cfg.Height {
		return provider.Image{}, fmt.Errorf("image dimensions exceed %d pixels", MaxPixels)
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return provider.Image{}, fmt.Errorf("decoding image: %w", err)
	}
	fits := cfg.Width <= MaxDimension && cfg.Height <= MaxDimension
	mime := "image/" + format
	if fits && (format == "png" || format == "jpeg" || format == "webp") {
		return named(provider.Image{MIME: mime, Width: cfg.Width, Height: cfg.Height, Data: data}), nil
	}
	if !fits {
		src = resize(src, MaxDimension)
	}
	var buf bytes.Buffer
	if format == "jpeg" {
		err = jpeg.Encode(&buf, src, &jpeg.Options{Quality: 85})
	} else {
		mime = "image/png"
		err = png.Encode(&buf, src)
	}
	if err != nil {
		return provider.Image{}, fmt.Errorf("encoding image: %w", err)
	}
	b := src.Bounds()
	return named(provider.Image{MIME: mime, Width: b.Dx(), Height: b.Dy(), Data: buf.Bytes()}), nil
}

// fitSize scales w×h down so the longer side is at most limit.
func fitSize(w, h, limit int) (int, int) {
	if w <= limit && h <= limit {
		return w, h
	}
	if w >= h {
		return limit, max(1, (h*limit+w/2)/w)
	}
	return max(1, (w*limit+h/2)/h), limit
}

// resize downscales src to fit limit with bilinear filtering (codex uses
// a triangle filter, the same thing).
func resize(src image.Image, limit int) image.Image {
	b := src.Bounds()
	w, h := fitSize(b.Dx(), b.Dy(), limit)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.BiLinear.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	return dst
}

// named sets File to the content hash, so equal images share one file.
func named(im provider.Image) provider.Image {
	sum := sha256.Sum256(im.Data)
	ext := strings.TrimPrefix(im.MIME, "image/")
	if ext == "jpeg" {
		ext = "jpg"
	}
	im.File = hex.EncodeToString(sum[:]) + "." + ext
	return im
}

// Label describes an image for its placeholder, e.g. "1024x768 PNG".
func Label(im provider.Image) string {
	return fmt.Sprintf("%dx%d %s", im.Width, im.Height, strings.ToUpper(strings.TrimPrefix(im.MIME, "image/")))
}

// Dir is where image files are stored.
func Dir() string { return config.ImagesDir() }

// Save writes im's bytes to its content-addressed file. Existing files are
// left alone: same name, same bytes.
func Save(im provider.Image) error {
	if im.File == "" || len(im.Data) == 0 {
		return errors.New("image has no data")
	}
	path := filepath.Join(Dir(), filepath.Base(im.File))
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	// CreateTemp used 0600; keep that mode for image files.
	return fsutil.WriteAtomic(path, im.Data, 0o600)
}

// Load fills in im.Data from its file.
func Load(im provider.Image) (provider.Image, error) {
	data, err := os.ReadFile(filepath.Join(Dir(), filepath.Base(im.File)))
	if err != nil {
		return im, err
	}
	im.Data = data
	return im, nil
}

// ReadFile prepares the image at path.
func ReadFile(path string) (provider.Image, error) {
	st, err := os.Stat(path)
	if err != nil {
		return provider.Image{}, err
	}
	if !st.Mode().IsRegular() || st.Size() > MaxFileBytes {
		return provider.Image{}, fmt.Errorf("%s: not an image file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return provider.Image{}, err
	}
	return Prepare(data)
}

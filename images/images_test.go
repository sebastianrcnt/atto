package images

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/provider"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestPreparePassThroughAndResize(t *testing.T) {
	small := pngBytes(t, 30, 20)
	im, err := Prepare(small)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(im.Data, small) || im.MIME != "image/png" || im.Width != 30 || Label(im) != "30x20 PNG" {
		t.Fatalf("small image changed: %+v", im)
	}
	if !strings.HasSuffix(im.File, ".png") || len(im.File) != 64+4 {
		t.Fatalf("file name %q", im.File)
	}

	big, err := Prepare(pngBytes(t, 4096, 1024))
	if err != nil {
		t.Fatal(err)
	}
	if big.Width != MaxDimension || big.Height != 512 {
		t.Fatalf("resized to %dx%d", big.Width, big.Height)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(big.Data))
	if err != nil || cfg.Width != 2048 || cfg.Height != 512 {
		t.Fatalf("encoded %+v %v", cfg, err)
	}

	var jb bytes.Buffer
	_ = jpeg.Encode(&jb, image.NewRGBA(image.Rect(0, 0, 100, 3000)), nil)
	j, err := Prepare(jb.Bytes())
	if err != nil || j.MIME != "image/jpeg" || j.Width != 68 || j.Height != 2048 || !strings.HasSuffix(j.File, ".jpg") {
		t.Fatalf("jpeg: %+v %v", j, err)
	}

	if _, err := Prepare([]byte("not an image")); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestSaveLoad(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	im, _ := Prepare(pngBytes(t, 3, 3))
	if err := Save(im); err != nil {
		t.Fatal(err)
	}
	if err := Save(im); err != nil { // second save is a no-op
		t.Fatal(err)
	}
	ref := im
	ref.Data = nil
	got, err := Load(ref)
	if err != nil || !bytes.Equal(got.Data, im.Data) {
		t.Fatalf("load: %v", err)
	}
	if _, err := Load(provider.Image{File: "missing.png"}); err == nil {
		t.Fatal("missing file loaded")
	}
}

func TestPrepareRejectsInvalidAndOversizedImages(t *testing.T) {
	header := append([]byte(nil), pngBytes(t, 1, 1)[:33]...)
	if _, err := Prepare(header); err == nil || !strings.Contains(err.Error(), "decoding image") {
		t.Fatalf("incomplete image: %v", err)
	}
	binary.BigEndian.PutUint32(header[16:20], 100_000)
	binary.BigEndian.PutUint32(header[20:24], 100_000)
	binary.BigEndian.PutUint32(header[29:33], crc32.ChecksumIEEE(header[12:29]))
	if _, err := Prepare(header); err == nil || !strings.Contains(err.Error(), "pixels") {
		t.Fatalf("oversized image must be rejected before decode: %v", err)
	}
}

package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"golang.org/x/image/draw"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sebastianrcnt/atto/fsutil"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/outputs"
)

// resourceRequest is resolved on the lane, then read off it. Clients can
// only read resources already named by this thread's transcript, not paths
// of their own choosing. File discovery is rooted at the thread's cwd.
type resourceRequest struct {
	path    string
	mime    string
	kind    string
	preview bool
}

func threadFiles(ctx context.Context, root, query string, limit int) (any, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		return nil, invalid("limit must be at most 1000")
	}
	entries, truncated := fsutil.ListFiles(ctx, root, fsutil.ListOptions{})
	files := []map[string]any{}
	query = strings.ToLower(strings.ReplaceAll(query, `\`, "/"))
	for _, entry := range entries {
		if !strings.Contains(strings.ToLower(entry.Path), query) {
			continue
		}
		if len(files) == limit {
			truncated = true
			break
		}
		files = append(files, map[string]any{"path": entry.Path, "directory": entry.Dir})
	}
	return map[string]any{"files": files, "truncated": truncated}, ctx.Err()
}

func (t *thread) itemResource(p threadParams, kind string) (any, error) {
	return itemResource(t.snapshot().Items, p, kind)
}

func itemResource(items []Item, p threadParams, kind string) (any, error) {
	for _, it := range items {
		if it.ID != p.ItemID {
			continue
		}
		if kind == "image" {
			if p.Index == nil || *p.Index < 0 || *p.Index >= len(it.Images) {
				return nil, invalid("index must name an image of itemId")
			}
			im := it.Images[*p.Index]
			if !storedFile.MatchString(im.File) || filepath.Base(im.File) != im.File || strings.ContainsAny(im.File, `/\`) {
				return nil, invalid("image has no stored file")
			}
			return resourceRequest{path: filepath.Join(images.Dir(), im.File), mime: im.MIME, kind: kind, preview: p.Preview}, nil
		}
		if it.FullOutput == "" {
			return map[string]any{"output": it.Output, "truncated": it.Dropped > 0 || it.Truncated}, nil
		}
		return resourceRequest{path: it.FullOutput, kind: kind}, nil
	}
	return nil, invalid("itemId is not in this thread's transcript")
}

func readResource(r resourceRequest) (any, error) {
	var f io.ReadCloser
	var err error
	if r.kind == "image" {
		f, err = os.Open(r.path)
	} else {
		f, err = outputs.Open(r.path) // saved output is compressed
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	const limit = 10 << 20
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	truncated := len(b) > limit
	if truncated {
		b = b[:limit]
	}
	if r.kind == "image" {
		if truncated {
			return nil, invalid("stored image exceeds 10 MiB")
		}
		if r.preview {
			cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
			if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > images.MaxPixels/cfg.Height {
				return nil, invalid("invalid stored image dimensions")
			}
			src, _, err := image.Decode(bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			scale := min(1.0, min(600.0/float64(cfg.Width), 350.0/float64(cfg.Height)))
			dst := image.NewRGBA(image.Rect(0, 0, max(1, int(float64(cfg.Width)*scale)), max(1, int(float64(cfg.Height)*scale))))
			draw.BiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
			var out bytes.Buffer
			if err := png.Encode(&out, dst); err != nil {
				return nil, err
			}
			b, r.mime = out.Bytes(), "image/png"
		}
		return map[string]any{"mimeType": r.mime, "data": base64.StdEncoding.EncodeToString(b)}, nil
	}
	return map[string]any{"output": string(b), "truncated": truncated}, nil
}

package server

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/provider"
)

// Images sent with turn/start, as the TUI attaches them: each is checked,
// prepared (scaled to fit images.MaxDimension) and stored under
// ~/.atto/images by content hash, and its placeholder ("[image 1: 640x480
// PNG]") is added to the message text.

// ImageInput is one image in turn/start's "images": base64 data (a data:
// URL is accepted too) and its MIME type.
type ImageInput struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
	// File, instead of data, names an image already in the image store
	// (input/submit from a client on the same machine), with its size and
	// the name it was read from.
	File   string `json:"file,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	Name   string `json:"name,omitempty"`
}

const (
	// maxTurnImages bounds the images of one turn.
	maxTurnImages = 10
	// maxImageBytes bounds one image, decoded; requests are capped at 16 MB
	// by the transports anyway.
	maxImageBytes = 10 << 20
)

var imageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// turnImages validates and stores the images of a turn for model m.
func turnImages(in []ImageInput, m config.ModelRef) ([]provider.Image, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if !m.Model.Images() {
		return nil, invalid("%s", images.Unsupported(m.Model.DisplayName(), "the model picker, thread/setModel", config.ModelsPath()))
	}
	if len(in) > maxTurnImages {
		return nil, invalid("too many images (%d, at most %d)", len(in), maxTurnImages)
	}
	out := make([]provider.Image, 0, len(in))
	for i, x := range in {
		im, err := decodeImage(x)
		if err != nil {
			return nil, invalid("image %d: %v", i+1, err)
		}
		if err := images.Save(im); err != nil {
			return nil, fmt.Errorf("saving image %d: %w", i+1, err)
		}
		out = append(out, im)
	}
	return out, nil
}

func decodeImage(x ImageInput) (provider.Image, error) {
	data, mime := x.Data, strings.ToLower(strings.TrimSpace(x.MIMEType))
	if rest, ok := strings.CutPrefix(data, "data:"); ok { // data:image/png;base64,...
		head, body, found := strings.Cut(rest, ",")
		if !found || !strings.HasSuffix(head, ";base64") {
			return provider.Image{}, fmt.Errorf("data URL is not base64")
		}
		if mime == "" {
			mime = strings.ToLower(strings.TrimSuffix(head, ";base64"))
		}
		data = body
	}
	if !imageTypes[mime] {
		return provider.Image{}, fmt.Errorf("mimeType %q is not supported (image/png, image/jpeg, image/gif or image/webp)", x.MIMEType)
	}
	if base64.StdEncoding.DecodedLen(len(data)) > maxImageBytes+2 {
		return provider.Image{}, fmt.Errorf("larger than %d MB", maxImageBytes>>20)
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return provider.Image{}, fmt.Errorf("data is not base64: %v", err)
	}
	if len(raw) > maxImageBytes {
		return provider.Image{}, fmt.Errorf("larger than %d MB", maxImageBytes>>20)
	}
	if !images.Sniff(raw) {
		return provider.Image{}, fmt.Errorf("data is not a PNG, JPEG, GIF or WebP image")
	}
	return images.Prepare(raw)
}

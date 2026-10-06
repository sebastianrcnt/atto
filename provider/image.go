package provider

import "encoding/base64"

// Image is an image attached to a user message, or to a tool result by
// "atto view". Sessions store only the reference (File, MIME, size); the
// bytes live in a content-addressed file (package images) and are loaded
// into Data before a request. Because the file never changes, the data URL
// built from it is byte-identical on every request, which keeps the prefix
// cache warm.
type Image struct {
	File   string `json:"file"` // name in the images directory, e.g. "<sha256>.png"
	MIME   string `json:"mime"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	// Name is the file a tool result's image was read from ("atto view
	// shot.png"), for display; the model is not sent it.
	Name string `json:"name,omitempty"`

	Data []byte `json:"-"`
}

// ImageMissing is sent in place of an image whose file could not be read.
const ImageMissing = "[image unavailable: its file is missing]"

// base64Data returns the image bytes as base64, or "" when not loaded.
func (im Image) base64Data() string {
	if len(im.Data) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(im.Data)
}

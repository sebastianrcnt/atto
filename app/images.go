package app

import (
	"context"
	"fmt"
	"github.com/sebastianrcnt/atto/core"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/tui"
)

// Image input follows codex-rs: Ctrl+V (or Alt+V) attaches the clipboard
// image, and pasting the path of an image file (what a terminal sends
// when a file is dropped on it) attaches that file. Each image shows in the
// editor as "[image N: WxH PNG]"; deleting the placeholder drops it.

// imagesUnsupported warns that the current model takes no images.
func (a *App) imagesUnsupported() {
	m := a.model()
	a.add(&noticeBlock{
		text:  images.Unsupported(m.Model.DisplayName(), "/model", core.ShortPath(config.ModelsPath())),
		style: func(s string) string { return tui.FG(3, s) },
	})
}

// attach adds im to the editor.
func (a *App) attach(im provider.Image) {
	a.editor.AttachImage(images.Label(im), im)
}

// pasteClipboardImage reads the clipboard in the background: the platform
// tools can take a moment.
func (a *App) pasteClipboardImage() {
	if !a.model().Model.Images() {
		a.imagesUnsupported()
		return
	}
	read := a.clipboard
	go func() {
		im, err := read(context.Background())
		a.ui.Do(func() {
			if err != nil {
				a.errorNotice(fmt.Errorf("pasting image: %w", err))
				return
			}
			a.attach(im)
		})
	}()
}

// pasteImagePath is the editor's paste hook: a pasted path to an image file
// attaches the image instead of inserting the path. Without image support
// the path is pasted as text, as in codex.
func (a *App) pasteImagePath(text string) bool {
	path, ok := images.PastedPath(text)
	if !ok || !a.model().Model.Images() {
		return false
	}
	im, err := images.ReadFile(path)
	if err != nil {
		return false
	}
	a.attach(im)
	return true
}

func attachedImages(att []tui.Attachment) []provider.Image {
	var out []provider.Image
	for _, at := range att {
		if im, ok := at.Value.(provider.Image); ok {
			out = append(out, im)
		}
	}
	return out
}

package server

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/core"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/provider"
)

// ContextInfo is thread/context's result: what fills the context, for
// the terminal's /context. The breakdown is only known while no run goes
// on (the agent's messages change during one).
type ContextInfo struct {
	Loaded        core.Loaded `json:"loaded"`
	ContextTokens int         `json:"contextTokens"`
	ContextWindow int         `json:"contextWindow,omitempty"`
	CompactLimit  int         `json:"compactLimit,omitempty"`
	// Cap is the tier cap below the window (0: none), and PriceCap that
	// it is the model's price boundary rather than settings.json's.
	Cap         int              `json:"cap,omitempty"`
	PriceCap    bool             `json:"priceCap,omitempty"`
	LongContext bool             `json:"longContext,omitempty"`
	Breakdown   *agent.Breakdown `json:"breakdown,omitempty"`
	System      string           `json:"systemPrompt,omitempty"` // view system
	Usage       Usage            `json:"usage"`
	Busy        bool             `json:"busy"`
}

func (t *thread) contextInfo(view string) ContextInfo {
	m := t.model()
	c := ContextInfo{Loaded: core.Collect(t.agent, t.hookSrc, t.modelFrom, t.effortFrom), ContextTokens: t.ctx,
		ContextWindow: m.Model.ContextWindow, LongContext: t.agent.LongContext(), Usage: t.total, Busy: t.turns.Busy}
	c.CompactLimit, c.Cap = t.agent.CompactionLimit()
	c.PriceCap = c.Cap > 0 && c.Cap == m.Model.Cost.ContextPriceBoundary()
	if !t.turns.Busy {
		b := t.agent.Breakdown()
		c.Breakdown = &b
	}
	if view == "system" {
		c.System = t.agent.SystemPrompt()
	}
	return c
}

// storedFile is a content-addressed image name: no path, a hash and a
// known extension.
var storedFile = regexp.MustCompile(`^[0-9a-f]{16,128}\.(png|jpe?g|gif|webp)$`)

// storedImage loads an image a client on this machine already put in the
// image store; only names of the store are accepted, never a path.
func storedImage(x ImageInput) (provider.Image, error) {
	if !storedFile.MatchString(x.File) {
		return provider.Image{}, fmt.Errorf("%q is not an image of the store", x.File)
	}
	im := provider.Image{File: x.File, MIME: x.MIMEType, Width: x.Width, Height: x.Height, Name: x.Name}
	if _, err := os.Stat(filepath.Join(images.Dir(), x.File)); err != nil {
		return provider.Image{}, fmt.Errorf("image %s is not in the store", x.File)
	}
	return images.Load(im)
}

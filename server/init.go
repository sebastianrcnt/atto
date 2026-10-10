package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"github.com/sebastianrcnt/atto/ui"
	"slices"
)

// ClientInfo names a client in initialize.
type ClientInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
}

// Capabilities are what a client can do, said in initialize. Interactive
// clients answer prompts (extension dialogs, confirmations): while one is
// attached, extensions see ctx.hasUI true.
type Capabilities struct {
	UI          *ui.Capabilities `json:"ui,omitempty"`
	Interactive bool             `json:"interactive,omitempty"`
	Images      bool             `json:"images,omitempty"`
	// Reattach says the client follows a thread/closed of reason
	// "upgrade" by opening the session again (its worker was replaced
	// by one of the current build). Workers are only replaced while
	// every attached client says so.
	Reattach bool `json:"reattach,omitempty"`
}

// negotiate checks that the client speaks the current protocol revision,
// the only one served.
func negotiate(versions []int) (int, error) {
	if slices.Contains(versions, ProtocolVersion) {
		return ProtocolVersion, nil
	}
	offered := "none"
	if len(versions) > 0 {
		offered = fmt.Sprint(versions)
	}
	return 0, failure(ReasonUnsupportedProtocol, "this atto speaks protocol revision %d only; the client offered %s: list %d in initialize's protocolVersions", ProtocolVersion, offered, ProtocolVersion)
}

// newInstanceID names one run of a server, so that event IDs of an
// earlier run (they start from 1 again) are never taken for this one's.
func newInstanceID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// initialize answers initialize: the server, the revision agreed and what
// the client follows events from.
func (s *Server) initialize(ctx context.Context, p threadParams) (any, error) {
	if p.Capabilities != nil && p.Capabilities.UI != nil {
		if err := p.Capabilities.UI.Validate(); err != nil {
			return nil, invalid("%s", err)
		}
	}
	v, err := negotiate(p.ProtocolVersions)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"name": "atto", "version": s.Version, "protocolVersion": v,
		"serverInstanceId": s.instance, "eventId": s.eventSeq(), "settings": clientSettings()}
	if c := connOf(ctx); c != nil {
		s.mu.Lock()
		if p.Client != nil {
			c.name = p.Client.Name
		}
		c.interactive = p.Capabilities != nil && p.Capabilities.Interactive
		c.reattach = p.Capabilities != nil && p.Capabilities.Reattach
		if p.Capabilities != nil {
			c.ui = p.Capabilities.UI
		}
		s.mu.Unlock()
		out["clientId"] = c.id
	}
	return out, nil
}

// errorReason gives older error paths a machine-readable reason without
// changing their code or message. Execution paths use failure for finer reasons.
func errorReason(code int) string {
	switch code {
	case codeParse:
		return ReasonParse
	case codeInvalidRequest:
		return ReasonInvalidRequest
	case codeInvalidParams:
		return ReasonInvalidParams
	case codeMethodNotFound:
		return ReasonMethodNotFound
	default:
		return ReasonInternal
	}
}

func (s *Server) uiCapabilities(ctx context.Context, p threadParams) (any, error) {
	c := ui.Capabilities{Version: 1, Surface: p.Surface, Width: p.Width, Elements: p.Elements}
	if err := c.Validate(); err != nil {
		return nil, invalid("%s", err)
	}
	if cc := connOf(ctx); cc != nil {
		s.mu.Lock()
		cc.ui = &c
		s.mu.Unlock()
	}
	return map[string]any{}, nil
}

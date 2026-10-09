package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"maps"
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
	Interactive bool `json:"interactive,omitempty"`
	Images      bool `json:"images,omitempty"`
}

// negotiate picks the protocol revision for a client that speaks
// versions: the newest both speak. A client that lists none gets the
// legacy revision 2 (revision 1 clients did not list them).
func negotiate(versions []int) (int, error) {
	if len(versions) == 0 {
		return 2, nil
	}
	best := 0
	for _, v := range versions {
		if v >= MinProtocolVersion && v <= ProtocolVersion && v > best {
			best = v
		}
	}
	if best == 0 {
		return 0, failure(ReasonUnsupportedProtocol, "this atto speaks protocol %d to %d, the client %v: update the older side", MinProtocolVersion, ProtocolVersion, versions)
	}
	return best, nil
}

// newInstanceID names one run of a server, so that event IDs of an
// earlier run (they start from 1 again) are never taken for this one's.
func newInstanceID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// initialize answers initialize: the server, the revision agreed and what
// the client follows events from; extra adds fields (a live session's).
func (s *Server) initialize(ctx context.Context, p threadParams, extra map[string]any) (any, error) {
	v, err := negotiate(p.ProtocolVersions)
	if err != nil {
		return nil, err
	}
	if c := connOf(ctx); c != nil {
		s.mu.Lock()
		if p.Client != nil {
			c.name = p.Client.Name
		}
		c.protocol.Store(int32(v))
		c.interactive = p.Capabilities != nil && p.Capabilities.Interactive
		s.mu.Unlock()
		extra = maps.Clone(extra)
		if extra == nil {
			extra = map[string]any{}
		}
		extra["clientId"] = c.id
	}
	out := map[string]any{"name": "atto", "version": s.Version, "protocolVersion": v,
		"serverInstanceId": s.instance, "eventId": s.eventSeq(), "settings": clientSettings()}
	maps.Copy(out, extra)
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

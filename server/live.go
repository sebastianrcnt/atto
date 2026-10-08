package server

import "github.com/sebastianrcnt/atto/core/transcript"

// Publish sends a notification through the event hub (embedded integrations).
func (s *Server) Publish(method string, params map[string]any) { s.publish(method, params) }

// WireItem is the protocol form of a transcript item of session sid.
func WireItem(sid string, it *transcript.Item) Item { return wireItem(sid, it) }

package server

import "github.com/sebastianrcnt/atto/core/transcript"

// WireItem is the protocol form of a transcript item of session sid.
func WireItem(sid string, it *transcript.Item) Item { return wireItem(sid, it) }

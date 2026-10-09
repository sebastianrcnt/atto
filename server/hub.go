package server

import (
	"encoding/json"
	"sync"
)

// broker is the server's event hub: every notification is numbered and
// fanned out to subscribers, whatever transport they use (stdio, a socket,
// a WebSocket, an in-process client). A subscriber that falls behind is
// dropped rather than slowing the server: it is told events/reset and reads
// its threads again. Event IDs start from 1 with each server; the server's
// instance ID tells two runs apart.
type broker struct {
	mu   sync.Mutex
	seq  int64
	subs map[chan hubEvent]chan struct{} // each subscriber's kick channel
}

// subscriberBuffer is how many events a subscriber may fall behind.
const subscriberBuffer = 4096

type hubEvent struct {
	id   int64
	data []byte
}

func newBroker() *broker {
	return &broker{subs: map[chan hubEvent]chan struct{}{}}
}

// publish numbers v and sends it to the subscribers. A notification learns
// its event ID (rpcNotification.EventID) here, so every transport carries
// the cursor.
func (b *broker) publish(v any) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	if n, ok := v.(rpcNotification); ok {
		n.EventID = b.seq
		v = n
	}
	data, _ := json.Marshal(v)
	ev := hubEvent{b.seq, data}
	for ch, kick := range b.subs {
		select {
		case ch <- ev:
		default:
			// A slow client: rather than skip events it would never know
			// it missed, end its stream; the connection tells it to read
			// its threads again.
			delete(b.subs, ch)
			close(kick)
		}
	}
	return b.seq
}

// subscribe returns a channel of new events and a channel closed when the
// subscriber fell behind.
func (b *broker) subscribe() (ch chan hubEvent, kick chan struct{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch = make(chan hubEvent, subscriberBuffer)
	kick = make(chan struct{})
	b.subs[ch] = kick
	return ch, kick
}

func (b *broker) unsubscribe(ch chan hubEvent) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
}

// last is the ID of the latest event.
func (b *broker) last() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seq
}

// clients counts the subscribers.
func (b *broker) clients() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

// publish sends a notification to every transport, through the hub, and
// to the Notify observer.
func (s *Server) publish(method string, params map[string]any) {
	if s.events != nil {
		s.events.publish(rpcNotification{JSONRPC: "2.0", Method: method, Params: params})
	}
	if s.Notify != nil {
		s.Notify(method, params)
	}
}

// resetNotification tells a client its events have a gap: it reads its
// threads again, following from eventId.
func (s *Server) resetNotification() []byte {
	seq := s.events.last()
	b, _ := json.Marshal(rpcNotification{JSONRPC: "2.0", Method: "events/reset", EventID: seq, Params: map[string]any{"eventId": seq, "serverInstanceId": s.instance}})
	return b
}

package server

import (
	"encoding/json"
	"sync"
)

// broker is the server's event hub: every notification is numbered,
// kept in a ring of recent events, and fanned out to subscribers, whatever
// transport they use (SSE, a stdio or socket connection, an in-process
// client). A subscriber that falls behind is dropped rather than slowing
// the server: it reconnects from the ring, or reads its thread again
// (events/reset) when the ring no longer has what it missed. Event IDs
// start from 1 with each server; the server's instance ID tells two runs
// apart. The ring is bounded by count and by bytes.
type broker struct {
	mu    sync.Mutex
	seq   int64
	ring  []sseEvent
	bytes int                             // in ring
	subs  map[chan sseEvent]chan struct{} // each subscriber's kick channel
	keep  int
}

// keepBytes bounds the ring's events: completed items carry whole command
// outputs, and 10,000 of them held megabytes for a resume that a thread
// read does as well.
const keepBytes = 2 << 20

// subscriberBuffer is how many events a subscriber may fall behind.
const subscriberBuffer = 4096

type sseEvent struct {
	id   int64
	data []byte
}

func newBroker(keep int) *broker {
	return &broker{subs: map[chan sseEvent]chan struct{}{}, keep: keep}
}

// publish numbers v, keeps it and sends it to the subscribers. A
// notification learns its event ID (rpcNotification.EventID) here, so
// every transport carries the cursor.
func (b *broker) publish(v any) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	if n, ok := v.(rpcNotification); ok {
		n.EventID = b.seq
		v = n
	}
	data, _ := json.Marshal(v)
	ev := sseEvent{b.seq, data}
	b.ring = append(b.ring, ev)
	b.bytes += len(data)
	drop := 0
	for len(b.ring)-drop > 1 && (len(b.ring)-drop > b.keep || b.bytes > keepBytes) {
		b.bytes -= len(b.ring[drop].data)
		drop++
	}
	b.ring = b.ring[drop:]
	for ch, kick := range b.subs {
		select {
		case ch <- ev:
		default:
			// A slow client: rather than skip events it would never know
			// it missed, end its stream; it reconnects with Last-Event-ID
			// and resumes from the ring.
			delete(b.subs, ch)
			close(kick)
		}
	}
	return b.seq
}

// subscribe returns events after lastID (replayed from the ring), a
// channel of new ones, and a channel closed when the subscriber fell
// behind and must reconnect. gap is set when the events after lastID are
// not known any more: lastID is from before the server started (it
// restarted) or older than the ring keeps.
func (b *broker) subscribe(lastID int64) (backlog []sseEvent, ch chan sseEvent, kick chan struct{}, gap bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case lastID > b.seq:
		gap = true
	case lastID > 0 && len(b.ring) > 0 && lastID < b.ring[0].id-1:
		gap = true
	default:
		for _, ev := range b.ring {
			if ev.id > lastID {
				backlog = append(backlog, ev)
			}
		}
	}
	ch = make(chan sseEvent, subscriberBuffer)
	kick = make(chan struct{})
	b.subs[ch] = kick
	return backlog, ch, kick, gap
}

func (b *broker) unsubscribe(ch chan sseEvent) {
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

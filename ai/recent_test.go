package ai

import (
	"bytes"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

func TestRecentRequests(t *testing.T) {
	for i := range keepRecent + 3 {
		recordRequest("u", []byte(fmt.Sprint(i)))
	}
	r := RecentRequests()
	if len(r) != keepRecent || string(r[0].Body) != "3" || string(r[len(r)-1].Body) != fmt.Sprint(keepRecent+2) {
		t.Fatalf("ring: %d, first %s", len(r), r[0].Body)
	}
	PinRecentRequests("compaction", 2)
	for range keepRecent {
		recordRequest("u", []byte("later"))
	}
	p := PinnedRequests()["compaction"]
	if len(p) != 2 || string(p[1].Body) != fmt.Sprint(keepRecent+2) {
		t.Fatalf("pinned: %v", p)
	}
}

func TestRecentRequestsRoundTrip(t *testing.T) {
	body := []byte(strings.Repeat(`{"role":"user","content":"héllo 한국어"},`, 5000))
	recordRequest("u", body)
	r := RecentRequests()
	if got := r[len(r)-1]; got.URL != "u" || !bytes.Equal(got.Body, body) {
		t.Fatalf("round trip: url %q, %d bytes, want %d", got.URL, len(got.Body), len(body))
	}
	if gz := recent.ring[len(recent.ring)-1].gz; len(gz) >= len(body)/4 {
		t.Fatalf("not compressed: %d of %d bytes", len(gz), len(body))
	}
	PinRecentRequests("rt", 1)
	if p := PinnedRequests()["rt"]; len(p) != 1 || !bytes.Equal(p[0].Body, body) {
		t.Fatal("pinned body differs")
	}
}

func TestRecentRequestsByteCap(t *testing.T) {
	// Random bytes do not compress, so each request is about 1 MiB.
	rnd := make([]byte, 1<<20)
	rand.New(rand.NewSource(1)).Read(rnd)
	for range keepRecent {
		recordRequest("u", rnd)
	}
	recent.Lock()
	n, total := len(recent.ring), 0
	for _, r := range recent.ring {
		total += len(r.gz)
	}
	recent.Unlock()
	if n < 1 || n >= keepRecent || total > maxRecentBytes {
		t.Fatalf("ring holds %d requests, %d bytes (cap %d)", n, total, maxRecentBytes)
	}
	// A request over the cap on its own is still kept.
	big := append(append([]byte(nil), rnd...), rnd...)
	big = append(big, rnd...)
	big = append(big, rnd...)
	big = append(big, rnd...)
	recordRequest("big", big)
	if r := RecentRequests(); len(r) != 1 || r[0].URL != "big" {
		t.Fatalf("ring after oversized request: %d", len(r))
	}
}

func BenchmarkRecordRequest(b *testing.B) {
	var sb strings.Builder
	for sb.Len() < 1<<20 {
		fmt.Fprintf(&sb, `{"role":"user","content":"message number %d with some text in it"},`, sb.Len())
	}
	body := []byte(sb.String())
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		recordRequest("u", body)
	}
}

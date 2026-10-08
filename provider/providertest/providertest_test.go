package providertest

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestScript(t *testing.T) {
	gate := make(chan struct{})
	m := New(t, Reply{Text: "hello", Words: 2, Gate: gate}, Reply{Command: "echo hi", Description: "Say hi"})
	done := make(chan string, 1)
	go func() {
		r, err := http.Post(m.URL, "application/json", strings.NewReader(`{"messages":[]}`))
		if err != nil {
			done <- err.Error()
			return
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		done <- string(b)
	}()
	if m.Started(time.Second) != 1 {
		t.Fatal("request did not arrive")
	}
	select {
	case <-done:
		t.Fatal("reply did not wait for gate")
	default:
	}
	close(gate)
	select {
	case b := <-done:
		if !strings.Contains(b, `"content":"he"`) || !strings.Contains(b, `"content":"llo"`) || !strings.Contains(b, "[DONE]") {
			t.Fatalf("stream: %s", b)
		}
	case <-time.After(time.Second):
		t.Fatal("reply did not finish")
	}
	for range 2 {
		r, err := http.Post(m.URL, "application/json", strings.NewReader(`{"input":"tool"}`))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if !strings.Contains(string(b), "tool_calls") || !strings.Contains(string(b), "echo hi") {
			t.Fatalf("tool reply: %s", b)
		}
	}
	requests := m.Requests()
	if len(requests) != 3 || requests[0] != `{"messages":[]}` {
		t.Fatalf("requests: %v", requests)
	}
	requests[0] = "changed"
	if m.Requests()[0] == "changed" {
		t.Fatal("Requests returned shared storage")
	}
}

func TestSplit(t *testing.T) {
	for _, c := range []struct {
		text  string
		words int
		want  string
	}{
		{"", 2, ""}, {"hello", 1, "hello"}, {"hi", 5, "hi"}, {"한글🙂", 3, "한|글|🙂"},
	} {
		if got := strings.Join(split(c.text, c.words), "|"); got != c.want {
			t.Fatalf("split(%q, %d) = %q, want %q", c.text, c.words, got, c.want)
		}
	}
}

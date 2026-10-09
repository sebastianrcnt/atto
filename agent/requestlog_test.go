package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func readRequestLog(t *testing.T) []requestLogEntry {
	t.Helper()
	f, err := os.Open(requestLogPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []requestLogEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e requestLogEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("%q: %v", sc.Text(), err)
		}
		out = append(out, e)
	}
	return out
}

func resetRequestLog(t *testing.T) {
	t.Helper()
	_ = os.Remove(requestLogPath())
	t.Cleanup(func() { _ = os.Remove(requestLogPath()) })
}

// Retries are logged with the connection they failed on, and so is the
// request that got through after them.
func TestRequestLogRecordsRetriesAndRecovery(t *testing.T) {
	noWait(t)
	resetRequestLog(t)
	srv, _ := scriptedServer(t, cutOff, status(503, "overloaded"), okReply)
	a := newTestAgent(srv.URL)
	if err := a.Run(context.Background(), "go", func(any) {}); err != nil {
		t.Fatal(err)
	}
	log := readRequestLog(t)
	if len(log) != 3 {
		t.Fatalf("log %+v", log)
	}
	for i, want := range []string{requestRetry, requestRetry, requestRecovered} {
		e := log[i]
		if e.Event != want || e.Attempt != i+1 || e.Model == "" {
			t.Fatalf("entry %d: %+v", i, e)
		}
		if e.Conn == nil || !strings.HasPrefix(e.Conn.Local, "127.0.0.1:") || e.Conn.Remote != strings.TrimPrefix(srv.URL, "http://") {
			t.Fatalf("entry %d conn: %+v", i, e.Conn)
		}
	}
	if !strings.Contains(log[1].Error, "overloaded") || log[0].WaitMs != 1 || log[2].Error != "" {
		t.Fatalf("log %+v", log)
	}
}

// A request that fails for good is logged once; one that succeeds at once
// is not logged at all.
func TestRequestLogRecordsFailures(t *testing.T) {
	noWait(t)
	resetRequestLog(t)
	srv, _ := scriptedServer(t, okReply)
	if err := newTestAgent(srv.URL).Run(context.Background(), "go", func(any) {}); err != nil {
		t.Fatal(err)
	}
	if log := readRequestLog(t); len(log) != 0 {
		t.Fatalf("logged a plain success: %+v", log)
	}
	srv, _ = scriptedServer(t, status(401, "invalid api key"))
	if err := newTestAgent(srv.URL).Run(context.Background(), "go", func(any) {}); err == nil {
		t.Fatal("no error")
	}
	log := readRequestLog(t)
	if len(log) != 1 || log[0].Event != requestFailed || !strings.Contains(log[0].Error, "invalid api key") {
		t.Fatalf("log %+v", log)
	}
}

// The same 5xx twice is logged as one retry, then the failure with a note
// saying why the request was not sent again.
func TestRequestLogRecordsRepeatedServerError(t *testing.T) {
	noWait(t)
	resetRequestLog(t)
	srv, _ := scriptedServer(t, status(503, visionDown))
	if err := newTestAgent(srv.URL).Run(context.Background(), "go", func(any) {}); err == nil {
		t.Fatal("no error")
	}
	log := readRequestLog(t)
	if len(log) != 2 || log[0].Event != requestRetry || log[0].Note != "" || log[1].Event != requestFailed ||
		log[1].Attempt != 2 || !strings.Contains(log[1].Note, "same error twice") || !strings.Contains(log[1].Error, "strata-vision") {
		t.Fatalf("log %+v", log)
	}
}

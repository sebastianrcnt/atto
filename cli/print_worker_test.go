//go:build !windows

package cli

import (
	"bytes"
	"encoding/json"
	"syscall"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/daemon"
	"github.com/sebastianrcnt/atto/provider/providertest"
)

// atto -p on a session a daemon worker runs goes through the worker,
// which owns the session's writer.
func TestPrintViaWorker(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	t.Setenv("HOME", t.TempDir())
	m := providertest.New(t, providertest.Reply{Text: "from the worker"})
	m.Install(t, config.Dir())
	cwd := t.TempDir()
	w, _, err := daemon.StartWorker("", cwd, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		// The worker ends its session as it goes: wait for it, before
		// the temporary directories go.
		daemon.Stop(true)
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if syscall.Kill(w.PID, 0) != nil {
				break
			}
		}
	}()

	got, ok := workerFor(PrintOptions{Resume: w.Session})
	if !ok || got.Session != w.Session {
		t.Fatalf("workerFor: %+v %v", got, ok)
	}
	var out, errOut bytes.Buffer
	if err := printViaWorker(PrintOptions{Resume: w.Session, Prompt: "hello", Format: "json"}, w, &out, &errOut); err != nil {
		t.Fatalf("%v: %s", err, errOut.String())
	}
	var res printResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil || res.Result != "from the worker" || res.IsError || res.SessionID != w.Session {
		t.Fatalf("result %s (%v)", out.String(), err)
	}
	if n := len(m.Requests()); n != 1 {
		t.Fatalf("%d requests", n)
	}
}

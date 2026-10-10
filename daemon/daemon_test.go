package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
)

// TestMain lets the test binary serve as a session worker.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "-version" { // the binary on disk, asked by a daemon
		fmt.Println("atto", testVersion())
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "_session-server" { // a session worker
		if err := RunWorker(testVersion(), os.Args[2:]); err != nil {
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "_daemon" { // a daemon started on demand
		exe, err := os.Executable()
		if err == nil {
			err = Serve(exe, testVersion())
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}

// testVersion is the build test workers and daemons say they are, and
// the test binary on disk answers -version with: ATTO_TEST_VERSION.
func testVersion() string {
	if v := os.Getenv("ATTO_TEST_VERSION"); v != "" {
		return v
	}
	return "test"
}

// conn is a test client: it reads frames in the background.
type conn struct {
	t      *testing.T
	c      net.Conn
	frames chan frame
}

type frame struct {
	typ byte
	b   []byte
}

func open(t *testing.T, h Hello) *conn {
	t.Helper()
	c, err := dial(false)
	if err != nil {
		t.Fatal(err)
	}
	h.Proto = Proto
	if err := writeJSON(c, fHello, h); err != nil {
		t.Fatal(err)
	}
	k := &conn{t: t, c: c, frames: make(chan frame, 100)}
	go func() {
		defer close(k.frames)
		for {
			typ, b, err := readFrame(c)
			if err != nil {
				return
			}
			k.frames <- frame{typ, b}
		}
	}()
	t.Cleanup(func() { c.Close() })
	return k
}

// until reads frames until one of type typ whose payload contains want,
// collecting the output on the way.
func (k *conn) until(typ byte, want string) (frame, string) {
	k.t.Helper()
	var out strings.Builder
	timeout := time.After(10 * time.Second)
	for {
		select {
		case f, ok := <-k.frames:
			if !ok {
				k.t.Fatalf("connection closed waiting for %c %q; output %q", typ, want, out.String())
			}
			if f.typ == typ && strings.Contains(string(f.b), want) {
				return f, out.String()
			}
			if f.typ == fError {
				k.t.Fatalf("error frame: %s", f.b)
			}
		case <-timeout:
			k.t.Fatalf("timed out waiting for %c %q; output %q", typ, want, out.String())
		}
	}
}

func startDaemon(t *testing.T, idle time.Duration) chan error {
	t.Helper()
	t.Setenv(config.EnvDir, t.TempDir())
	idleExit = idle
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	noHandover.Store(true)
	t.Cleanup(func() { noHandover.Store(false) })
	version := testVersion()
	go func() { done <- Serve(exe, version) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if c, err := net.Dial("unix", SocketPath()); err == nil {
			c.Close()
			return done
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon did not listen")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestProtocolMismatch(t *testing.T) {
	if Proto != 4 {
		t.Fatal("worker-only control requires protocol 4")
	}
	startDaemon(t, 5*time.Second)
	c, err := dial(false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = writeJSON(c, fHello, Hello{Proto: 1, Op: "list"})
	typ, b, err := readFrame(c)
	if err != nil || typ != fError || !strings.Contains(string(b), "protocol") {
		t.Fatalf("%c %s %v", typ, b, err)
	}
	_ = Stop(true)
}

func TestSocketPathFallsBackWhenLong(t *testing.T) {
	t.Setenv(config.EnvDir, filepath.Join(os.TempDir(), strings.Repeat("d", 120)))
	p := SocketPath()
	if len(p) > maxSocketPath || !strings.HasPrefix(p, os.TempDir()) {
		t.Fatalf("socket path %q", p)
	}
}

func TestClientRejectsLegacyDaemon(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	if err := privateDir(RunDir()); err != nil {
		t.Fatal(err)
	}
	ln, err := listenSocket(SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer c.Close()
		if err := authConn(c); err != nil {
			done <- err
			return
		}
		typ, b, err := readFrame(c)
		var h Hello
		if err != nil || typ != fHello || json.Unmarshal(b, &h) != nil || h.Proto == 1 {
			done <- fmt.Errorf("new client sent legacy Hello: %c %s %v", typ, b, err)
			return
		}
		done <- writeFrame(c, fError, []byte("the running atto daemon speaks protocol 1, this atto 2: end its panes, then run atto daemon stop"))
	}()
	c, _, _, err := request(Hello{Op: "new"}, false)
	if c != nil {
		c.Close()
		t.Fatal("connected to a legacy daemon")
	}
	// The existing mismatch response is actionable, not an unavailable
	// daemon error that interactive startup would silently fall back from.
	if err == nil || errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "protocol 1") {
		t.Fatalf("legacy daemon error: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestStopOlderDaemonAfterUpgrade(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	if err := privateDir(RunDir()); err != nil {
		t.Fatal(err)
	}
	ln, err := listenSocket(SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan error, 1)
	go func() {
		for _, version := range []int{Proto, Proto - 1} {
			c, err := ln.Accept()
			if err != nil {
				done <- err
				return
			}
			if err := authConn(c); err != nil {
				c.Close()
				done <- err
				return
			}
			typ, b, err := readFrame(c)
			var h Hello
			if err != nil || typ != fHello || json.Unmarshal(b, &h) != nil || h.Op != "stop" || !h.Force || h.Proto != version {
				c.Close()
				done <- fmt.Errorf("stop hello: %c %s %v", typ, b, err)
				return
			}
			if version == Proto {
				err = writeFrame(c, fError, []byte(fmt.Sprintf("the running atto daemon speaks protocol %d, this atto %d: end its panes, then run atto daemon stop", Proto-1, Proto)))
			} else {
				err = writeJSON(c, fExit, Exit{})
			}
			c.Close()
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	if err := Stop(true); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

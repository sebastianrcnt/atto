//go:build !windows

package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
)

const helperEnv = "ATTO_DAEMON_TEST_HELPER"

// TestMain lets the test binary be the program a pane runs: a line-based
// stand-in for atto that speaks the markers.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "_session-server" { // a session worker
		if err := RunWorker("test", os.Args[2:]); err != nil {
			os.Exit(1)
		}
		return
	}
	if os.Getenv(helperEnv) != "" {
		helper()
		return
	}
	os.Exit(m.Run())
}

func helper() {
	ConsumePaneToken()
	if os.Getenv(EnvPaneToken) != "" {
		panic("marker token leaked")
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGUSR1)
	go func() {
		for range sigs {
			fmt.Print("redraw\r\n")
		}
	}()
	fmt.Print(MarkerSeq("ready") + "\x1b[?2004hhello pane " + os.Getenv(EnvPane) + "\r\n")
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		switch {
		case len(f) == 0:
		case f[0] == "detach":
			fmt.Print(MarkerSeq("detach"))
		case f[0] == "switch" && len(f) == 2:
			fmt.Print(MarkerSeq("switch", f[1]))
		case f[0] == "state" && len(f) == 2:
			fmt.Print(MarkerSeq("state", f[1]))
		case f[0] == "new" && len(f) == 2:
			fmt.Print(MarkerSeq("new", f[1]))
		case f[0] == "open" && len(f) == 3:
			fmt.Print(MarkerSeq("open", f[1], f[2]))
		case f[0] == "session" && len(f) == 3:
			fmt.Print(MarkerSeq("session", f[1], f[2]))
		case f[0] == "exit" && len(f) == 2:
			n, _ := strconv.Atoi(f[1])
			os.Exit(n)
		default:
			fmt.Printf("got %s\r\n", sc.Text())
		}
	}
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
			if f.typ == fOutput {
				out.Write(f.b)
			}
			if f.typ == typ && (typ != fOutput && strings.Contains(string(f.b), want) || typ == fOutput && strings.Contains(out.String(), want)) {
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

func (k *conn) typ(s string) {
	if err := writeFrame(k.c, fInput, []byte(s)); err != nil {
		k.t.Fatal(err)
	}
}

func startDaemon(t *testing.T, idle time.Duration) chan error {
	t.Helper()
	t.Setenv(config.EnvDir, t.TempDir())
	t.Setenv(helperEnv, "1")
	idleExit = idle
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- Serve(exe) }()
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

func TestDaemonPaneLifecycle(t *testing.T) {
	done := startDaemon(t, 300*time.Millisecond)
	a := open(t, Hello{Op: "new", Cwd: t.TempDir(), Env: os.Environ(), Cols: 100, Rows: 30})
	f, _ := a.until(fAttached, `"id":1`)
	var p Pane
	_ = json.Unmarshal(f.b, &p)
	if p.ID != 1 || p.PID == 0 {
		t.Fatalf("pane %+v", p)
	}
	a.until(fOutput, "hello pane 1")
	a.typ("ping\n")
	a.until(fOutput, "got ping")

	// The session marker is taken out of the stream and listed.
	a.typ("session s123 work\n")
	deadline := time.Now().Add(5 * time.Second)
	for {
		ps, err := List()
		if err != nil {
			t.Fatal(err)
		}
		if len(ps) == 1 && ps[0].Session == "s123" && ps[0].Name == "work" && ps[0].Clients == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("list %+v", ps)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Detach: the client is told, the pane goes on.
	a.typ("detach\n")
	f, out := a.until(fExit, "detached")
	if strings.Contains(out, "7337") {
		t.Fatalf("a marker reached the client: %q", out)
	}

	// Reattach by session prefix: the modes come first, then a repaint.
	b := open(t, Hello{Op: "attach", Target: "s12", Cols: 90, Rows: 20})
	b.until(fAttached, `"id":1`)
	_, out = b.until(fOutput, "redraw")
	if !strings.HasPrefix(out, "\x1b[?2004h") {
		t.Fatalf("attach output %q: the paste mode should be restored first", out)
	}
	b.typ("again\n")
	b.until(fOutput, "got again")

	// The program's exit code ends the attachment, and the daemon exits
	// after its last pane.
	b.typ("exit 3\n")
	f, _ = b.until(fExit, "code")
	var x Exit
	_ = json.Unmarshal(f.b, &x)
	if x.Code != 3 || x.Detached {
		t.Fatalf("exit %+v", x)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not exit after its last pane")
	}
}

func TestDaemonSwitchMovesTheTerminal(t *testing.T) {
	startDaemon(t, 300*time.Millisecond)
	a := open(t, Hello{Op: "new", Cwd: t.TempDir(), Env: os.Environ()})
	a.until(fOutput, "hello pane 1")
	b := open(t, Hello{Op: "new", Cwd: t.TempDir(), Env: os.Environ()})
	b.until(fOutput, "hello pane 2")
	b.typ("detach\n")
	b.until(fExit, "detached")

	// Pane 1 asks to show pane 2 on the terminal that typed: its modes are
	// undone, the screen cleared, and pane 2 attaches and repaints.
	a.typ("switch 2\n")
	f, out := a.until(fAttached, `"id":2`)
	if !strings.Contains(out, "\x1b[?2004l") || !strings.Contains(out, "\x1b[2J") {
		t.Fatalf("switch output %q", out)
	}
	_ = f
	a.until(fOutput, "redraw")
	a.typ("where\n")
	a.until(fOutput, "got where")
	ps, _ := List()
	if len(ps) != 2 || ps[0].Clients != 0 || ps[1].Clients != 1 {
		t.Fatalf("panes after switch %+v", ps)
	}
	// Unknown targets and the pane itself are ignored.
	a.typ("switch 9\n")
	a.typ("switch 2\n")
	a.typ("still\n")
	a.until(fOutput, "got still")
	_ = Stop(true)
}

func TestDaemonStateNewAndOpen(t *testing.T) {
	startDaemon(t, 300*time.Millisecond)
	dir := t.TempDir()
	a := open(t, Hello{Op: "new", Cwd: dir, Env: os.Environ()})
	a.until(fOutput, "hello pane 1")
	a.typ("state working\n")
	a.typ("session s1 one\n")
	waitList(t, func(ps []Pane) bool { return len(ps) == 1 && ps[0].State == "working" })

	// new: a pane in that directory, shown on this terminal at once.
	a.typ("new " + dir + "\n")
	a.until(fAttached, `"id":2`)
	a.until(fOutput, "hello pane 2")
	ps, _ := List()
	if len(ps) != 2 || ps[1].Cwd != dir || ps[1].Clients != 1 || ps[0].Clients != 0 {
		t.Fatalf("after new: %+v", ps)
	}
	// open: a session already in a pane is shown there; another gets a
	// new pane running atto -session.
	a.typ("open s1 " + dir + "\n")
	a.until(fAttached, `"id":1`)
	a.typ("open s9 " + dir + "\n")
	a.until(fAttached, `"id":3`)
	ps, _ = List()
	if len(ps) != 3 || strings.Join(ps[2].Args, " ") != "-session s9" {
		t.Fatalf("after open: %+v", ps)
	}
	_ = Stop(true)
}

func waitList(t *testing.T, ok func([]Pane) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ps, _ := List()
		if ok(ps) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("list %+v", ps)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDaemonKillStopAndErrors(t *testing.T) {
	done := startDaemon(t, 300*time.Millisecond)
	a := open(t, Hello{Op: "new", Cwd: t.TempDir(), Env: os.Environ()})
	a.until(fOutput, "hello pane")

	// A second daemon for the same atto dir steps aside.
	exe, _ := os.Executable()
	if err := Serve(exe); err != nil {
		t.Fatalf("second daemon: %v", err)
	}

	if err := Stop(false); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("stop with a pane running: %v", err)
	}
	if err := Kill("9"); err == nil {
		t.Fatal("killed a pane that does not exist")
	}
	if c := open(t, Hello{Op: "attach", Target: "nope"}); c != nil {
		f := <-c.frames
		if f.typ != fError {
			t.Fatalf("attach to a missing pane: %c %s", f.typ, f.b)
		}
	}
	if err := Kill("1"); err != nil {
		t.Fatal(err)
	}
	a.until(fExit, "code")
	if err := Stop(false); err != nil && !strings.Contains(err.Error(), ErrUnavailable.Error()) {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not exit")
	}
	if ps, err := List(); err != nil || ps != nil {
		t.Fatalf("list with no daemon: %v %v", ps, err)
	}
}

func TestProtocolMismatch(t *testing.T) {
	if Proto <= 1 {
		t.Fatal("pane switching and state require a new protocol version")
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
	t.Setenv(config.EnvDir, "/tmp/"+strings.Repeat("d", 120))
	p := SocketPath()
	if len(p) > maxSocketPath || !strings.HasPrefix(p, os.TempDir()) {
		t.Fatalf("socket path %q", p)
	}
}

func TestSocketDirectoryRejectsUnsafePaths(t *testing.T) {
	for _, mode := range []os.FileMode{0o755, 0o777} {
		dir := t.TempDir()
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		if err := privateDir(dir); err == nil {
			t.Fatalf("accepted mode %o", mode)
		}
	}
	root := t.TempDir()
	link := root + "/link"
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if err := privateDir(link); err == nil {
		t.Fatal("accepted a symlink")
	}
	dir := root + "/private"
	if err := privateDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := privateDir(dir); err != nil {
		t.Fatal(err)
	}
}

func TestDialRejectsUnsafeDirectoryBeforeHello(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	if err := os.MkdirAll(RunDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(RunDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if c, err := dial(false); err == nil {
		c.Close()
		t.Fatal("accepted unsafe socket directory")
	}
}

func TestPeerUID(t *testing.T) {
	if err := peerUID(uint32(os.Getuid())); err != nil {
		t.Fatal(err)
	}
	if err := peerUID(uint32(os.Getuid()) + 1); err == nil {
		t.Fatal("accepted another user's daemon")
	}
}

func TestAttachAfterPaneExit(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	p := &pane{exited: true, exitCode: 3, st: newStream()}
	c := &client{conn: a}
	done := make(chan bool, 1)
	go func() { done <- p.attach(c) }()
	typ, payload, err := readFrame(b)
	if err != nil || typ != fExit || !strings.Contains(string(payload), `"code":3`) {
		t.Fatalf("late attach: %c %s %v", typ, payload, err)
	}
	if <-done {
		t.Fatal("attached to an exited pane")
	}
	if len(p.clients) != 0 {
		t.Fatal("late client registered")
	}
}

func TestMoveDoesNotRegisterDisconnectedClient(t *testing.T) {
	a, b := net.Pipe()
	c := &client{conn: a}
	from := &pane{st: newStream(), info: Pane{ID: 1}, clients: []*client{c}, last: c}
	to := &pane{st: newStream(), info: Pane{ID: 2}}
	c.pane = from
	d := &daemon{panes: map[int]*pane{1: from, 2: to}}
	done := make(chan struct{})
	go func() { d.move(c, from, "2"); close(done) }()
	// The reset is in progress after removal from the old pane. The input
	// reader observes the disconnect before the destination can attach.
	var h [5]byte
	if _, err := b.Read(h[:]); err != nil {
		t.Fatal(err)
	}
	c.close()
	b.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("move blocked")
	}
	if len(to.clients) != 0 {
		t.Fatal("closed client registered on destination")
	}
}

func TestFailedAttachRemovesClient(t *testing.T) {
	a, b := net.Pipe()
	b.Close()
	p := &pane{st: newStream(), size: sane(Size{})}
	c := &client{conn: a}
	p.attach(c)
	deadline := time.Now().Add(time.Second)
	for {
		p.mu.Lock()
		n := len(p.clients)
		p.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("failed send left a client registered")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSlowClientDoesNotStallOutput(t *testing.T) {
	slow, unread := net.Pipe()
	defer unread.Close()
	fast, reader := net.Pipe()
	defer reader.Close()
	a, b := &client{conn: slow}, &client{conn: fast}
	p := &pane{st: newStream(), clients: []*client{a, b}}
	a.pane, b.pane = p, p
	defer a.close()
	defer b.close()
	received := make(chan string, 1)
	go func() { _, payload, _ := readFrame(reader); received <- string(payload) }()
	returned := make(chan struct{})
	go func() { p.output([]byte("healthy")); close(returned) }()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("output stalled on slow client")
	}
	select {
	case got := <-received:
		if got != "healthy" {
			t.Fatalf("output %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("healthy client stalled")
	}
}

func TestClientQueueOverflowDropsClient(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	c := &client{conn: a}
	p := &pane{st: newStream(), clients: []*client{c}}
	c.pane = p
	defer c.close()
	// No reader drains this connection. The writer may hold one frame,
	// but the rest must remain bounded and overflow must remove the client.
	returned := make(chan struct{})
	go func() {
		for range clientQueueFrames + 2 {
			p.output([]byte(strings.Repeat("x", 64<<10)))
		}
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("overflow blocked the pump")
	}
	p.mu.Lock()
	n := len(p.clients)
	p.mu.Unlock()
	if n != 0 {
		t.Fatal("overflow did not drop client")
	}
	c.wmu.Lock()
	queued := c.queued
	c.wmu.Unlock()
	if queued > clientQueueBytes {
		t.Fatalf("queue has %d bytes", queued)
	}
}

func TestClientRejectsLegacyDaemon(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	if err := privateDir(RunDir()); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", SocketPath())
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

//go:build !windows

package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// idleExit is how long the daemon waits, with no pane left, for a client
// before it exits.
var idleExit = 2 * time.Second

// writeWait bounds a write in a client's independent writer.
const writeWait = 5 * time.Second

const clientQueueBytes = 1 << 20
const clientQueueFrames = 32

// Serve runs the daemon in this process until its last pane ends (or
// stop). exe is the atto binary the panes run. It returns at once, with
// no error, when another daemon already serves this atto dir.
func Serve(exe string) error {
	if err := privateDir(RunDir()); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(RunDir(), "daemon.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil // another daemon has it
	}
	sock := SocketPath()
	if err := privateDir(filepath.Dir(sock)); err != nil {
		return err
	}
	_ = os.Remove(sock) // a dead daemon's: we hold the lock
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	_ = os.Chmod(sock, 0o600)
	d := &daemon{exe: exe, ln: ln, panes: map[int]*pane{}, idleAfter: idleExit}
	d.idle = time.AfterFunc(d.idleAfter, func() { ln.Close() })
	defer os.Remove(sock)
	for {
		c, err := ln.Accept()
		if err != nil {
			d.mu.Lock()
			for _, p := range d.panes {
				p.hangup()
			}
			d.mu.Unlock()
			return nil
		}
		go d.serve(c)
	}
}

type daemon struct {
	exe       string
	ln        net.Listener
	mu        sync.Mutex
	panes     map[int]*pane
	next      int
	idle      *time.Timer // ends the daemon when it fires with no pane
	idleAfter time.Duration
}

type pane struct {
	d    *daemon
	cmd  *exec.Cmd
	ptmx *os.File

	mu       sync.Mutex
	info     Pane
	clients  []*client
	last     *client // the client that typed last: its size wins
	st       *stream
	size     Size
	exited   bool
	exitCode int
	ready    bool // its atto handles SIGUSR1 (it said so with a marker)
}

type client struct {
	conn      net.Conn
	wmu       sync.Mutex
	queue     chan clientFrame
	done      chan struct{}
	queued    int
	stopped   bool
	finishing bool
	size      Size // guarded by its pane's mu

	pmu    sync.Mutex
	pane   *pane // the pane it shows
	closed bool
}

func (c *client) current() *pane {
	c.pmu.Lock()
	defer c.pmu.Unlock()
	return c.pane
}

func (c *client) close() {
	c.pmu.Lock()
	c.closed = true
	p := c.pane
	c.pmu.Unlock()
	c.wmu.Lock()
	if !c.stopped {
		c.stopped = true
		if c.done != nil {
			close(c.done)
		}
	}
	c.wmu.Unlock()
	c.conn.Close()
	if p != nil {
		p.remove(c)
	}
}

type clientFrame struct {
	typ     byte
	payload []byte
	last    bool
}

func (c *client) send(typ byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.stopped || c.finishing {
		return net.ErrClosed
	}
	if c.queue == nil {
		c.queue = make(chan clientFrame, clientQueueFrames)
		c.done = make(chan struct{})
		go c.write()
	}
	if c.queued+len(payload) > clientQueueBytes {
		return errors.New("daemon: client output queue full")
	}
	f := clientFrame{typ, slices.Clone(payload), typ == fExit || typ == fError || typ == fList}
	select {
	case c.queue <- f:
		c.queued += len(payload)
		c.finishing = f.last
		return nil
	default:
		return errors.New("daemon: client output queue full")
	}
}

func (c *client) write() {
	for {
		select {
		case <-c.done:
			return
		case f := <-c.queue:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			err := writeFrame(c.conn, f.typ, f.payload)
			c.wmu.Lock()
			c.queued -= len(f.payload)
			c.wmu.Unlock()
			if err != nil || f.last {
				c.close()
				return
			}
		}
	}
}

func (c *client) sendJSON(typ byte, v any) error {
	b, _ := json.Marshal(v)
	return c.send(typ, b)
}

func (d *daemon) serve(conn net.Conn) {
	typ, b, err := readFrame(conn)
	var h Hello
	if err != nil || typ != fHello || json.Unmarshal(b, &h) != nil {
		conn.Close()
		return
	}
	c := &client{conn: conn, size: h.Size}
	fail := func(format string, args ...any) {
		if c.send(fError, fmt.Appendf(nil, format, args...)) != nil {
			c.close()
		}
	}
	if h.Proto != Proto {
		fail("the running atto daemon speaks protocol %d, this atto %d: end its panes, then run atto daemon stop", Proto, h.Proto)
		return
	}
	switch h.Op {
	case "list":
		if c.sendJSON(fList, d.list()) != nil {
			c.close()
		}
	case "stop":
		if n := len(d.list()); n > 0 && !h.Force {
			fail("%d pane(s) running; atto daemon stop -force ends them", n)
			return
		}
		if c.sendJSON(fExit, Exit{}) != nil {
			c.close()
		}
		d.ln.Close()
	case "kill":
		p := d.find(h.Target)
		if p == nil {
			fail("no pane %q (atto attach -l lists them)", h.Target)
			return
		}
		p.hangup()
		if c.sendJSON(fExit, Exit{}) != nil {
			c.close()
		}
	case "new":
		p, err := d.start(h)
		if err != nil {
			fail("starting atto: %v", err)
			return
		}
		if p.attach(c) {
			d.read(c)
		}
	case "attach":
		p := d.find(h.Target)
		if p == nil {
			if h.Target == "" {
				fail("no atto pane is running")
			} else {
				fail("no pane %q (atto attach -l lists them)", h.Target)
			}
			return
		}
		if p.attach(c) {
			d.read(c)
		}
	default:
		fail("unknown request %q", h.Op)
	}
}

func (d *daemon) list() []Pane {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []Pane
	for _, p := range d.panes {
		p.mu.Lock()
		i := p.info
		i.Clients = len(p.clients)
		p.mu.Unlock()
		out = append(out, i)
	}
	slices.SortFunc(out, func(a, b Pane) int { return a.ID - b.ID })
	return out
}

// find picks a pane by ID or open session (a prefix will do); "" is the
// most recently active one, preferring those no terminal shows.
func (d *daemon) find(target string) *pane {
	d.mu.Lock()
	defer d.mu.Unlock()
	var best *pane
	var bestKey time.Time
	bestFree := false
	for _, p := range d.panes {
		p.mu.Lock()
		i, free := p.info, len(p.clients) == 0
		p.mu.Unlock()
		if target != "" {
			if strconv.Itoa(i.ID) == target || (i.Session != "" && strings.HasPrefix(i.Session, target)) {
				return p
			}
			continue
		}
		if best == nil || (free && !bestFree) || (free == bestFree && i.Active.After(bestKey)) {
			best, bestKey, bestFree = p, i.Active, free
		}
	}
	return best
}

func (d *daemon) start(h Hello) (*pane, error) {
	d.mu.Lock()
	d.next++
	id := d.next
	d.mu.Unlock()
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(tokenBytes)
	cmd := exec.Command(d.exe, h.Args...)
	cmd.Dir = h.Cwd
	cmd.Env = append(slices.DeleteFunc(slices.Clone(h.Env), func(e string) bool {
		return strings.HasPrefix(e, EnvPane+"=") || strings.HasPrefix(e, EnvPaneToken+"=")
	}), EnvPane+"="+strconv.Itoa(id), EnvPaneToken+"="+token)
	size := sane(h.Size)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(size.Cols), Rows: uint16(size.Rows)})
	if err != nil {
		return nil, err
	}
	now := time.Now()
	p := &pane{d: d, cmd: cmd, ptmx: ptmx, st: newStream(), size: size,
		info: Pane{ID: id, PID: cmd.Process.Pid, Cwd: h.Cwd, Args: h.Args, Started: now, Active: now}}
	p.st.token = token
	d.mu.Lock()
	d.panes[id] = p
	d.idle.Stop()
	d.mu.Unlock()
	go p.pump()
	return p, nil
}

func sane(s Size) Size {
	if s.Cols <= 0 || s.Rows <= 0 {
		return Size{Cols: 80, Rows: 24}
	}
	return s
}

// pump relays the program's output to the clients until it exits, then
// ends their attachments with its exit code.
func (p *pane) pump() {
	buf := make([]byte, 64<<10)
	for {
		n, err := p.ptmx.Read(buf)
		if n > 0 {
			p.output(buf[:n])
		}
		if err != nil {
			break
		}
	}
	code := 0
	if err := p.cmd.Wait(); err != nil {
		code = 1
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			code = ee.ExitCode()
		}
	}
	p.ptmx.Close()
	p.mu.Lock()
	p.exited, p.exitCode = true, code
	clients := p.clients
	p.clients = nil
	p.mu.Unlock()
	d := p.d
	d.mu.Lock()
	delete(d.panes, p.info.ID)
	if len(d.panes) == 0 {
		d.idle.Reset(d.idleAfter)
	}
	d.mu.Unlock()
	for _, c := range clients {
		if c.sendJSON(fExit, Exit{Code: code}) != nil {
			c.close()
		}
	}
}

func (p *pane) output(b []byte) {
	p.mu.Lock()
	out, markers := p.st.feed(b)
	clients := slices.Clone(p.clients)
	var detach, move, spawnFor *client
	target := ""
	var spawn Hello
	for _, m := range markers {
		cmd, rest, _ := strings.Cut(m, ";")
		switch cmd {
		case "detach":
			detach = p.last
		case "session":
			p.info.Session, p.info.Name, _ = strings.Cut(rest, ";")
		case "ready":
			p.ready = true
		case "switch":
			move, target = p.last, rest
		case "state":
			p.info.State = rest
		case "new", "open": // a new pane (on a saved session), shown at once
			cwd, session := rest, ""
			if cmd == "open" {
				session, cwd, _ = strings.Cut(rest, ";")
			}
			spawnFor = p.last
			spawn = Hello{Cwd: cwd, Env: p.cmd.Env}
			if session != "" {
				spawn.Args = []string{"-session", session}
				target = session
			}
		}
	}
	p.mu.Unlock()
	if len(out) > 0 {
		for _, c := range clients {
			if c.send(fOutput, out) != nil {
				c.close()
			}
		}
	}
	if detach != nil {
		p.detach(detach, true)
	}
	if move != nil {
		p.d.move(move, p, target)
	}
	if spawnFor != nil {
		p.d.spawnFor(spawnFor, p, spawn, target)
	}
}

// spawnFor starts a pane for client c, which pane from showed, and moves
// c there; a session already open in a pane is shown instead.
func (d *daemon) spawnFor(c *client, from *pane, h Hello, session string) {
	if session != "" {
		if q := d.find(session); q != nil {
			d.move(c, from, strconv.Itoa(q.info.ID))
			return
		}
	}
	from.mu.Lock()
	h.Size = c.size
	from.mu.Unlock()
	q, err := d.start(h)
	if err != nil {
		if c.send(fOutput, fmt.Appendf(nil, "\r\natto: starting a pane: %v\r\n", err)) != nil {
			c.close()
		}
		return
	}
	d.move(c, from, strconv.Itoa(q.info.ID))
}

// move shows another pane on c's terminal: from's modes are undone and the
// screen cleared before target's are set and it repaints.
func (d *daemon) move(c *client, from *pane, target string) {
	to := d.find(target)
	if to == nil || to == from || !from.remove(c) {
		return
	}
	from.mu.Lock()
	reset := from.st.reset()
	from.mu.Unlock()
	if c.send(fOutput, []byte(reset+"\x1b[2J\x1b[H")) != nil {
		c.close()
		return
	}
	to.attach(c)
}

// attach shows the pane on c's terminal: the terminal modes the program
// set, then a full repaint at c's size.
func (p *pane) attach(c *client) bool {
	c.pmu.Lock()
	if c.closed {
		c.pmu.Unlock()
		return false
	}
	p.mu.Lock()
	if p.exited {
		code := p.exitCode
		p.mu.Unlock()
		c.pmu.Unlock()
		if c.sendJSON(fExit, Exit{Code: code}) != nil {
			c.close()
		}
		return false
	}
	p.info.Active = time.Now()
	info := p.info
	info.Clients = len(p.clients) + 1
	restore := p.st.restore()
	c.pane = p
	p.clients = append(p.clients, c)
	p.last = c
	size := c.size
	err := c.sendJSON(fAttached, info)
	if err == nil && restore != "" {
		err = c.send(fOutput, []byte(restore))
	}
	p.mu.Unlock()
	c.pmu.Unlock()
	if err != nil {
		c.close()
		return false
	}
	p.resize(size)
	p.redraw()
	return true
}

// read takes c's input for the pane it shows until it leaves.
func (d *daemon) read(c *client) {
	for {
		typ, b, err := readFrame(c.conn)
		p := c.current()
		if err != nil {
			c.close()
			return
		}
		if p == nil {
			c.close()
			return
		}
		switch typ {
		case fInput:
			p.mu.Lock()
			p.last = c
			p.info.Active = time.Now()
			p.mu.Unlock()
			if _, err := p.ptmx.Write(b); err != nil {
				p.detach(c, false)
				return
			}
		case fResize:
			var s Size
			if json.Unmarshal(b, &s) == nil {
				p.mu.Lock()
				c.size = s
				last := p.last == c
				p.mu.Unlock()
				if last {
					p.resize(s)
				}
			}
		}
	}
}

// remove takes c off the pane, if it was on; the next client's size then
// applies.
func (p *pane) remove(c *client) bool {
	p.mu.Lock()
	i := slices.Index(p.clients, c)
	if i < 0 {
		p.mu.Unlock()
		return false
	}
	p.clients = slices.Delete(p.clients, i, i+1)
	if p.last == c {
		p.last = nil
		if n := len(p.clients); n > 0 {
			p.last = p.clients[n-1]
		}
	}
	var size *Size
	if p.last != nil {
		s := p.last.size
		size = &s
	}
	p.mu.Unlock()
	if size != nil {
		p.resize(*size)
	}
	return true
}

// detach ends c's attachment; the pane goes on. tell sends c the reason.
func (p *pane) detach(c *client, tell bool) {
	if !p.remove(c) {
		return
	}
	if tell {
		if c.sendJSON(fExit, Exit{Detached: true}) != nil {
			c.close()
		}
	} else {
		c.close()
	}
}

func (p *pane) resize(s Size) {
	s = sane(s)
	p.mu.Lock()
	same := s == p.size
	p.size = s
	p.mu.Unlock()
	if !same {
		_ = pty.Setsize(p.ptmx, &pty.Winsize{Cols: uint16(s.Cols), Rows: uint16(s.Rows)})
	}
}

// redraw asks the pane's atto for a full repaint. It repaints on SIGUSR1,
// once it has said it handles it: until then the signal would end it (and
// a just-started atto draws its first frame anyway).
func (p *pane) redraw() {
	p.mu.Lock()
	ready := p.ready
	p.mu.Unlock()
	if ready {
		_ = p.cmd.Process.Signal(syscall.SIGUSR1)
	}
}

// hangup ends the pane's program as a closed terminal would.
func (p *pane) hangup() { _ = p.cmd.Process.Signal(syscall.SIGHUP) }

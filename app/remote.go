package app

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/daemon"
	"github.com/sebastianrcnt/atto/server"
	"github.com/sebastianrcnt/atto/tui"
)

// /remote serves the session this terminal shows to a phone or browser:
// the same protocol and web client as atto serve, as a scoped gateway of
// the session runtime (server.Scope): the browser follows the session the
// terminal shows, sees the transcript live and sends input as if typed;
// the runtime's prompts reach it too. Each start makes a new token, so
// /remote off revokes the link; it never stops the session.

// defaultRemotePort is /remote's port unless settings.json's
// "remote": {"port": N} or /remote on <port> says otherwise.
const defaultRemotePort = 7879

// remote is a running /remote gateway.
type remote struct {
	srv     *http.Server
	addr    string // what it listens on
	token   string
	links   []string
	clients int
	proxy   *server.Server
}

func (a *App) cmdRemote(arg string) {
	fields := strings.Fields(arg)
	verb := ""
	if len(fields) > 0 {
		verb = fields[0]
	}
	switch verb {
	case "":
		if a.remote != nil {
			a.showRemote(a.remote)
			return
		}
		a.startRemote(0)
	case "on":
		port := 0
		if len(fields) > 1 {
			p, err := strconv.Atoi(fields[1])
			if err != nil || p < 1 || p > 65535 {
				a.notice("Not a port: %s. Usage: /remote on [port]", fields[1])
				return
			}
			port = p
		}
		if r := a.remote; r != nil {
			if _, cur, _ := net.SplitHostPort(r.addr); port == 0 || strconv.Itoa(port) == cur {
				a.showRemote(r)
				return
			}
			a.stopRemote()
		}
		a.startRemote(port)
	case "off":
		if a.remote == nil {
			a.notice("Remote control is off.")
			return
		}
		a.stopRemote()
		a.notice("Remote control stopped. Its link no longer works.")
	default:
		a.notice("Usage: /remote [on [port]|off]")
	}
}

// startRemote listens on port (0: the configured one) on every interface.
func (a *App) startRemote(port int) {
	if port == 0 {
		port = defaultRemotePort
		if s, err := config.LoadSettings(); err == nil && s.Remote != nil && s.Remote.Port > 0 {
			port = s.Remote.Port
		}
		if a.remotePort != nil {
			port = *a.remotePort
		}
	}
	token, err := server.NewToken(16)
	if err != nil {
		a.errorNotice(err)
		return
	}
	host := a.remoteHost
	if host == "" {
		host = "0.0.0.0"
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		a.errorNotice(fmt.Errorf("remote control: %w", err))
		return
	}
	srv := a.conn.own
	if srv == nil {
		srv = server.New(Version, a.cwd)
		srv.Workers = daemon.Routes()
	}
	r := &remote{token: token, addr: ln.Addr().String()}
	if a.conn.own == nil {
		r.proxy = srv
	}
	countClients := func(n int) {
		go a.ui.Do(func() {
			if a.remote == r {
				r.clients = n
			}
		})
	}
	scope := server.Scope{
		OnClients: countClients,
		Thread: func() string {
			var id string
			a.ui.Do(func() { id = a.threadID })
			return id
		},
		Local: func(text string) bool {
			done := false
			a.ui.Do(func() {
				if c, _, err := server.ResolveCommand(a.catalogOrBuiltins(), text); err == nil && c.Origin == "builtin" && local[c.Name] != nil {
					done = a.runLocal(text)
				}
			})
			return done
		},
	}
	r.srv = &http.Server{Handler: srv.ScopedHandler(token, scope), ReadHeaderTimeout: 10 * time.Second}
	r.links = server.WebLinks(r.addr, token)
	if len(r.links) == 0 { // no network beyond this machine
		r.links = []string{"http://" + r.addr + "/#token=" + token}
	}
	a.remote = r
	if a.modal != nil {
		a.advertiseModal(a.modal)
	}
	go func() { _ = r.srv.Serve(ln) }()
	a.showRemote(r)
}

// stopRemote closes the server and every connection to it.
func (a *App) stopRemote() {
	r := a.remote
	if r == nil {
		return
	}
	a.remote = nil
	_ = r.srv.Close()
	if r.proxy != nil {
		r.proxy.Close()
	}
}

// showRemote prints the links, a QR code of the first, and the warnings.
func (a *App) showRemote(r *remote) {
	b := &remoteBlock{title: "Remote control is on. Open this link on your phone or in a browser:", links: r.links}
	if qr, err := server.QR(r.links[0]); err == nil {
		b.qr = qr
	}
	if len(r.links) > 1 {
		b.hint = append(b.hint, "Other addresses of this machine are listed too; the QR code is the first.")
	}
	if !server.IsLoopback(r.addr) {
		b.hint = append(b.hint, strings.TrimPrefix(server.TLSWarning, "warning: "))
	}
	b.hint = append(b.hint, "Anyone with the link controls this session. /remote off stops it and revokes the link.")
	a.add(b)
}

// remoteBlock shows the links and QR code of /remote.
type remoteBlock struct {
	title string
	links []string
	qr    []string
	hint  []string
}

func (b *remoteBlock) Render(width int) []string {
	out := []string{"  " + tui.Bold("◉ ") + b.title}
	for _, l := range b.links {
		for _, w := range tui.Wrap(l, max(1, width-4)) {
			out = append(out, "    "+tui.FG(6, w))
		}
	}
	if len(b.qr) > 0 {
		if qw := tui.VisibleWidth(b.qr[0]) + 4; qw <= width {
			out = append(out, "")
			for _, l := range b.qr {
				out = append(out, "    "+l) // not styled: the code needs plain foreground blocks
			}
		} else {
			out = append(out, "  "+tui.Dim(fmt.Sprintf("(widen the terminal to %d columns for the QR code)", qw)))
		}
	}
	for _, h := range b.hint {
		for _, w := range tui.Wrap(h, max(1, width-2)) {
			out = append(out, "  "+tui.Dim(w))
		}
	}
	return out
}

// remoteStatus is the status line's indicator.
func (a *App) remoteStatus() string {
	if a.remote == nil {
		return ""
	}
	return tui.Dim(fmt.Sprintf("remote · %d connected", a.remote.clients))
}

// remoteSwitched tells the gateway's clients the terminal shows another
// session now.
func (a *App) remoteSwitched() {
	if a.remote != nil {
		srv := a.remote.proxy
		if srv == nil && a.conn != nil {
			srv = a.conn.own
		}
		if srv != nil {
			srv.Switched(a.threadID, a.remoteThread)
		}
	}
	a.remoteThread = a.threadID
}

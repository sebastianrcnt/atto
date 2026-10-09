package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/sebastianrcnt/atto/server/web"
)

// WebHandler serves the static page and the protocol at /ws without a token:
// the web UI is for a trusted LAN or a tailnet (Tailscale), which decide who can
// reach the port. What still needs guarding is the browser itself: a page from
// elsewhere must not drive atto. Origin checks stop cross-site sockets, and the
// Host check stops DNS rebinding (a site's name re-pointed at this machine),
// which a same-host Origin would otherwise pass. No cookies, HTTP RPC or
// query parameters on /ws.
func (s *Server) WebHandler(origins []string) http.Handler {
	ws := s.WebSocketHandler("", origins)
	static := web.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !webHostOK(r.Host, origins) {
			http.Error(w, "host not allowed; open atto by IP address, localhost, this machine's name or its Tailscale name", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/ws" {
			if r.URL.RawQuery != "" {
				http.Error(w, "query parameters forbidden", 400)
				return
			}
			ws.ServeHTTP(w, r)
			return
		}
		static.ServeHTTP(w, r)
	})
}

// webHostOK accepts the names a person types to reach this machine: an IP
// address, localhost, the machine's own name (also as NAME.local), a Tailscale
// MagicDNS name (*.ts.net) and the hosts of --allow-origin. A rebinding
// attacker's domain is none of these.
func webHostOK(hostport string, origins []string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if host == "" {
		return false
	}
	if net.ParseIP(host) != nil || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".ts.net") {
		return true
	}
	if name, err := os.Hostname(); err == nil && name != "" {
		name = strings.ToLower(name)
		short, _, _ := strings.Cut(name, ".")
		if host == name || host == short || host == short+".local" {
			return true
		}
	}
	for _, o := range origins {
		if u, err := url.Parse(o); err == nil && strings.EqualFold(u.Hostname(), host) {
			return true
		}
	}
	return false
}

// WebListener owns only its transport. Close cancels attached clients, never
// closes the Server or any worker/session. Call Wait to collect serve errors.
type WebListener struct {
	URL    string   // the listening address, e.g. http://[::]:7879/
	URLs   []string // addresses to open it by: for a wildcard bind, this machine's IPv4 addresses (LAN, Tailscale)
	Public bool
	cancel context.CancelFunc
	done   chan struct{}
	err    error
	once   sync.Once
}

func (l *WebListener) Close()      { l.once.Do(l.cancel) }
func (l *WebListener) Wait() error { <-l.done; return l.err }

func (s *Server) ListenWeb(ctx context.Context, address string, origins []string) (*WebListener, error) {
	u, err := url.Parse(address)
	if err != nil {
		return nil, err
	}
	if (u.Scheme != "ws" && u.Scheme != "http") || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("web listen must be ws://HOST:PORT or http://HOST:PORT")
	}
	ln, err := net.Listen("tcp", u.Host)
	if err != nil {
		return nil, err
	}
	public := !IsLoopback(ln.Addr().String())
	ctx, cancel := context.WithCancel(ctx)
	l := &WebListener{URL: "http://" + ln.Addr().String() + "/", Public: public, cancel: cancel, done: make(chan struct{})}
	l.URLs = openURLs(ln.Addr().(*net.TCPAddr))
	if len(l.URLs) == 0 {
		l.URLs = []string{l.URL}
	}
	go func() {
		defer close(l.done)
		defer ln.Close()
		l.err = serveHTTP(ctx, ln, s.WebHandler(origins))
		cancel()
	}()
	return l, nil
}

// openURLs lists http://IP:PORT/ for each non-loopback IPv4 address of this
// machine when addr is a wildcard bind; otherwise none.
func openURLs(addr *net.TCPAddr) []string {
	if !addr.IP.IsUnspecified() {
		return nil
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.IsLoopback() || n.IP.To4() == nil || n.IP.IsLinkLocalUnicast() {
			continue
		}
		out = append(out, "http://"+net.JoinHostPort(n.IP.String(), strconv.Itoa(addr.Port))+"/")
	}
	return out
}

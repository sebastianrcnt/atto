package app

import (
	"context"
	"strings"

	"github.com/sebastianrcnt/atto/daemon"
	"github.com/sebastianrcnt/atto/server"
)

// /remote shares the TUI's in-process runtime or the ordinary daemon facade;
// it is not a second session owner. /remote off only closes transports.
func (a *App) cmdRemote(arg string) {
	if strings.TrimSpace(arg) == "off" {
		if a.webListener != nil {
			a.webListener.Close()
			a.webListener = nil
		}
		a.notice("Web listener stopped; session work continues.")
		return
	}
	if a.webListener != nil {
		a.notice("Web UI: %s", strings.Join(a.webListener.URLs, "  "))
		return
	}
	s := a.webServer
	if a.conn != nil && a.conn.own != nil {
		s = a.conn.own
	}
	if s == nil {
		s = server.New(Version, a.cwd)
		s.Workers = daemon.Routes()
		a.webServer = s
	}
	address := "ws://0.0.0.0:7879"
	if strings.TrimSpace(arg) != "" {
		address = strings.TrimSpace(arg)
	}
	l, err := s.ListenWeb(context.Background(), address, nil)
	if err != nil {
		a.errorNotice(err)
		return
	}
	a.webListener = l
	a.notice("Web UI: %s  (/remote off stops listening)", strings.Join(l.URLs, "  "))
	if l.Public {
		a.notice("%s", server.WebWarning)
	}
}

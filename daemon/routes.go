package daemon

import (
	"context"
	"errors"
	"os"
	"runtime"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/server"
)

// Enabled reports whether frontends should use daemon session workers.
func Enabled() bool {
	if runtime.GOOS == "windows" || os.Getenv("ATTO_NO_DAEMON") != "" {
		return false
	}
	settings, _ := config.LoadSettings()
	return settings.Daemon == nil || *settings.Daemon
}

// Usable is Enabled, unless a daemon of another protocol runs (the binary
// was upgraded under it): then sessions run in-process until it stops.
func Usable() bool {
	if !Enabled() {
		return false
	}
	_, err := Workers()
	return !errors.Is(err, ErrProtocol)
}

// Routes supplies a protocol facade with the daemon's worker discovery.
func Routes() *server.WorkerRoutes {
	return &server.WorkerRoutes{
		Open: func(ctx context.Context, id, cwd, model, effort string, deferred bool) (*server.Client, string, error) {
			var args []string
			if model != "" {
				args = append(args, "-model", model)
			}
			if effort != "" {
				args = append(args, "-effort", effort)
			}
			if deferred {
				args = append(args, "-defer-start")
			}
			w, readOnly, err := StartWorker(id, cwd, args)
			if err != nil {
				return nil, "", err
			}
			if readOnly != "" {
				return nil, "", errors.New(readOnly)
			}
			nc, err := DialWorker(w)
			if err != nil {
				return nil, "", err
			}
			return server.NewClient(nc), w.Session, nil
		},
		List: func() ([]server.WorkerSummary, error) {
			workers, err := Workers()
			if err != nil {
				return nil, err
			}
			out := make([]server.WorkerSummary, 0, len(workers))
			for _, w := range workers {
				out = append(out, server.WorkerSummary{ID: w.Session, Cwd: w.Cwd, PID: w.PID, Version: w.Version, Busy: w.Busy, Clients: w.Clients})
			}
			return out, nil
		},
	}
}

package server

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/sebastianrcnt/atto/config"
)

// RunStdio implements "atto app-server": JSON-RPC over stdin/stdout.
func RunStdio(version string) error { return RunStdioWith(version, nil, nil) }

// RunStdioWith accepts app-server flags and optional worker routing.
func RunStdioWith(version string, args []string, routes *WorkerRoutes) error {
	fs := flag.NewFlagSet("app-server", flag.ContinueOnError)
	var origins originFlags
	fs.Var(&origins, "allow-origin", "additional browser origin allowed on WebSocket (repeatable)")
	inProcess := fs.Bool("in-process", false, "run session runtimes in this process")
	listen := fs.String("listen", "stdio://", "transport: stdio://, unix:///path.sock or ws://IP:PORT")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := config.Ensure(); err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runtime := New(version, cwd)
	if !*inProcess {
		runtime.Workers = routes
	}
	defer runtime.Close()
	return runtime.ServeListen(ctx, *listen, origins, os.Stderr)
}

// TLSWarning is said when app-server listens beyond this machine.
const TLSWarning = "warning: listening beyond this machine without TLS; prefer a private network such as Tailscale."

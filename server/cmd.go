package server

import (
	"context"
	"flag"
	"fmt"
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
	web := fs.Bool("web", false, "serve the browser UI at / and WebSocket protocol at /ws")
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
	if *web {
		l, err := runtime.ListenWeb(ctx, *listen, origins)
		if err != nil {
			return err
		}
		defer l.Close()
		for _, u := range l.URLs {
			fmt.Fprintln(os.Stdout, u)
		}
		if l.Public {
			fmt.Fprintln(os.Stderr, WebWarning)
		}
		return l.Wait()
	}
	return runtime.ServeListen(ctx, *listen, origins, os.Stderr)
}

// WebWarning is said when the web UI listens beyond this machine.
const WebWarning = "warning: the web UI has no password: anyone who can reach this port can use atto (and run commands). Use it on a trusted LAN or over Tailscale."

// TLSWarning is said when app-server listens beyond this machine.
const TLSWarning = "warning: listening beyond this machine without TLS; prefer a private network such as Tailscale."

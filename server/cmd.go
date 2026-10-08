package server

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"

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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	runtime := New(version, cwd)
	if !*inProcess {
		runtime.Workers = routes
	}
	defer runtime.Close()
	return runtime.ServeListen(ctx, *listen, origins, os.Stderr)
}

// TLSWarning is said when the web client is served beyond this machine.
const TLSWarning = "warning: listening beyond this machine without TLS; prefer a private network such as Tailscale."

// WebLinks are the web client's links, token included, for a server
// listening on addr: one per host it can be reached at (see URLHosts).
func WebLinks(addr, token string) []string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil
	}
	var out []string
	for _, h := range URLHosts(addr) {
		out = append(out, "http://"+net.JoinHostPort(strings.Trim(h, "[]"), port)+"/#token="+token)
	}
	return out
}

// RunHTTP implements "atto serve": the protocol over HTTP + SSE plus the
// web client.
func RunHTTP(version string, args []string, out io.Writer) error {
	return RunHTTPWith(version, args, out, nil)
}

// RunHTTPWith accepts optional daemon worker routing.
func RunHTTPWith(version string, args []string, out io.Writer, routes *WorkerRoutes) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	var origins originFlags
	fs.Var(&origins, "allow-origin", "additional browser origin allowed on WebSocket (repeatable)")
	inProcess := fs.Bool("in-process", false, "run session runtimes in this process")
	listen := fs.String("listen", "127.0.0.1:7878", "address to listen on")
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
	token, err := LoadOrCreateToken()
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	defer ln.Close()
	runtime := New(version, cwd)
	if !*inProcess {
		runtime.Workers = routes
	}
	defer runtime.Close()

	addr := ln.Addr().String()
	banner(out, version, cwd, addr, token)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return serveHTTP(ctx, ln, runtime.HTTPHandlerOrigins(token, origins))
}

// banner says where atto serve listens: the web client's links, and
// beyond this machine the TLS warning and a QR code of the first link.
func banner(out io.Writer, version, cwd, addr, token string) {
	fmt.Fprintf(out, "atto %s serving %s\n", version, cwd)
	links := WebLinks(addr, token)
	if len(links) == 0 {
		links = []string{"http://" + addr + "/#token=" + token}
	}
	for i, l := range links {
		label := "  web:    "
		if i > 0 {
			label = "          "
		}
		fmt.Fprintln(out, label+l)
	}
	fmt.Fprintf(out, "  rpc:    POST http://%s/rpc   events: GET http://%s/events  (Authorization: Bearer <token>)\n", addr, addr)
	fmt.Fprintf(out, "  ws:     ws://%s/ws  (same token)\n", addr)
	fmt.Fprintf(out, "  token:  %s\n", TokenPath())
	if !IsLoopback(addr) {
		fmt.Fprintln(out, "  "+TLSWarning)
		if lines, err := QR(links[0]); err == nil {
			fmt.Fprintln(out)
			for _, l := range lines {
				fmt.Fprintln(out, "  "+l)
			}
		}
	}
}

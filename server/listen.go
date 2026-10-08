package server

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"
)

// ServeListen serves app-server's selected transport. Diagnostics never go
// to the protocol's stdout. Unix sockets are JSON lines, not WS-over-UDS.
func (s *Server) ServeListen(ctx context.Context, address string, origins []string, diagnostics io.Writer) error {
	if address == "stdio://" {
		return s.ServeStdio(ctx, os.Stdin, os.Stdout)
	}
	u, err := url.Parse(address)
	if err != nil {
		return err
	}
	switch u.Scheme {
	case "unix":
		if u.Host != "" || u.Path == "" || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("listen must be unix:///absolute/path.sock")
		}
		ln, err := net.Listen("unix", u.Path)
		if err != nil {
			return err
		} // never unlink an existing listener
		defer ln.Close() // UnixListener removes its own socket
		if err := os.Chmod(u.Path, 0o600); err != nil {
			return err
		}
		fmt.Fprintf(diagnostics, "atto app-server listening on unix://%s\n", u.Path)
		return s.serveListener(ctx, ln)
	case "ws":
		if u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("listen must be ws://IP:PORT")
		}
		ln, err := net.Listen("tcp", u.Host)
		if err != nil {
			return err
		}
		defer ln.Close()
		token := ""
		if !IsLoopback(ln.Addr().String()) {
			token, err = LoadOrCreateToken()
			if err != nil {
				return err
			}
		}
		fmt.Fprintf(diagnostics, "atto app-server listening on ws://%s/\n", ln.Addr())
		if token != "" {
			fmt.Fprintf(diagnostics, "  bearer token: %s\n  token: %s\n  %s\n", token, TokenPath(), TLSWarning)
		}
		return serveHTTP(ctx, ln, s.WebSocketHandler(token, origins))
	default:
		return fmt.Errorf("unsupported listen transport %q (use stdio://, unix:///path.sock or ws://IP:PORT)", u.Scheme)
	}
}

func (s *Server) serveListener(ctx context.Context, ln net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	go func() { <-ctx.Done(); ln.Close() }()
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		wg.Go(func() { defer conn.Close(); _ = s.ServeConn(ctx, conn) })
	}
}

func serveHTTP(ctx context.Context, ln net.Listener, handler http.Handler) error {
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			sh, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = srv.Shutdown(sh)
		case <-done:
		}
	}()
	err := srv.Serve(ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

type originFlags []string

func (f *originFlags) String() string { return fmt.Sprint([]string(*f)) }
func (f *originFlags) Set(s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("allow-origin must be an exact http(s) origin")
	}
	*f = append(*f, s)
	return nil
}

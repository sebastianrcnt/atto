// Command atto2 runs one read-only agent on a directory: a prototype of
// atto2's agent runtime.
//
//	atto2 [-dir path] [-base-url url] [-model id] "question"
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"atto2/agent"
	"atto2/cortex"
	"atto2/kernel"
	"atto2/machine"
	"atto2/model"
)

func main() {
	o := parseFlags()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, `usage: atto2 [-dir path] [-model id] "question"`)
		os.Exit(2)
	}
	k, err := kernel.WithGrant(*o.dir, grantNames(*o.grant)...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "atto2:", err)
		os.Exit(1)
	}
	m := machine.New(k)
	defer m.Close()
	a := newAgent(o, m)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	answer, err := a.Run(ctx, strings.Join(flag.Args(), " "))
	if *o.verbose {
		printSyscallLog(k)
	}
	err = writeMetrics(*o.metrics, a, answer, err)
	fmt.Fprintf(os.Stderr, "\n[%d steps, %d tokens in last prompt, %d syscalls]\n", a.Steps, a.Cortex.Tokens, len(k.Log))
	if err != nil {
		fmt.Fprintln(os.Stderr, "atto2:", err)
		os.Exit(1)
	}
	fmt.Println(answer)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type options struct {
	dir, baseURL, modelID, metrics, grant *string
	verbose, quiet                        *bool
	steps                                 *int
}

func parseFlags() options {
	var o options
	o.grant = flag.String("grant", "bash,now,exit", "comma-separated syscall names")
	o.dir = flag.String("dir", ".", "read-only project working directory for sys.bash")
	o.baseURL = flag.String("base-url", envOr("ATTO2_BASE_URL", "http://192.168.0.235:8081/v1"), "chat completions endpoint")
	o.modelID = flag.String("model", envOr("ATTO2_MODEL", "orca-local"), "model id")
	o.verbose = flag.Bool("v", false, "print syscall log at end")
	o.metrics = flag.String("metrics", "", "write run metrics as JSON to this path")
	o.quiet = flag.Bool("q", false, "hide Lua trace (agent stdout is still shown)")
	o.steps = flag.Int("steps", 30, "most model calls")
	flag.Parse()
	return o
}

func newAgent(o options, m *machine.Machine) *agent.Agent {
	a := &agent.Agent{
		Name:     "/root",
		Cortex:   cortex.New(cortex.Instructions(m.Kernel, *o.dir)),
		Machine:  m,
		Model:    &model.Client{BaseURL: *o.baseURL, Model: *o.modelID, APIKey: os.Getenv("ATTO2_API_KEY")},
		MaxSteps: *o.steps,
	}
	a.Trace = func(e agent.Event) {
		if e.Kind == "stdout" || !*o.quiet {
			trace(e)
		}
	}
	return a
}

func grantNames(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	names := strings.Split(value, ",")
	for i := range names {
		names[i] = strings.TrimSpace(names[i])
	}
	return names
}

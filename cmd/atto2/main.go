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
	input := strings.Join(flag.Args(), " ")
	if input == "" && *o.replay == "" {
		fmt.Fprintln(os.Stderr, `usage: atto2 [-grant names] [-dir path] [-model id] "question"`)
		os.Exit(2)
	}
	if err := execute(o, input); err != nil {
		fmt.Fprintln(os.Stderr, "atto2:", err)
		os.Exit(1)
	}
}

func execute(o options, input string) error {
	j, closeFile, err := openJournal(o, input)
	if err != nil {
		return err
	}
	defer closeFile()
	if j != nil {
		input = j.Header.Input
	}
	k, err := kernel.WithGrant(*o.dir, grantNames(*o.grant)...)
	if err != nil {
		return err
	}
	m := machine.New(k)
	defer m.Close()
	a := newAgent(o, m)
	if err := attachJournal(a, j, *o.replay != ""); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	answer, err := run(ctx, a, input, *o.baseline)
	if *o.verbose {
		printSyscallLog(k)
	}
	err = writeMetrics(*o.metrics, a, answer, err)
	fmt.Fprintf(os.Stderr, "\n[%d steps, %d tokens in last prompt, %d syscalls]\n", a.Steps, a.Cortex.Tokens, len(k.Log))
	if err == nil {
		fmt.Println(answer)
	}
	return err
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type options struct {
	dir, baseURL, modelID, metrics, grant, record, replay *string
	verbose, quiet, baseline                              *bool
	steps                                                 *int
}

func parseFlags() options {
	var o options
	o.record = flag.String("record", "", "record a life as JSONL")
	o.replay = flag.String("replay", "", "replay a recorded life without a model")
	o.grant = flag.String("grant", "bash,now,exit", "comma-separated syscall names")
	o.dir = flag.String("dir", ".", "read-only project working directory for sys.bash")
	o.baseURL = flag.String("base-url", envOr("ATTO2_BASE_URL", "http://192.168.0.235:8081/v1"), "chat completions endpoint")
	o.modelID = flag.String("model", envOr("ATTO2_MODEL", "orca-local"), "model id")
	o.baseline = flag.Bool("baseline", false, "one chat completion without tools")
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

func run(ctx context.Context, a *agent.Agent, input string, baseline bool) (string, error) {
	if !baseline {
		return a.Run(ctx, input)
	}
	reply, usage, err := a.Model.Complete(ctx, []model.Message{{Role: "user", Content: input}}, nil)
	a.Steps = 1
	a.Cortex.Tokens = usage.PromptTokens
	a.CompletionTokens = usage.CompletionTokens
	if reply.Reasoning != "" {
		a.Trace(agent.Event{Kind: "thinking", Text: reply.Reasoning})
	}
	return reply.Content, err
}

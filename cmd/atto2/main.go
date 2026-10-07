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
	"atto2/machine"
	"atto2/model"
)

func main() {
	dir := flag.String("dir", ".", "the directory the agent may read (its /)")
	baseURL := flag.String("base-url", envOr("ATTO2_BASE_URL", "http://192.168.0.235:8081/v1"), "chat completions endpoint")
	modelID := flag.String("model", envOr("ATTO2_MODEL", "orca-local"), "model id")
	quiet := flag.Bool("q", false, "print only the answer")
	steps := flag.Int("steps", 30, "most model calls")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, `usage: atto2 [-dir path] [-model id] "question"`)
		os.Exit(2)
	}
	m, err := machine.New(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "atto2:", err)
		os.Exit(1)
	}
	defer m.Close()
	a := &agent.Agent{
		Name:     "/root",
		Cortex:   cortex.New(cortex.Instructions(m.Pwd())),
		Machine:  m,
		Model:    &model.Client{BaseURL: *baseURL, Model: *modelID, APIKey: os.Getenv("ATTO2_API_KEY")},
		MaxSteps: *steps,
	}
	if !*quiet {
		a.Trace = trace
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	answer, err := a.Run(ctx, strings.Join(flag.Args(), " "))
	if err != nil {
		fmt.Fprintln(os.Stderr, "atto2:", err)
		os.Exit(1)
	}
	if *quiet {
		fmt.Println(answer)
	}
	fmt.Fprintf(os.Stderr, "\n[%d tokens in context]\n", a.Cortex.Tokens)
}

// trace shows a step on stderr, the answer on stdout.
func trace(e agent.Event) {
	switch e.Kind {
	case "thinking":
		fmt.Fprintf(os.Stderr, "\x1b[2m∴ %s\x1b[0m\n", oneLine(e.Text, 200))
	case "code":
		fmt.Fprintf(os.Stderr, "\x1b[36m▶ lua\x1b[0m\n%s\n", indent(e.Text))
	case "output":
		fmt.Fprintf(os.Stderr, "\x1b[2m%s\x1b[0m", indent(clip(e.Text, 30)))
	case "answer":
		fmt.Println("\n" + e.Text)
	}
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(strings.TrimSuffix(s, "\n"), "\n", "\n  ") + "\n"
}

func clip(s string, lines int) string {
	ls := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	if len(ls) <= lines {
		return s
	}
	return strings.Join(ls[:lines], "\n") + fmt.Sprintf("\n… %d more lines\n", len(ls)-lines)
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

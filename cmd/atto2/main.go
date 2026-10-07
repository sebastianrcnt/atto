// Command atto2 runs one read-only agent on a directory: a prototype of
// atto2's agent runtime.
//
//	atto2 [-dir path] [-base-url url] [-model id] "question"
package main

import (
	"context"
	"encoding/json"
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
	dir := flag.String("dir", ".", "read-only project working directory for sys.bash")
	baseURL := flag.String("base-url", envOr("ATTO2_BASE_URL", "http://192.168.0.235:8081/v1"), "chat completions endpoint")
	modelID := flag.String("model", envOr("ATTO2_MODEL", "orca-local"), "model id")
	verbose := flag.Bool("v", false, "print syscall log at end")
	metrics := flag.String("metrics", "", "write run metrics as JSON to this path")
	quiet := flag.Bool("q", false, "hide Lua trace (agent stdout is still shown)")
	steps := flag.Int("steps", 30, "most model calls")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, `usage: atto2 [-dir path] [-model id] "question"`)
		os.Exit(2)
	}
	k, err := kernel.Project(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "atto2:", err)
		os.Exit(1)
	}
	m := machine.New(k)
	defer m.Close()
	a := &agent.Agent{
		Name:     "/root",
		Cortex:   cortex.New(cortex.Instructions(k, *dir)),
		Machine:  m,
		Model:    &model.Client{BaseURL: *baseURL, Model: *modelID, APIKey: os.Getenv("ATTO2_API_KEY")},
		MaxSteps: *steps,
	}
	a.Trace = func(e agent.Event) {
		if e.Kind == "stdout" || !*quiet {
			trace(e)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	answer, err := a.Run(ctx, strings.Join(flag.Args(), " "))
	if *verbose {
		for _, e := range k.Log {
			args, _ := json.Marshal(e.Args)
			result, _ := json.Marshal(e.Result)
			if e.Error != "" {
				result, _ = json.Marshal(map[string]string{"error": e.Error})
			}
			fmt.Fprintf(os.Stderr, "%s sys.%s args=%s result=%s\n", e.Time.Format("2006-01-02T15:04:05.000Z07:00"), e.Name, oneLine(string(args), 200), oneLine(string(result), 300))
		}
		fmt.Fprintf(os.Stderr, "runs: %d pure, %d impure\n", k.PureRuns, k.ImpureRuns)
	}
	stats := map[string]any{"report": answer, "steps": a.Steps, "prompt_tokens": a.Cortex.Tokens, "syscalls": len(k.Log), "pure_runs": k.PureRuns, "impure_runs": k.ImpureRuns}
	if err != nil {
		stats["error"] = err.Error()
	}
	if *metrics != "" {
		raw, _ := json.MarshalIndent(stats, "", "  ")
		if writeErr := os.WriteFile(*metrics, raw, 0o600); writeErr != nil {
			fmt.Fprintln(os.Stderr, "atto2:", writeErr)
			if err == nil {
				err = writeErr
			}
		}
	}
	fmt.Fprintf(os.Stderr, "\n[%d steps, %d tokens in last prompt, %d syscalls]\n", a.Steps, a.Cortex.Tokens, len(k.Log))
	if err != nil {
		fmt.Fprintln(os.Stderr, "atto2:", err)
		os.Exit(1)
	}
	fmt.Println(answer)
}

// trace shows execution on stderr and the model's own stdout on stdout.
func trace(e agent.Event) {
	switch e.Kind {
	case "thinking":
		fmt.Fprintf(os.Stderr, "\x1b[2m∴ %s\x1b[0m\n", oneLine(e.Text, 200))
	case "code":
		fmt.Fprintf(os.Stderr, "\x1b[36m▶ lua\x1b[0m\n%s\n", indent(e.Text))
	case "output":
		fmt.Fprintf(os.Stderr, "\x1b[2m%s\x1b[0m", indent(clip(e.Text, 30)))
	case "stdout":
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

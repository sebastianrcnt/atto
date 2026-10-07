package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"atto2/agent"
	"atto2/kernel"
)

func printSyscallLog(k *kernel.Kernel) {
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

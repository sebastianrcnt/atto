package jobs

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

func superviseMonitor(ctx context.Context, dir string, j *Job, out io.Writer) (int, string, error) {
	m := j.Monitor
	var re *regexp.Regexp
	if m.Until != "" {
		re = regexp.MustCompile(m.Until)
	}
	j.Status, j.PID = Running, os.Getpid()
	if err := save(dir, *j); err != nil {
		return -1, "", err
	}
	every := max(m.Every, time.Second)
	var baseline *string
	start := time.Now()
	for n := 1; ; n++ {
		var buf strings.Builder
		budget := max(every, time.Minute)
		if m.Timeout > 0 {
			budget = min(budget, max(0, m.Timeout-time.Since(start)))
		}
		runCtx, cancel := context.WithTimeout(ctx, budget)
		code, _, err := run(runCtx, dir, j, j.Command, &buf, false)
		cancel()
		if err != nil {
			return -1, "", err
		}
		output := buf.String()
		fmt.Fprintf(out, "── check %d · %s · exit %d ──\n%s\n", n, time.Now().Format("15:04:05"), code, strings.TrimRight(output, "\n"))
		if ctx.Err() != nil {
			return -1, "", nil
		}
		if m.Timeout > 0 && time.Since(start) >= m.Timeout {
			return 1, fmt.Sprintf("timed out after %s without the condition holding", m.Timeout) + "\n" + lastLines(output, eventTail), nil
		}
		var why string
		switch {
		case re != nil && re.MatchString(output):
			why = fmt.Sprintf("output matched /%s/", m.Until)
		case m.UntilExit != nil && code == *m.UntilExit:
			why = fmt.Sprintf("command exited with %d", code)
		case m.OnChange && baseline != nil && output != *baseline:
			why = "output changed"
		}
		if baseline == nil {
			baseline = &output
		}
		if why != "" {
			return 0, why + "\n" + lastLines(output, eventTail), nil
		}
		if m.Timeout > 0 && time.Since(start) >= m.Timeout {
			return 1, fmt.Sprintf("timed out after %s without the condition holding", m.Timeout) + "\n" + lastLines(output, eventTail), nil
		}
		wait := every
		if m.Timeout > 0 {
			wait = min(wait, max(0, m.Timeout-time.Since(start)))
		}
		select {
		case <-ctx.Done():
			return -1, "", nil
		case <-time.After(wait):
		}
	}
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

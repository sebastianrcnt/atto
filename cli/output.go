package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"

	"github.com/sebastianrcnt/atto/outputs"
)

const outputUsage = `usage: atto output <path|call-id> [-head N] [-tail N] [-grep RE] [-i]

Prints the full output of a command that was cut in a tool result. Saved
output is compressed (zstd) under ~/.atto/outputs; this reads it, and the
plain log files older versions left. The argument is the path the result
named, or the id of the tool call.

-grep RE   only the lines matching the regular expression (-i: ignoring case)
-head N    only the first N lines (of those)
-tail N    only the last N lines (of those)`

// RunOutput implements "atto output".
func RunOutput(args []string, out io.Writer) error {
	flags := newFlags("output")
	head := flags.Int("head", 0, "first N lines")
	tail := flags.Int("tail", 0, "last N lines")
	grep := flags.String("grep", "", "regexp")
	fold := flags.Bool("i", false, "ignore case")
	// Flags may follow the path, as in the hint the model is given.
	var rest []string
	for {
		if err := flags.Parse(args); err != nil {
			return fmt.Errorf("%v\n%s", err, outputUsage)
		}
		if flags.NArg() == 0 {
			break
		}
		rest = append(rest, flags.Arg(0))
		args = flags.Args()[1:]
	}
	if len(rest) != 1 {
		return fmt.Errorf("%s", outputUsage)
	}
	if *head < 0 || *tail < 0 || (*head > 0 && *tail > 0) {
		return fmt.Errorf("atto output: use one of -head and -tail, with a positive number\n%s", outputUsage)
	}
	var re *regexp.Regexp
	if *grep != "" {
		pat := *grep
		if *fold {
			pat = "(?i)" + pat
		}
		var err error
		if re, err = regexp.Compile(pat); err != nil {
			return fmt.Errorf("atto output: %w", err)
		}
	}
	path, err := outputs.Resolve(rest[0], os.Getenv("ATTO_SESSION_ID"))
	if err != nil {
		return fmt.Errorf("atto output: %w", err)
	}
	f, err := outputs.Open(path)
	if err != nil {
		return fmt.Errorf("atto output: %w", err)
	}
	defer f.Close()
	return printLines(out, f, re, *head, *tail)
}

// printLines copies r to out, only the lines matching re (if not nil), and
// of those only the first head or the last tail (if positive).
func printLines(out io.Writer, r io.Reader, re *regexp.Regexp, head, tail int) error {
	if re == nil && head == 0 && tail == 0 {
		_, err := io.Copy(out, r)
		return err
	}
	br := bufio.NewReaderSize(r, 64<<10)
	var ring []string
	n := 0
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			if re == nil || re.MatchString(trimEOL(line)) {
				switch {
				case tail > 0:
					if len(ring) < tail {
						ring = append(ring, line)
					} else {
						ring[n%tail] = line
					}
					n++
				default:
					if _, werr := io.WriteString(out, line); werr != nil {
						return werr
					}
					if n++; head > 0 && n == head {
						return nil
					}
				}
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}
	for i := range ring {
		if _, err := io.WriteString(out, ring[(n+i)%len(ring)]); err != nil {
			return err
		}
	}
	return nil
}

func trimEOL(s string) string {
	if n := len(s); n > 0 && s[n-1] == '\n' {
		s = s[:n-1]
		if n := len(s); n > 0 && s[n-1] == '\r' {
			s = s[:n-1]
		}
	}
	return s
}

package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/sebastianrcnt/atto/session"
)

const historyUsage = `usage:
  atto history grep [-i] [-max N] [-active] [-session ID] <regexp>   search the full transcript (-max 0 = no cap)
  atto history show [-C N] [-full] [-session ID] <n>                 print entry #n (and N around it)

The session defaults to $ATTO_SESSION_ID (set for commands atto runs),
else the latest session in the current directory. The transcript includes
everything said before any compaction, and the branches the user left by
going back to an earlier message: those entries are marked "(other branch)"
and are not part of the current conversation. -active searches only the
current branch.`

// RunHistory implements "atto history".
func RunHistory(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", historyUsage)
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("history "+sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sessID := fs.String("session", os.Getenv("ATTO_SESSION_ID"), "session ID")
	ignoreCase := fs.Bool("i", false, "case-insensitive")
	maxHits := fs.Int("max", 40, "maximum matching lines (0 = no cap)")
	ctxN := fs.Int("C", 0, "entries of context around #n")
	full := fs.Bool("full", false, "do not truncate long entries")
	activeOnly := fs.Bool("active", false, "search only the active branch")
	if err := fs.Parse(rest); err != nil {
		return fmt.Errorf("%v\n%s", err, historyUsage)
	}
	if *maxHits < 0 {
		return fmt.Errorf("-max must not be negative (0 means no cap), got %d\n%s", *maxHits, historyUsage)
	}

	path, err := resolveSession(*sessID)
	if err != nil {
		return err
	}
	_, entries, err := session.Load(path)
	if err != nil {
		return err
	}
	items := session.Items(entries)
	if *activeOnly {
		var on []session.Item
		for _, it := range items {
			if !it.OffBranch {
				on = append(on, it)
			}
		}
		items = on
	}

	switch sub {
	case "grep":
		if fs.NArg() != 1 {
			return fmt.Errorf("%s", historyUsage)
		}
		pat := fs.Arg(0)
		if *ignoreCase {
			pat = "(?i)" + pat
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			return err
		}
		return historyGrep(out, items, re, *maxHits)
	case "show":
		var n int
		if fs.NArg() != 1 {
			return fmt.Errorf("%s", historyUsage)
		}
		if _, err := fmt.Sscan(strings.TrimPrefix(fs.Arg(0), "#"), &n); err != nil {
			return fmt.Errorf("bad entry number %q", fs.Arg(0))
		}
		return historyShow(out, items, n, *ctxN, *full)
	}
	return fmt.Errorf("unknown subcommand %q\n%s", sub, historyUsage)
}

func resolveSession(id string) (string, error) {
	if id != "" {
		return session.Find(id)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if s, ok := session.Latest(cwd); ok {
		return s.Path, nil
	}
	return "", fmt.Errorf("no session: set ATTO_SESSION_ID or pass -session")
}

// snippet trims a matching line to ~160 columns centered on the match.
func snippet(line string, loc []int) string {
	const width = 160
	if len(line) <= width {
		return line
	}
	start := max(0, loc[0]-width/2)
	end := min(len(line), start+width)
	s := line[start:end]
	if start > 0 {
		s = "…" + s
	}
	if end < len(line) {
		s += "…"
	}
	return s
}

func historyGrep(out io.Writer, items []session.Item, re *regexp.Regexp, maxHits int) error {
	hits, entries := 0, 0
	for _, it := range items {
		matched := false
		for line := range strings.SplitSeq(it.Text, "\n") {
			loc := re.FindStringIndex(line)
			if loc == nil {
				continue
			}
			if maxHits > 0 && hits >= maxHits {
				fmt.Fprintf(out, "[stopped after %d matching lines; narrow the pattern or raise -max]\n", maxHits)
				return nil
			}
			fmt.Fprintf(out, "#%d %s: %s\n", it.N, itemLabel(it), strings.TrimSpace(snippet(line, loc)))
			hits++
			matched = true
		}
		if matched {
			entries++
		}
	}
	if hits == 0 {
		fmt.Fprintln(out, "no matches")
		return nil
	}
	fmt.Fprintf(out, "[%d matching lines in %d entries; read one with: atto history show <n>]\n", hits, entries)
	return nil
}

const showLimit = 8000

func historyShow(out io.Writer, items []session.Item, n, ctxN int, full bool) error {
	shown := 0
	for _, it := range items {
		if it.N < n-ctxN || it.N > n+ctxN {
			continue
		}
		text := it.Text
		if !full && len(text) > showLimit {
			text = text[:showLimit] + fmt.Sprintf("\n[truncated %d bytes; pass -full for everything]", len(it.Text)-showLimit)
		}
		fmt.Fprintf(out, "── #%d %s ──\n%s\n\n", it.N, itemLabel(it), text)
		shown++
	}
	if shown == 0 && len(items) == 0 {
		return fmt.Errorf("no entry #%d (the session has no messages)", n)
	}
	if shown == 0 {
		return fmt.Errorf("no entry #%d (entries are numbered 1..%d)", n, items[len(items)-1].N)
	}
	return nil
}

// itemLabel marks entries on branches the user went back from.
func itemLabel(it session.Item) string {
	if it.OffBranch {
		return it.Label + " (other branch)"
	}
	return it.Label
}

package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/goal"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/session"
)

const sessionsUsage = `usage:
  atto sessions [list] [-all] [-archived] [-json] [-n N]   sessions of this directory, newest first
  atto sessions show <id>                                   details and the last user messages
  atto sessions rename <id> <name>                          name a session (like /name)
  atto sessions archive <id>                                move a session to the archive
  atto sessions unarchive <id>                              bring it back
  atto sessions delete [-y] <id>                            delete it for good, with its jobs,
                                                            inbox, goal and unshared images

-all lists every directory, -archived the archive, -json prints JSON.
An <id> may be a unique prefix. Resume one with: atto resume <id>.
The full transcript: atto history show -session <id> <n>.
Inside an atto agent only list and show are allowed.`

// Seams for tests: whether stdin is a terminal, and where answers come from.
var (
	sessionsStdin io.Reader = os.Stdin
	sessionsIsTTY           = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
)

// RunSessions implements "atto sessions".
func RunSessions(args []string, out io.Writer) error {
	sub := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	if sub == "help" {
		fmt.Fprintln(out, sessionsUsage)
		return nil
	}
	if config.InAgent() && sub != "list" && sub != "show" {
		return fmt.Errorf("an atto agent may only list and show sessions: %s would change or destroy the user's session history (%s is set)", sub, config.EnvAgent)
	}

	fs := newFlags("sessions " + sub)
	all := fs.Bool("all", false, "every directory")
	archived := fs.Bool("archived", false, "archived sessions")
	asJSON := fs.Bool("json", false, "JSON output")
	limit := fs.Int("n", 0, "show at most N sessions")
	yes := fs.Bool("y", false, "delete without asking")
	pos, err := parseInterleaved(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(out, sessionsUsage)
		return nil
	}
	if err != nil {
		return fmt.Errorf("%v\n%s", err, sessionsUsage)
	}

	switch sub {
	case "list":
		if len(pos) != 0 {
			return fmt.Errorf("%s", sessionsUsage)
		}
		return sessionsList(out, *all, *archived, *asJSON, *limit)
	case "show", "archive", "unarchive", "delete":
		if len(pos) != 1 {
			return fmt.Errorf("%s", sessionsUsage)
		}
		path, err := session.Find(pos[0])
		if err != nil {
			return err
		}
		switch sub {
		case "show":
			return sessionsShow(out, path)
		case "archive", "unarchive":
			return sessionsMove(out, path, sub == "archive")
		}
		return sessionsDelete(out, path, *yes)
	case "rename":
		if len(pos) < 2 {
			return fmt.Errorf("%s", sessionsUsage)
		}
		path, err := session.Find(pos[0])
		if err != nil {
			return err
		}
		return sessionsRename(out, path, strings.Join(pos[1:], " "))
	}
	return fmt.Errorf("unknown subcommand %q\n%s", sub, sessionsUsage)
}

// parseInterleaved parses flags that may come before, between or after
// positional words, so "delete <id> -y" works as well as "delete -y <id>".
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// oneLine collapses whitespace and cuts s to n runes.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

type sessionJSON struct {
	ID       string    `json:"id"`
	Name     string    `json:"name,omitempty"`
	Cwd      string    `json:"cwd"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
	Messages int       `json:"messages"`
	Preview  string    `json:"preview"`
	Archived bool      `json:"archived"`
	Path     string    `json:"path"`
	Running  bool      `json:"running,omitempty"` // left running in the background
}

func sessionsList(out io.Writer, all, archived, asJSON bool, limit int) error {
	cwd := ""
	if !all {
		var err error
		if cwd, err = os.Getwd(); err != nil {
			return err
		}
	}
	list, err := session.List(cwd, archived)
	if err != nil {
		return err
	}
	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	if asJSON {
		rows := make([]sessionJSON, 0, len(list)) // [] rather than null when empty
		for _, s := range list {
			rows = append(rows, sessionJSON{s.ID, s.Name, s.Cwd, s.Created, s.Updated, s.Messages, s.Preview, s.Archived, s.Path, s.Running > 0})
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	kind := "sessions"
	if archived {
		kind = "archived sessions"
	}
	if len(list) == 0 {
		if cwd != "" {
			if elsewhere, _ := session.List("", archived); len(elsewhere) > 0 {
				fmt.Fprintf(out, "No %s in this directory; %d in other directories (use -all).\n", kind, len(elsewhere))
				return nil
			}
		}
		fmt.Fprintf(out, "No %s.\n", kind)
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	running := false
	for _, s := range list {
		running = running || s.Running > 0
	}
	head := "ID\tUPDATED\tMSGS\tNAME\tPREVIEW"
	if running {
		head = "ID\tUPDATED\tMSGS\tSTATUS\tNAME\tPREVIEW"
	}
	if all {
		head += "\tCWD"
	}
	fmt.Fprintln(tw, head)
	for _, s := range list {
		name := oneLine(s.Name, 30)
		if name == "" {
			name = "-"
		}
		row := fmt.Sprintf("%s\t%s\t%d\t%s\t%s", s.ID, session.RelTime(s.Updated), s.Messages, name, oneLine(s.Preview, 60))
		if running {
			status := "-"
			if s.Running > 0 {
				status = "running"
			}
			row = fmt.Sprintf("%s\t%s\t%d\t%s\t%s\t%s", s.ID, session.RelTime(s.Updated), s.Messages, status, name, oneLine(s.Preview, 60))
		}
		if all {
			row += "\t" + s.Cwd
		}
		fmt.Fprintln(tw, row)
	}
	return tw.Flush()
}

const showUserMessages = 5

func sessionsShow(out io.Writer, path string) error {
	h, entries, err := session.Load(path)
	if err != nil {
		return err
	}
	sum := session.Summary{ID: h.ID, Created: h.Time, Updated: h.Time}
	var model string
	branches := 0
	for _, e := range entries {
		sum.Updated = e.Time
		switch e.Type {
		case session.TypeName:
			sum.Name = e.Name
		case session.TypeModel:
			model = e.Provider + "/" + e.Model
		case session.TypeBranch:
			branches++
		}
	}
	var users []string
	msgs := 0
	for _, e := range session.Active(entries) {
		if e.Type != session.TypeMessage || e.Message == nil {
			continue
		}
		switch e.Message.Role {
		case "user":
			msgs++
			users = append(users, e.Message.Content)
		case "assistant":
			msgs++
		}
	}
	status := "active"
	if !strings.HasPrefix(path, config.SessionsDir()) {
		status = "archived"
	}
	if l, ok := session.LockedBy(path); ok {
		if l.Kind == session.KindTUI {
			status += fmt.Sprintf(" (open in atto, pid %d)", l.PID)
		} else {
			status += fmt.Sprintf(" (running in the background, pid %d)", l.PID)
		}
	}
	f := func(k, v string) { fmt.Fprintf(out, "%-9s %s\n", k+":", v) }
	f("id", h.ID)
	f("status", status)
	f("cwd", h.Cwd)
	f("created", h.Time.Local().Format("2006-01-02 15:04")+" ("+session.RelTime(h.Time)+")")
	f("updated", sum.Updated.Local().Format("2006-01-02 15:04")+" ("+session.RelTime(sum.Updated)+")")
	if model != "" && model != "/" {
		f("model", model)
	}
	if sum.Name != "" {
		f("name", sum.Name)
	}
	f("messages", fmt.Sprint(msgs))
	if branches > 0 {
		f("branches", fmt.Sprint(branches+1))
	}
	f("file", path)
	if len(users) > 0 {
		last := users[max(0, len(users)-showUserMessages):]
		fmt.Fprintf(out, "\nlast %d of %d user messages:\n", len(last), len(users))
		for _, m := range last {
			fmt.Fprintf(out, "  > %s\n", oneLine(m, 200))
		}
	}
	fmt.Fprintf(out, "\nfull transcript: atto history show -session %s <n>   (search: atto history grep -session %s <regexp>)\n", h.ID, h.ID)
	return nil
}

// idOf is the session ID in a file name: <YYYYMMDD-HHMMSS>-<id>.jsonl.
func idOf(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	return base[strings.LastIndex(base, "-")+1:]
}

func isArchived(path string) bool {
	rel, err := filepath.Rel(config.ArchivedDir(), path)
	return err == nil && !strings.HasPrefix(rel, "..")
}

func sessionsMove(out io.Writer, path string, archive bool) error {
	id := idOf(path)
	if archive == isArchived(path) {
		if archive {
			return fmt.Errorf("session %s is already archived", id)
		}
		return fmt.Errorf("session %s is not archived", id)
	}
	var err error
	if archive {
		_, err = session.Archive(path)
	} else {
		_, err = session.Unarchive(path)
	}
	if err != nil {
		return err
	}
	if archive {
		fmt.Fprintf(out, "Archived session %s.\n", id)
	} else {
		fmt.Fprintf(out, "Unarchived session %s.\n", id)
	}
	return nil
}

func sessionsRename(out io.Writer, path, name string) error {
	name = strings.TrimSpace(name)
	if err := session.Rename(path, name); err != nil {
		return err
	}
	fmt.Fprintf(out, "Named session %s %q.\n", idOf(path), name)
	return nil
}

func sessionsDelete(out io.Writer, path string, yes bool) error {
	id := idOf(path)
	if id == "" || strings.ContainsAny(id, `/\.`) {
		return fmt.Errorf("refusing to delete %s: unexpected session id", path)
	}
	if id == os.Getenv("ATTO_SESSION_ID") {
		return fmt.Errorf("session %s is the one this command is running in", id)
	}
	if info, locked := session.LockedBy(path); locked {
		return session.LockError(info)
	}
	release, err := session.Lock(path)
	if err != nil {
		return err
	}
	defer release()
	active := jobs.ActiveCount(id)

	h, entries, err := session.Load(path)
	if err != nil {
		return err
	}
	msgs := 0
	for _, e := range session.Active(entries) {
		if e.Type == session.TypeMessage && e.Message != nil && (e.Message.Role == "user" || e.Message.Role == "assistant") {
			msgs++
		}
	}
	if !yes {
		if !sessionsIsTTY() {
			return fmt.Errorf("deleting is permanent: pass -y to confirm (no terminal to ask on)")
		}
		fmt.Fprintf(out, "Permanently delete session %s (%d messages, %s)", id, msgs, h.Cwd)
		if active > 0 {
			fmt.Fprintf(out, " and stop its %d running job(s)", active)
		}
		fmt.Fprint(out, "? [y/N] ")
		line, _ := bufio.NewReader(sessionsStdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintln(out, "Not deleted.")
			return nil
		}
	}

	// Stop jobs first: a supervisor still writing into the directory would
	// recreate it after the removal.
	if active > 0 {
		jobs.KillAll(id)
	}

	// Images this session used; scanned before the file is gone.
	used, err := imageRefs(path)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	for _, sidecar := range []string{session.LockPath(path), session.LogPath(path)} {
		if err := os.Remove(sidecar); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(out, "warning: %v\n", err)
		}
	}
	for _, dir := range []string{jobs.Root(id), events.Dir(id)} {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintf(out, "warning: %v\n", err)
		}
	}
	if err := os.Remove(goal.Path(id)); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(out, "warning: %v\n", err)
	}
	fmt.Fprintf(out, "Deleted session %s.\n", id)

	n, err := pruneImages(used)
	if err != nil {
		fmt.Fprintf(out, "warning: images not cleaned up: %v\n", err)
	} else if n > 0 {
		fmt.Fprintf(out, "Removed %d image(s) no other session uses.\n", n)
	}
	return nil
}

// imageName matches a stored image's file name: sha256 hex plus extension.
var imageName = regexp.MustCompile(`[0-9a-f]{64}\.[A-Za-z0-9]{2,5}`)

// imageRefs returns the image file names a session file mentions. It scans
// the raw text rather than decoding entries, so an image is counted wherever
// it appears (a message, a compaction's replacement history, a future entry
// type): keeping an image too long is harmless, deleting a used one is not.
func imageRefs(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	refs := map[string]bool{}
	for _, m := range imageName.FindAll(data, -1) {
		refs[string(m)] = true
	}
	return refs, nil
}

// pruneImages deletes the candidate images that no remaining session,
// active or archived, references. If any session can't be read the answer
// is unknown, so nothing is deleted. Only candidates (the deleted session's
// own images) are considered: other orphans, and images a running session
// has saved but not yet written to its file, are left alone.
func pruneImages(candidates map[string]bool) (int, error) {
	if len(candidates) == 0 {
		return 0, nil
	}
	for _, root := range []string{config.SessionsDir(), config.ArchivedDir()} {
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
				return nil
			}
			refs, err := imageRefs(p)
			if err != nil {
				return err
			}
			for name := range refs {
				delete(candidates, name)
			}
			return nil
		})
		if err != nil {
			return 0, err
		}
	}
	n := 0
	for name := range candidates {
		err := os.Remove(filepath.Join(images.Dir(), name))
		if err == nil {
			n++
		} else if !os.IsNotExist(err) {
			return n, err
		}
	}
	return n, nil
}

package cli

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/maint"
	"github.com/sebastianrcnt/atto/update"
)

func refuseMaintenance() error {
	if config.InAgentCommand() || os.Getenv("ATTO_SESSION_ID") != "" {
		return errors.New("restore, clean and uninstall cannot run inside an atto model shell (ATTO_AGENT/ATTO_SESSION_ID is set); run them from your own terminal")
	}
	return nil
}
func RunBackup(args []string, out io.Writer) error {
	fs := newFlags("backup")
	output := fs.String("o", "", "output .tar.zst file (outside ATTO_DIR)")
	secrets := fs.Bool("with-secrets", false, "include auth.json and server-token; keep archive private")
	cache := fs.Bool("include-cache", false, "include rebuildable cache/")
	force := fs.Bool("force", false, "warn rather than refuse active data (may be inconsistent)")
	words, e := parseInterleaved(fs, args)
	if e != nil || len(words) != 0 {
		return fmt.Errorf("usage: atto backup [-o FILE] [-with-secrets] [-include-cache] [-force]")
	}
	_, e = maint.Backup(maint.BackupOptions{Output: *output, WithSecrets: *secrets, IncludeCache: *cache, Force: *force, Version: update.Current(), Out: out})
	return e
}
func RunRestore(args []string, out io.Writer) error {
	if e := refuseMaintenance(); e != nil {
		return e
	}
	fs := newFlags("restore")
	into := fs.String("into", "", "destination ATTO_DIR")
	force := fs.Bool("force", false, "preserve existing directory before replacement")
	worktrees := fs.Bool("worktrees", false, "recreate recorded branches in repositories that exist")
	words, e := parseInterleaved(fs, args)
	if e != nil || len(words) > 1 || len(words) == 0 && !*worktrees {
		return errors.New("usage: atto restore FILE [-into DIR] [-force] [-worktrees]\n       atto restore -worktrees [-into DIR]")
	}
	file := ""
	if len(words) > 0 {
		file = words[0]
	}
	_, e = maint.Restore(file, maint.RestoreOptions{Root: *into, Force: *force, Worktrees: *worktrees, Out: out})
	return e
}
func parseOlder(s string) (time.Duration, error) {
	if before, ok := strings.CutSuffix(s, "d"); ok {
		n, e := strconv.ParseFloat(before, 64)
		if e != nil || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 || n > float64((1<<63-1)/int64(24*time.Hour)) {
			return 0, errors.New("-older must be a positive duration, e.g. 30d or 24h")
		}
		return time.Duration(n * float64(24*time.Hour)), nil
	}
	d, e := time.ParseDuration(s)
	if e != nil || d <= 0 {
		return 0, errors.New("-older must be a positive duration, e.g. 30d or 24h")
	}
	return d, nil
}
func RunClean(args []string, out io.Writer) error {
	if e := refuseMaintenance(); e != nil {
		return e
	}
	fs := newFlags("clean")
	yes := fs.Bool("y", false, "remove without confirmation")
	older := fs.String("older", "30d", "minimum age for orphan outputs and debug dumps")
	dry := fs.Bool("dry-run", false, "show table without removal")
	words, e := parseInterleaved(fs, args)
	if e != nil || len(words) != 0 {
		return errors.New("usage: atto clean [-y] [-older 30d] [-dry-run]")
	}
	age, e := parseOlder(*older)
	if e != nil {
		return e
	}
	_, e = maint.Clean(maint.CleanOptions{Older: age, Yes: *yes, DryRun: *dry, Out: out, Confirm: maint.Prompt(os.Stdin, out)})
	return e
}
func RunUninstall(args []string, out io.Writer) error {
	if e := refuseMaintenance(); e != nil {
		return e
	}
	fs := newFlags("uninstall")
	yes := fs.Bool("y", false, "confirm uninstall and default backup (dirty worktrees/branches still require explicit confirmation)")
	keep := fs.Bool("keep-data", false, "retain ATTO_DIR")
	noBackup := fs.Bool("no-backup", false, "skip the default backup")
	words, e := parseInterleaved(fs, args)
	if e != nil || len(words) != 0 {
		return errors.New("usage: atto uninstall [-y] [-keep-data] [-no-backup]")
	}
	var confirm func(string, bool) bool
	if !*yes {
		confirm = maint.Prompt(os.Stdin, out)
	}
	return maint.Uninstall(maint.UninstallOptions{Yes: *yes, KeepData: *keep, NoBackup: *noBackup, Version: update.Current(), Out: out, Confirm: confirm})
}

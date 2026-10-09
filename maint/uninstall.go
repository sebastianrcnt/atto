package maint

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/daemon"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/shell"
)

type UninstallOptions struct {
	Root, Temp, Binary, Version, BackupFile string
	Yes, KeepData, NoBackup                 bool
	Out                                     io.Writer
	Confirm                                 func(string, bool) bool
}

// StopRuntime stops the daemon and recorded supervisors, then non-daemon frontends.
func StopRuntime(root string, out io.Writer) error {
	old, had := os.LookupEnv(config.EnvDir)
	os.Setenv(config.EnvDir, root)
	defer func() {
		if had {
			os.Setenv(config.EnvDir, old)
		} else {
			os.Unsetenv(config.EnvDir)
		}
	}()
	if e := daemon.Stop(true); e != nil && !errors.Is(e, daemon.ErrUnavailable) {
		return e
	}
	dirs, e := os.ReadDir(filepath.Join(root, "jobs"))
	if e != nil && !os.IsNotExist(e) {
		return e
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		for _, j := range jobs.List(d.Name()) {
			if j.Active() {
				if _, e = jobs.Kill(d.Name(), j.ID); e != nil {
					return e
				}
				fmt.Fprintf(out, "Stopped job %s/%d\n", d.Name(), j.ID)
			}
		}
	}
	paths, e := sessionPaths(root)
	if e != nil {
		return e
	}
	for _, p := range paths {
		info, busy := session.LockedBy(p)
		if !busy {
			continue
		}
		if info.PID <= 0 || info.PID == os.Getpid() || !isAttoProcess(info.PID) {
			return fmt.Errorf("session %s is locked by unrecognized pid %d; close it before uninstalling", p, info.PID)
		}
		if e := shell.Terminate(info.PID); e != nil {
			return e
		}
		fmt.Fprintf(out, "Stopped session process %d\n", info.PID)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		active, e := Activity(root)
		if e != nil {
			return e
		}
		if len(active) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("processes still active: %s; data kept", strings.Join(active, ", "))
		}
		time.Sleep(100 * time.Millisecond)
	}
}
func Uninstall(o UninstallOptions) error {
	if o.Root == "" {
		o.Root = config.Dir()
	}
	if o.Temp == "" {
		o.Temp = os.TempDir()
	}
	var e error
	o.Root, e = filepath.Abs(o.Root)
	if e != nil {
		return e
	}
	if e = safeRoot(o.Root); e != nil {
		return e
	}
	if o.Binary == "" {
		o.Binary, e = os.Executable()
		if e != nil {
			return e
		}
	}
	out := writer(o.Out)
	fmt.Fprintf(out, "Uninstall atto\nBinary: %s\nData: %s (keep: %t)\n", o.Binary, o.Root, o.KeepData)
	if !o.Yes && (o.Confirm == nil || !o.Confirm("Uninstall atto?", false)) {
		fmt.Fprintln(out, "Cancelled.")
		return nil
	}
	if !o.NoBackup {
		yes := o.Yes || o.Confirm != nil && o.Confirm("Create a backup before uninstalling (credentials excluded)?", true)
		if yes {
			fmt.Fprintln(out, "Stopping runtime for a consistent backup.")
			if e = StopRuntime(o.Root, out); e != nil {
				return e
			}
			if _, e = Backup(BackupOptions{Root: o.Root, Output: o.BackupFile, Version: o.Version, Out: out}); e != nil {
				return fmt.Errorf("backup failed; nothing removed: %w", e)
			}
		}
	}
	if e = StopRuntime(o.Root, out); e != nil {
		return e
	}
	records, e := Worktrees(o.Root)
	if e != nil {
		return e
	}
	repos := map[string]bool{}
	keep := []string{}
	var failures []error
	for _, w := range records {
		if w.Repo != "" {
			repos[w.Repo] = true
		}
		if !within(filepath.Join(o.Root, "worktrees"), w.Path) {
			fmt.Fprintln(out, "Kept worktree outside ATTO_DIR:", w.Path)
			continue
		}
		if _, e = os.Stat(w.Path); os.IsNotExist(e) {
			continue
		}
		dirt, e := git(w.Path, "status", "--porcelain")
		if e != nil {
			keep = append(keep, w.Path)
			fmt.Fprintln(out, "Kept uninspectable worktree:", w.Path)
			continue
		}
		args := []string{"worktree", "remove"}
		if dirt != "" {
			fmt.Fprintf(out, "Dirty worktree %s:\n%s\n", w.Path, dirt)
			if o.Confirm == nil || !o.Confirm("Discard these uncommitted changes and remove this worktree?", false) {
				keep = append(keep, w.Path)
				continue
			}
			args = append(args, "--force")
		}
		if _, e = git(w.Repo, append(args, w.Path)...); e != nil {
			keep = append(keep, w.Path)
			failures = append(failures, e)
			fmt.Fprintln(out, "Could not remove worktree:", e)
		} else {
			fmt.Fprintln(out, "Removed worktree:", w.Path)
		}
	}
	for repo := range repos {
		branches, e := git(repo, "for-each-ref", "--format=%(refname:short)", "refs/heads/atto/")
		if e != nil {
			fmt.Fprintln(out, "Could not list branches:", e)
			continue
		}
		if branches == "" {
			continue
		}
		fmt.Fprintf(out, "Atto branches in %s:\n%s\n", repo, branches)
		if o.Confirm != nil && o.Confirm("Delete these atto/* branches (including unmerged commits)?", false) {
			for branch := range strings.SplitSeq(branches, "\n") {
				if _, e = git(repo, "branch", "-D", branch); e != nil {
					failures = append(failures, e)
					fmt.Fprintln(out, e)
				}
			}
		} else {
			fmt.Fprintln(out, "Branches kept; merge or delete them manually.")
		}
	}
	if !o.KeepData {
		if len(keep) == 0 {
			e = removeTree(o.Root)
		} else {
			fmt.Fprintln(out, "Preserving retained worktrees and their metadata; removing other data.")
			e = removeExceptWorktrees(o.Root)
		}
		if e != nil {
			failures = append(failures, e)
			fmt.Fprintln(out, "Could not remove data:", e)
		} else {
			fmt.Fprintln(out, "Removed atto data (except explicitly retained worktrees, if any).")
		}
	} else {
		fmt.Fprintln(out, "Atto data retained.")
	}
	// Uninstall's temp pass does not age-filter runtime files, but still protects tests for a day.
	plan, e := PlanClean(CleanOptions{Root: o.Root, Temp: o.Temp, Out: out})
	if e != nil {
		fmt.Fprintln(out, "Could not inventory temp files:", e)
		failures = append(failures, e)
	} else {
		for _, i := range plan.Items {
			if !within(o.Temp, i.Path) || within(o.Root, i.Path) {
				continue
			}
			if e = removeTree(i.Path); e != nil {
				failures = append(failures, e)
			}
		}
		for _, i := range plan.Kept {
			if within(o.Temp, i.Path) {
				fmt.Fprintf(out, "Kept %s: %s\n", i.Path, i.Reason)
			}
		}
	}
	for _, artifact := range installerArtifacts(o.Binary) {
		if fileInUse(artifact) {
			fmt.Fprintln(out, "Kept active installer artifact:", artifact)
			continue
		}
		if e = removeExecutable(artifact); e != nil && !os.IsNotExist(e) {
			failures = append(failures, e)
			fmt.Fprintln(out, "Could not remove installer artifact:", e)
		}
	}
	if e = undoInstallerPath(o.Binary); e != nil {
		failures = append(failures, e)
		fmt.Fprintln(out, e)
	}
	if e = removeExecutable(o.Binary); e != nil && !os.IsNotExist(e) {
		failures = append(failures, e)
		fmt.Fprintln(out, "Could not remove binary:", e)
	} else {
		fmt.Fprintln(out, "Binary removed (on Windows, running exe deletion may finish after exit).")
	}
	fmt.Fprintln(out, "Uninstall complete. Shell startup files are unchanged: install.sh never edits them. Remove any PATH entries you added manually. Open a new terminal to refresh PATH.")
	return errors.Join(failures...)
}
func removeExceptWorktrees(root string) error {
	entries, e := os.ReadDir(root)
	if e != nil {
		return e
	}
	var errs []error
	for _, d := range entries {
		switch d.Name() {
		case "worktrees", "agent-state", "subagents":
			continue
		}
		if e = removeTree(filepath.Join(root, d.Name())); e != nil {
			errs = append(errs, e)
		}
	}
	return errors.Join(errs...)
}

// Validate that maintenance never follows a directory alias when recursing.
func rawWithin(root, p string) bool {
	rel, e := filepath.Rel(root, p)
	return e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
func noSymlinkParents(root, p string) bool {
	root, e := filepath.Abs(root)
	if e != nil {
		return false
	}
	p, e = filepath.Abs(p)
	if e != nil {
		return false
	}
	relative := ""
	if rawWithin(root, p) {
		relative, e = filepath.Rel(root, p)
	} else if resolved := canonical(root); rawWithin(resolved, p) {
		relative, e = filepath.Rel(resolved, p)
	} else {
		return false
	}
	if e != nil {
		return false
	}
	p = filepath.Join(root, relative)
	for p = filepath.Dir(p); p != root; p = filepath.Dir(p) {
		if !rawWithin(root, p) {
			return false
		}
		st, e := os.Lstat(p)
		if e == nil && st.Mode()&fs.ModeSymlink != 0 {
			return false
		}
	}
	return true
}

func installerArtifacts(binary string) []string {
	out := []string{binary + ".old", binary + ".new"}
	old, _ := filepath.Glob(binary + ".old-*")
	for _, p := range old {
		suffix := strings.TrimPrefix(p, binary+".old-")
		valid := suffix != ""
		for _, r := range suffix {
			if r < '0' || r > '9' {
				valid = false
			}
		}
		if valid {
			out = append(out, p)
		}
	}
	return out
}

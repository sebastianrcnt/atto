package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/sebastianrcnt/atto/update"
)

// thisBinary is the running executable, symlinks resolved.
func thisBinary() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// RunUpdate implements "atto update [-check]": the newest release on this
// binary's own channel. Changing channel is `atto channel`.
func RunUpdate(args []string, out io.Writer) error {
	exe, err := thisBinary()
	if err != nil {
		return err
	}
	return runUpdate(args, out, update.Current(), exe)
}

func runUpdate(args []string, out io.Writer, cur, exe string) error {
	fs := newFlags("update")
	variant := fs.String("variant", update.Variant, "binary variant: slim or full")
	checkOnly := fs.Bool("check", false, "only report whether a newer release exists")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return fmt.Errorf("usage: atto update [-check] [-variant slim|full]")
	}
	if *variant != "slim" && *variant != "full" {
		return fmt.Errorf("unknown variant %q; use slim or full", *variant)
	}
	switchingVariant := *variant != update.Variant
	channel := update.Channel
	if channel == "" {
		channel = update.Stable // a dev build has no channel; stable is the default
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	rel, err := update.Latest(ctx, channel)
	if err != nil {
		return err
	}
	install, _ := update.Plan(rel, cur, false)
	if switchingVariant {
		install = true
	}
	if !install {
		fmt.Fprintln(out, latestLine(cur))
		return nil
	}
	if *checkOnly {
		if switchingVariant {
			fmt.Fprintf(out, "atto %s · %s (%s) is available (atto update -variant %s)\n", cur, rel.Version, *variant, *variant)
		} else {
			fmt.Fprintln(out, availableLine(cur, rel.Version))
		}
		return nil
	}
	if err := installToVariant(ctx, out, rel, cur, exe, *variant); err != nil {
		return err
	}
	fmt.Fprintln(out, updatedLine(cur, rel.Version))
	if switchingVariant {
		fmt.Fprintf(out, "Switched variant: %s → %s.\n", update.Variant, *variant)
	}
	fmt.Fprintln(out, "Running sessions keep the old version until restarted.")
	return nil
}

// installTo replaces exe with rel's binary, after the checks both update and
// channel share.
func installTo(ctx context.Context, out io.Writer, rel update.Release, cur, exe string) error {
	return installToVariant(ctx, out, rel, cur, exe, update.Variant)
}

func installToVariant(ctx context.Context, out io.Writer, rel update.Release, cur, exe, variant string) error {
	if how := update.Managed(exe); how != "" {
		return fmt.Errorf("%s was installed by another tool; update it with: %s", exe, how)
	}
	fmt.Fprintf(out, "Updating %s from %s to %s…\n", exe, cur, rel.Version)
	if err := update.InstallReleaseVariant(ctx, rel.Tag, exe, variant); err != nil {
		if os.IsPermission(err) {
			return fmt.Errorf("%w\n%s isn't writable; reinstall with: %s", err, filepath.Dir(exe), update.Install)
		}
		return err
	}
	return nil
}

// The three outcomes of an update check, kept as functions so the wording is
// tested without a network. cur is "dev" for builds without a release tag.

func latestLine(cur string) string { return fmt.Sprintf("atto %s (latest)", cur) }

func availableLine(cur, tag string) string {
	return fmt.Sprintf("atto %s \u00b7 %s is available (atto update)", cur, tag)
}

func updatedLine(cur, tag string) string { return fmt.Sprintf("Updated atto %s \u2192 %s", cur, tag) }

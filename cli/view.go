package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/images"
	"github.com/sebastianrcnt/atto/provider"
)

const viewUsage = `usage: atto view <image>...

Lets the model see image files (a screenshot, a rendered plot): run from
the agent's shell, it attaches each image to the result of the command it
ran in. PNG, JPEG, GIF and WebP; images over 2048 pixels are scaled down,
and a command attaches at most 8.`

// RunView implements "atto view". It leaves the images in the directory
// the agent gave the command (config.EnvView); the agent attaches them to
// the command's result when it ends.
func RunView(args []string, out io.Writer) error {
	flags := newFlags("view")
	if err := flags.Parse(args); err != nil || flags.NArg() == 0 {
		return fmt.Errorf("%s", viewUsage)
	}
	dir := os.Getenv(config.EnvView)
	if dir == "" {
		return fmt.Errorf("atto view: works only in a command the atto agent runs in the foreground (%s is not set), not in a background job or your own shell", config.EnvView)
	}
	// Check them all first, so that a bad path attaches nothing.
	var names []string
	var ims []provider.Image
	for _, path := range flags.Args() {
		im, err := images.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("atto view: %s: no such file", path)
		}
		if err != nil {
			return fmt.Errorf("atto view: %s: %w", path, err)
		}
		names = append(names, filepath.Base(path))
		ims = append(ims, im)
	}
	if len(ims) > images.MaxViewed {
		return fmt.Errorf("atto view: at most %d images per command", images.MaxViewed)
	}
	for i, im := range ims {
		if err := images.Drop(dir, names[i], im); err != nil {
			if errors.Is(err, images.ErrNoViewDir) {
				return fmt.Errorf("atto view: %w; run atto view in a foreground command", err)
			}
			return fmt.Errorf("atto view: %s: %w", names[i], err)
		}
		fmt.Fprintf(out, "attached %s (%d×%d) for you to see\n", names[i], im.Width, im.Height)
	}
	return nil
}

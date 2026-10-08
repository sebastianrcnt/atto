package images

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/provider"
)

// MaxViewed bounds the images one command can attach with "atto view".
const MaxViewed = 8

// viewed is an image "atto view" leaves in a command's view directory.
type viewed struct {
	Name string `json:"name"`
	Data []byte `json:"data"`
}

// ErrNoViewDir means the view directory is gone: the command that was
// given it has ended (or moved to the background).
var ErrNoViewDir = errors.New("the command this ran in has ended")

// Drop leaves im, prepared from the file name, in dir for the agent to
// attach to the command's result. The file appears whole or not at all.
func Drop(dir, name string, im provider.Image) error {
	return drop(dir, name, im, time.Now())
}

func drop(dir, name string, im provider.Image, now time.Time) error {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return ErrNoViewDir
	}
	files := viewFiles(dir)
	if len(files) >= MaxViewed {
		return fmt.Errorf("at most %d images per command", MaxViewed)
	}
	data, err := json.Marshal(viewed{Name: name, Data: im.Data})
	if err != nil {
		return err
	}
	// Keep sequential drops ordered even when the clock has not advanced
	// (Windows clocks can have a much coarser resolution than UnixNano).
	stamp := now.UnixNano()
	if len(files) > 0 {
		last, _, _ := strings.Cut(filepath.Base(files[len(files)-1]), "-")
		if previous, err := strconv.ParseInt(last, 10, 64); err == nil {
			stamp = max(stamp, previous+1)
		}
	}
	f, err := os.CreateTemp(dir, fmt.Sprintf("%020d-*.tmp", stamp))
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), strings.TrimSuffix(f.Name(), ".tmp")+".json")
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// viewFiles are the images in dir, oldest first.
func viewFiles(dir string) []string {
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	slices.Sort(files)
	return files
}

// Collect reads the images Drop left in dir, at most MaxViewed, prepared
// again (a file that is not an image is skipped and reported).
func Collect(dir string) ([]provider.Image, error) {
	var out []provider.Image
	var errs []error
	for _, path := range viewFiles(dir) {
		if len(out) == MaxViewed {
			break
		}
		raw, err := os.ReadFile(path)
		var v viewed
		if err == nil {
			err = json.Unmarshal(raw, &v)
		}
		var im provider.Image
		if err == nil {
			im, err = Prepare(v.Data)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", filepath.Base(path), err))
			continue
		}
		im.Name = v.Name
		out = append(out, im)
	}
	return out, errors.Join(errs...)
}

// ViewLabel describes an image atto view attached, e.g.
// "shot.png 1136×1038".
func ViewLabel(im provider.Image) string {
	name := im.Name
	if name == "" {
		name = "image"
	}
	return fmt.Sprintf("%s %d×%d", name, im.Width, im.Height)
}

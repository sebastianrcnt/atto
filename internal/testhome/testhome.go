// Package testhome gives a test binary a private home directory without
// moving Go's own caches into it.
package testhome

import (
	"go/build"
	"io/fs"
	"os"
	"path/filepath"
)

// Use makes a temporary home for the test binary, points HOME and
// USERPROFILE at it and returns a function that removes it. Go's module
// cache, build cache and env file default to paths under the home, so a
// `go build` run by a test would otherwise download modules into the
// temporary home, whose read-only files os.RemoveAll cannot delete: every
// run leaked about 150 MB. They keep their real locations.
func Use() (func(), error) {
	keep := map[string]string{}
	if os.Getenv("GOPATH") == "" && build.Default.GOPATH != "" {
		keep["GOPATH"] = build.Default.GOPATH
	}
	if dir, err := os.UserCacheDir(); err == nil && os.Getenv("GOCACHE") == "" {
		keep["GOCACHE"] = filepath.Join(dir, "go-build")
	}
	if dir, err := os.UserConfigDir(); err == nil && os.Getenv("GOENV") == "" {
		keep["GOENV"] = filepath.Join(dir, "go", "env")
	}
	home, err := os.MkdirTemp("", "atto-home")
	if err != nil {
		return nil, err
	}
	for k, v := range keep {
		os.Setenv(k, v)
	}
	os.Setenv("HOME", home)
	os.Setenv("USERPROFILE", home)
	return func() { RemoveAll(home) }, nil
}

// RemoveAll removes dir even when it holds read-only directories, as Go's
// module cache does.
func RemoveAll(dir string) error {
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
	return os.RemoveAll(dir)
}

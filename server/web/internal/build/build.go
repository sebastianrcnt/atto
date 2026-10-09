package build

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"sort"
	"strings"
)

func SourceHash(root fs.FS) (string, error) {
	files := []string{"gen.go", "internal/build/build.go"}
	err := fs.WalkDir(root, "src", func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	h := sha256.New()
	for _, f := range files {
		b, e := fs.ReadFile(root, f)
		if e != nil {
			return "", e
		}
		h.Write([]byte(f + "\x00"))
		h.Write([]byte(strings.ReplaceAll(string(b), "\r\n", "\n")))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

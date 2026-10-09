//go:build ignore

// Go-only build. Tailwind's standalone release is pinned and checksum verified;
// esbuild is the repo's Go dependency. Normal builds use committed dist offline.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/sebastianrcnt/atto/server/web/internal/build"
)

const version = "v4.3.3"

var pins = map[string][2]string{
	"darwin/arm64":  {"tailwindcss-macos-arm64", "cdf646702987a743464dff4d9c60fd4480d1c1e73dd819a9a67f1078815dce9d"},
	"darwin/amd64":  {"tailwindcss-macos-x64", "7922e0953f2110c05976e3bf58f14e643d90427575e766b7d433f5f80cbee7e1"},
	"linux/arm64":   {"tailwindcss-linux-arm64", "55fd0b241214eff3de1e8ee4f22796662f2d2e7a49bcfca7477cfd0bac398195"},
	"linux/amd64":   {"tailwindcss-linux-x64", "dc61b3ac6b8c9ca874c0cc4c57b2409791a64c5540404ca5f5367360babc313a"},
	"windows/amd64": {"tailwindcss-windows-x64.exe", "e0e260ce048014e9268f6237ff18f8ccf02cef521cbd0ae04e82c2cdf7aa3955"},
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	pin, ok := pins[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return fmt.Errorf("no standalone Tailwind pin for this platform")
	}
	cache := os.Getenv("ATTO_WEBGEN_CACHE")
	if cache == "" {
		base, e := os.UserCacheDir()
		if e != nil {
			return e
		}
		cache = filepath.Join(base, "atto-webgen")
	}
	if e := os.MkdirAll(cache, 0755); e != nil {
		return e
	}
	tw := filepath.Join(cache, version+"-"+pin[0])
	b, _ := os.ReadFile(tw)
	if sum(b) != pin[1] {
		c := http.Client{Timeout: 5 * time.Minute}
		r, e := c.Get("https://github.com/tailwindlabs/tailwindcss/releases/download/" + version + "/" + pin[0])
		if e != nil {
			return e
		}
		defer r.Body.Close()
		if r.StatusCode != 200 {
			return fmt.Errorf("download: %s", r.Status)
		}
		b, e = io.ReadAll(io.LimitReader(r.Body, 150<<20))
		if e != nil {
			return e
		}
		if sum(b) != pin[1] {
			return fmt.Errorf("Tailwind checksum mismatch")
		}
		if e = os.WriteFile(tw, b, 0755); e != nil {
			return e
		}
	}
	if e := os.MkdirAll("dist", 0755); e != nil {
		return e
	}
	cmd := exec.Command(tw, "-i", "src/app.css", "-o", "dist/app.css", "--minify")
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if e := cmd.Run(); e != nil {
		return e
	}
	res := api.Build(api.BuildOptions{EntryPoints: []string{"src/main.ts"}, Bundle: true, Write: false, Outfile: "app.js", Format: api.FormatESModule, Target: api.ES2020, MinifyWhitespace: true, MinifyIdentifiers: true, MinifySyntax: true, LegalComments: api.LegalCommentsEndOfFile, LogLevel: api.LogLevelWarning})
	if len(res.Errors) > 0 {
		return fmt.Errorf("esbuild errors")
	}
	if e := os.WriteFile("dist/app.js", res.OutputFiles[0].Contents, 0644); e != nil {
		return e
	}
	b, e := os.ReadFile("src/index.html")
	if e != nil {
		return e
	}
	if e = os.WriteFile("dist/index.html", b, 0644); e != nil {
		return e
	}
	hash, e := build.SourceHash(os.DirFS("."))
	if e != nil {
		return e
	}
	return os.WriteFile("dist/source.sha256", []byte(hash+"\n"), 0644)
}

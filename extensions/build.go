//go:build !noext

package extensions

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

// target is the newest syntax goja runs unchanged. goja has async/await
// natively but no async generators (ES2018), so esbuild lowers those and
// everything newer (class fields, ??, ?., private members...).
const target = api.ES2017

// Bundle compiles entry, a TypeScript or JavaScript file, together with
// the files it imports into one CommonJS script that goja runs. Types are
// erased, not checked. The script carries an inline source map, so stack
// traces name the original files and lines. Errors read
// "file:line:col: message", one per line.
func Bundle(entry string) (string, error) {
	abs, err := filepath.Abs(entry)
	if err != nil {
		return "", err
	}
	return bundle(api.BuildOptions{EntryPoints: []string{abs}, AbsWorkingDir: filepath.Dir(abs)}, entry)
}

// bundle runs esbuild with the entry in o; the rest of the options are
// the same for every extension.
func bundle(o api.BuildOptions, entry string) (string, error) {
	dir := o.AbsWorkingDir
	o.JSXFactory = "atto.ui.jsx"
	o.JSXFragment = "atto.ui.Fragment"
	o.Bundle = true
	o.Write = false
	o.Format = api.FormatCommonJS
	o.Platform = api.PlatformNeutral
	o.Target = target
	o.Sourcemap = api.SourceMapInline
	// The maps only need lines; the sources are on disk.
	o.SourcesContent = api.SourcesContentExclude
	o.LogLevel = api.LogLevelSilent
	o.Charset = api.CharsetUTF8
	res := api.Build(o)
	if len(res.Errors) > 0 {
		var lines []string
		for _, m := range res.Errors {
			lines = append(lines, formatMessage(dir, m))
		}
		return "", fmt.Errorf("%s", strings.Join(lines, "\n"))
	}
	if len(res.OutputFiles) == 0 {
		return "", fmt.Errorf("%s: esbuild wrote nothing", entry)
	}
	return string(res.OutputFiles[0].Contents), nil
}

func formatMessage(dir string, m api.Message) string {
	if m.Location == nil {
		return m.Text
	}
	file := m.Location.File
	if !filepath.IsAbs(file) {
		file = filepath.Join(dir, file)
	}
	return fmt.Sprintf("%s:%d:%d: %s", file, m.Location.Line, m.Location.Column+1, m.Text)
}

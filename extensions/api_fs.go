//go:build !noext

package extensions

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/dop251/goja"
)

// fsObject is atto.fs: synchronous UTF-8 text files, paths relative to
// the session's directory. Errors throw.
func (e *ext) fsObject() *goja.Object {
	vm := e.vm
	o := vm.NewObject()
	throw := func(err error) { panic(vm.NewGoError(err)) }
	_ = o.Set("readFile", func(path string) string {
		e.readOnlyRender()
		b, err := os.ReadFile(e.resolve(path))
		if err != nil {
			throw(err)
		}
		return string(b)
	})
	_ = o.Set("writeFile", func(path, text string) {
		e.readOnlyRender()
		p := e.resolve(path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			throw(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			throw(err)
		}
	})
	_ = o.Set("exists", func(path string) bool {
		e.readOnlyRender()
		_, err := os.Stat(e.resolve(path))
		return err == nil
	})
	// list returns the names in a directory, sorted; directories end in /.
	_ = o.Set("list", func(path string) []string {
		e.readOnlyRender()
		entries, err := os.ReadDir(e.resolve(path))
		if err != nil {
			throw(err)
		}
		names := []string{}
		for _, en := range entries {
			n := en.Name()
			if en.IsDir() {
				n += "/"
			}
			names = append(names, n)
		}
		sort.Strings(names)
		return names
	})
	return o
}

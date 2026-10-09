//go:build !noext

package extensions

func bundleSpec(s Spec) (string, error) {
	if s.Source == Builtin {
		src, _ := BuiltinSource(s.Name)
		return src, nil
	}
	return Bundle(s.Path)
}

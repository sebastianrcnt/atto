package extensions

// Builtins are native Go commands in both variants. Full builds still let
// user and approved project extensions replace them by name.
func builtinSpecs() []Spec {
	return []Spec{{Name: "autorename", Path: "builtin/autorename.go", Source: Builtin}, {Name: "diff", Path: "builtin/diff.go", Source: Builtin}}
}

// BuiltinSource returns the native implementation as an example.
func BuiltinSource(name string) (string, bool) {
	switch name {
	case "diff":
		return diffSource, true
	case "autorename":
		return autorenameSource, true
	}
	return "", false
}

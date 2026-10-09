package extensions

import "fmt"

const Ignored = "ignored (slim)"

// UnsupportedMessage reports why extension files cannot run in this build.
func UnsupportedMessage(n int) string {
	return fmt.Sprintf("this atto build has no extension support (slim); %d extension files ignored", n)
}

func IgnoredCount(infos []Info) int {
	n := 0
	for _, in := range infos {
		if in.Source != Builtin {
			n++
		}
	}
	return n
}

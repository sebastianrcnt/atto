package update

import (
	"runtime"
	"testing"
)

func TestVariantAssetSelection(t *testing.T) {
	suffix := "_" + runtime.GOOS + "_" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		suffix += ".exe"
	}
	for _, variant := range []string{"full", "slim"} {
		prefix := "atto"
		if variant == "slim" {
			prefix += "-slim"
		}
		if got := AssetFor(variant); got != prefix+suffix {
			t.Fatalf("%s: %s", variant, got)
		}
	}
	if Asset() != AssetFor(Variant) {
		t.Fatal("variant drift")
	}
}

package images

import (
	"fmt"
	"testing"
	"time"
)

func TestDropOrdersImagesWhenClockDoesNotAdvance(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	dir := t.TempDir()
	im, err := Prepare(pngBytes(t, 3, 2))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for i := range MaxViewed {
		if err := drop(dir, fmt.Sprint(i), im, now); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Collect(dir)
	if err != nil || len(got) != MaxViewed {
		t.Fatalf("collect: %d images, %v", len(got), err)
	}
	for i, im := range got {
		if im.Name != fmt.Sprint(i) {
			t.Fatalf("image %d: %q", i, im.Name)
		}
	}
}

package ui

import "testing"

func TestImageResource(t *testing.T) {
	resource := ImageResource("example-i3", 2)
	if resource != "example-i3-image-2" {
		t.Fatal(resource)
	}
	if err := Validate(Pane, Image(ImageProps{Resource: resource, Alt: "example"})); err != nil {
		t.Fatal(err)
	}
}

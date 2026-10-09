package transcript

import (
	"reflect"
	"testing"
)

// Each extension has its own status; the latest text wins and only its
// owner restores the original; a saved entry is an extension's whole state.
func TestLegacyDisplay(t *testing.T) {
	var d LegacyDisplay
	if !d.IsZero() || d.applyStatus("a", "") || d.applyText("a", "") {
		t.Fatal("zero value")
	}
	d.applyStatus("a", "one")
	d.applyStatus("b", "two")
	kept := d // a copy keeps what it had
	if !d.applyStatus("a", "uno") || d.applyStatus("a", "uno") {
		t.Fatal("status change reported wrong")
	}
	if !reflect.DeepEqual(d.Statuses, []BlockStatus{{"a", "uno"}, {"b", "two"}}) || kept.Statuses[0].Text != "one" {
		t.Fatalf("statuses %v, copy %v", d.Statuses, kept.Statuses)
	}
	d.applyText("a", "A")
	d.applyText("b", "B")
	if d.applyText("a", "") || d.Owner != "b" || d.Text != "B" {
		t.Fatalf("not the owner restored it: %+v", d)
	}
	if !d.Apply(Display{Ext: "b"}) || d.Text != "" || !reflect.DeepEqual(d.Statuses, []BlockStatus{{"a", "uno"}}) {
		t.Fatalf("apply cleared b: %+v", d)
	}
	if d.IsZero() || !d.applyStatus("a", "") || !d.IsZero() {
		t.Fatalf("not zero: %+v", d)
	}
}

package events

import "testing"

func TestIsEventAndWakes(t *testing.T) {
	env := "<atto_internal_context source=\"agent\">\nMessage Type: FINAL_ANSWER\nFrom: /root/tests\nTo: /root\n\nall green\n\nno issues\n</atto_internal_context>"
	evs := []Event{{Text: "job 3 exited"}, {Text: env, Quiet: true}, {Text: "timer fired\nsecond line"}}
	if !IsEvent(env) || !IsEvent(Prefix+"x") || IsEvent("hello") {
		t.Fatal("IsEvent")
	}
	if Wakes(evs[1:2]) || !Wakes(evs) {
		t.Fatal("Wakes")
	}
}

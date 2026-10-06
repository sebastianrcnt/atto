package events

import "testing"

func TestSplitAndTitles(t *testing.T) {
	env := "<atto_internal_context source=\"agent\">\nMessage Type: FINAL_ANSWER\nFrom: /root/tests\nTo: /root\n\nall green\n\nno issues\n</atto_internal_context>"
	evs := []Event{{Text: "job 3 exited"}, {Text: env, Quiet: true}, {Text: "timer fired\nsecond line"}}
	text := Format(evs)
	parts := Split(text)
	if len(parts) != 3 || parts[0] != "job 3 exited" || parts[1] != env || parts[2] != "timer fired\nsecond line" {
		t.Fatalf("split %q", parts)
	}
	if got := TitleOf(parts[1]); got != "◆ final answer from /root/tests" {
		t.Fatalf("title %q", got)
	}
	if got := TitleOf(parts[2]); got != "timer fired" {
		t.Fatalf("title %q", got)
	}
	if !IsEvent(env) || !IsEvent(Prefix+"x") || IsEvent("hello") {
		t.Fatal("IsEvent")
	}
	if Wakes(evs[1:2]) || !Wakes(evs) {
		t.Fatal("Wakes")
	}
}

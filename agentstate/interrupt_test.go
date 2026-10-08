package agentstate

import "testing"

func TestInterruptTargetsOneTurn(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	if Interrupted("p", "a", 1) {
		t.Fatal("turn interrupted without a request")
	}
	if err := RequestInterrupt("p", "a", 1); err != nil {
		t.Fatal(err)
	}
	if !Interrupted("p", "a", 1) || Interrupted("p", "a", 2) {
		t.Fatal("request did not target exactly its turn")
	}
	if err := RequestInterrupt("p", "a", 2); err != nil {
		t.Fatal(err)
	}
	if Interrupted("p", "a", 1) || !Interrupted("p", "a", 2) {
		t.Fatal("new request did not replace the old one")
	}
	Remove("p", "a")
	if Interrupted("p", "a", 2) {
		t.Fatal("request survived removing the agent")
	}
	if RequestInterrupt("p", "../bad", 1) == nil || RequestInterrupt("p", "a", 0) == nil {
		t.Fatal("invalid interrupt request accepted")
	}
}

package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestMaintenanceModelShellRefusal(t *testing.T) {
	for _, env := range []string{"ATTO_AGENT", "ATTO_SESSION_ID", "ATTO_SUBAGENT"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(env, "test")
			for _, run := range []func([]string, *bytes.Buffer) error{func(a []string, b *bytes.Buffer) error { return RunRestore(a, b) }, func(a []string, b *bytes.Buffer) error { return RunClean(a, b) }, func(a []string, b *bytes.Buffer) error { return RunUninstall(a, b) }} {
				var b bytes.Buffer
				if e := run(nil, &b); e == nil || !strings.Contains(e.Error(), "model shell") {
					t.Fatal(e)
				}
			}
		})
	}
}
func TestParseMaintenanceAge(t *testing.T) {
	for _, s := range []string{"30d", "24h", "1.5d"} {
		d, e := parseOlder(s)
		if e != nil || d <= time.Hour {
			t.Fatal(s, d, e)
		}
	}
	for _, s := range []string{"0d", "-1d", "garbage", "NaNd", "Inf d", "99999999999d"} {
		if _, e := parseOlder(s); e == nil {
			t.Fatal("accepted", s)
		}
	}
}

package main

import (
	"reflect"
	"testing"
)

func TestGrantNames(t *testing.T) {
	for _, c := range []struct {
		value string
		want  []string
	}{
		{"", nil}, {"now, exit", []string{"now", "exit"}}, {"bash,now,exit", []string{"bash", "now", "exit"}},
	} {
		if got := grantNames(c.value); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%q: %v", c.value, got)
		}
	}
}

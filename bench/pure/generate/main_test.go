package main

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestGeneratedProblems(t *testing.T) {
	raw, err := os.ReadFile("../problems.json")
	if err != nil {
		t.Fatal(err)
	}
	var got []problem
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := problems()
	if len(got) != 10 || !reflect.DeepEqual(got, want) {
		t.Fatal("regenerate problems.json")
	}
	for _, p := range got {
		if p.ID == "" || p.Prompt == "" || p.Expected == "" || p.Checker != "exact" {
			t.Fatal(p)
		}
	}
}

func TestReferenceAlgorithms(t *testing.T) {
	if primeCount(10) != 4 || primeCount(2) != 0 {
		t.Fatal("primes")
	}
	if levenshtein("kitten", "sitting") != 3 || levenshtein("", "abc") != 3 || levenshtein("abc", "abc") != 0 {
		t.Fatal("distance")
	}
	if bfOutput("++++++++[>++++++++<-]>+.+.") != "AB" {
		t.Fatal("brainfuck")
	}
	if integer("FFFFFFFFFFFFFFFF", 16).String() != "18446744073709551615" {
		t.Fatal("big integer")
	}
}

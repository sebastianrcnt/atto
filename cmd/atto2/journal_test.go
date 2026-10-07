package main

import (
	"path/filepath"
	"testing"

	"atto2/kernel"
	"atto2/machine"
)

func journalOptions(t *testing.T) options {
	t.Helper()
	dir, grant, record, replay, baseURL, modelID, metrics := "/not/a/directory", "now,exit", "", "", "http://example.invalid", "test", ""
	verbose, quiet, baseline := false, true, false
	steps := 30
	return options{dir: &dir, grant: &grant, record: &record, replay: &replay, baseURL: &baseURL, modelID: &modelID, metrics: &metrics, verbose: &verbose, quiet: &quiet, baseline: &baseline, steps: &steps}
}

func TestCLIJournal(t *testing.T) {
	o := journalOptions(t)
	*o.record = filepath.Join(t.TempDir(), "life.jsonl")
	j, closeFile, err := openJournal(o, "question")
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Finish("report", nil); err != nil {
		t.Fatal(err)
	}
	closeFile()
	*o.replay = *o.record
	*o.record = ""
	*o.dir = "other"
	j, closeFile, err = openJournal(o, "")
	defer closeFile()
	if err != nil || j.Header.Input != "question" || *o.dir != "/not/a/directory" {
		t.Fatalf("%+v %v", j, err)
	}
	k, err := kernel.WithGrant(*o.dir, grantNames(*o.grant)...)
	if err != nil {
		t.Fatal(err)
	}
	m := machine.New(k)
	defer m.Close()
	a := newAgent(o, m)
	if err := attachJournal(a, j, true); err != nil {
		t.Fatal(err)
	}
	if a.Model != j || a.Journal != j {
		t.Fatal("journal not attached")
	}
	a.Cortex.System = "changed"
	if err := attachJournal(a, j, true); err == nil {
		t.Fatal("changed instructions accepted")
	}
}

func TestJournalFlagConflicts(t *testing.T) {
	for _, baseline := range []bool{false, true} {
		o := journalOptions(t)
		*o.record = "record"
		*o.baseline = baseline
		if !baseline {
			*o.replay = "replay"
		}
		if _, closeFile, err := openJournal(o, ""); err == nil {
			closeFile()
			t.Fatal("conflict accepted")
		}
	}
}

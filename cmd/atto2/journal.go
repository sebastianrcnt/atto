package main

import (
	"fmt"
	"os"
	"strings"

	"atto2/agent"
	"atto2/cortex"
	"atto2/kernel"
)

func openJournal(o options, input string) (*agent.Journal, func(), error) {
	noop := func() {}
	if (*o.record != "" && *o.replay != "") || (*o.baseline && (*o.record != "" || *o.replay != "")) {
		return nil, noop, fmt.Errorf("record, replay and baseline are mutually exclusive")
	}
	if *o.replay != "" {
		f, err := os.Open(*o.replay)
		if err != nil {
			return nil, noop, err
		}
		defer f.Close()
		j, err := agent.Replay(f)
		if err == nil {
			*o.dir = j.Header.Directory
			*o.grant = strings.Join(j.Header.Grant, ",")
			*o.steps = j.Header.MaxSteps
		}
		return j, noop, err
	}
	if *o.record == "" {
		return nil, noop, nil
	}
	k, err := kernel.WithGrant(*o.dir, grantNames(*o.grant)...)
	if err != nil {
		return nil, noop, err
	}
	f, err := os.OpenFile(*o.record, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, noop, err
	}
	h := agent.Header{Input: input, Grant: grantNames(*o.grant), Directory: *o.dir, Instructions: cortex.Instructions(k, *o.dir), MaxSteps: *o.steps}
	j, err := agent.Record(f, h)
	return j, func() { f.Close() }, err
}

func attachJournal(a *agent.Agent, j *agent.Journal, replaying bool) error {
	if j == nil {
		return nil
	}
	if a.Cortex.System != j.Header.Instructions {
		return fmt.Errorf("replay step 0 instructions: recorded %q; actual %q", j.Header.Instructions, a.Cortex.System)
	}
	a.Journal = j
	a.Machine.Kernel.Exchange = j.Exchange
	if replaying {
		a.Model = j
	}
	return nil
}

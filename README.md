# atto2

An experiment: atto rebuilt as an operating system for agents. atto (1)
is frozen; atto2 starts from nothing here.

This prototype is one agent, a pure computer, and a kernel:

- **machine** (`machine/`): the agent's own Lua 5.1 VM (gopher-lua).
  It computes only. Base, string, table and math are open; no files,
  OS, network, time, randomness, require/load, or `string.dump`.
  `text` provides split, lines, trim and Go-regexp match-all; `json`
  provides encode/decode and `json.null`. Globals persist between runs.
  `pairs`/`next` sort primitive keys; identity keys retain insertion order.
  Output and identity formatting are deterministic on replay as well.
- **kernel** (`kernel/`): the registry, schemas, per-agent grant and syscall
  log. Every syscall goes through one checked Go-function boundary, even
  through aliases. A run is pure exactly when no syscall was invoked,
  including rejected calls. Instructions are generated from the grant.
- **agent** (`agent/`): model → Lua → output → model, until `sys.exit`.
  The model's text is its stdout, not its result. Text without a tool
  call is shown, followed by one short reminder; the life continues.
- **cortex** (`cortex/`): instructions and conversation sent to the model.
- **model** (`model/`): an OpenAI-compatible chat completions client.
  The agent has one tool, `lua`.

The step-one syscalls are:

```lua
local r = sys.bash{cmd = "git ls-files | head", timeout = 60}
print(r.stdout, r.stderr, r.code)
local b = sys.bash
local r2 = b("grep -n module go.mod") -- same checked boundary
print(sys.now())                      -- Unix seconds, so impure
sys.exit{report = "My result"}         -- stops, even inside pcall/xpcall
```

Calls take positional arguments in schema order or one table. Unknown
fields, wrong types and missing required fields are errors. Lua has no
Python-style named arguments: use `sys.bash{cmd = "ls"}`, not
`sys.bash(cmd="ls")`.

`sys.bash` runs `/bin/bash -c` in `-dir`. On macOS it uses `sandbox-exec`
with a deny-by-default profile: reads and process execution are allowed;
writes are denied everywhere except `/dev/null` and a private, per-call
canonical temp directory. Network access is denied. The child receives a
minimal environment, not inherited credentials. **This is write isolation,
not read isolation**: `-dir` is a working directory, not a read chroot.
Linux and other platforms refuse `sys.bash` pending an OS-level sandbox.
The default timeout is 60 seconds; stdout/stderr each retain at most 64 KiB,
with omitted-byte counts. Timeout kills the process group and returns code
124. A command's nonzero exit is a result, not a syscall error.

```sh
go build -o atto2 ./cmd/atto2
./atto2 -dir ~/some/project -v "Where is the config file read, and what keys does it take?"
```

`-grant` selects comma-separated syscall names (default `bash,now,exit`).
Without bash, `-dir` is neither required nor checked:

```sh
./atto2 -grant now,exit -record life.jsonl "How many primes are below 199933?"
./atto2 -replay life.jsonl
```

Only granted names are stored in `sys` and described in instructions. Its
metatable routes attempts to call absent names through the checked boundary;
`sys.bash` without a grant fails with `sys.bash is not granted to this agent`.

`-record` writes JSONL: input/grants/instructions, then each model reply and
usage, Lua tool code/output, syscall arguments/results/errors, and the final
report or life error. `-replay` needs no question or model server. It restores
a fresh machine, feeds recorded replies, substitutes syscall results (including
timestamps), and verifies tool outputs and the final report. Exit lifecycle
semantics are checked afresh; replay does not execute world devices. Mismatches
name the step and include recorded and actual values. Record/replay and
`-baseline` are mutually exclusive. Records are not authenticated: an unused
syscall result cannot be validated against an independent source of truth.

`-baseline` makes exactly one chat completion with the question alone and no
tools. It uses the same client and metrics path; its answer is the model text.
The computational agent-versus-baseline experiment is in
[`bench/pure/`](bench/pure/README.md).

The CLI always prints the exit report on stdout, along with any model
stdout. Lua trace is on stderr (`-q` hides it). `-v` prints one line per
syscall at the end, including time, name, arguments and result/error
summaries, followed by pure/impure run counts. `-metrics path.json` writes
the report, steps, last prompt tokens, total completion tokens, syscall count and run counts in JSON.
Only `sys.exit` completes a life successfully; exhausting `-steps` (default
30) is an error: `life ended without exit after N steps`.

`-base-url` and `-model` (or `ATTO2_BASE_URL`, `ATTO2_MODEL`,
`ATTO2_API_KEY`) choose the model; the default is `orca-local` at
`http://192.168.0.235:8081/v1`, with no key unless supplied.

## Helpers

- `text.split(s, sep)` uses a literal separator (empty separator splits UTF-8
  characters). `text.lines(s)` returns an array, accepts CRLF, and omits the
  terminal empty line; empty input yields an empty array.
- `text.trim(s)` removes surrounding Unicode whitespace.
- `text.match_all(s, goPattern)` returns full-match strings without capture
  groups, otherwise arrays of capture strings (not the full match).
- `json.encode(v)` accepts scalars, dense 1-based arrays, and string-keyed
  objects; cycles, sparse/mixed tables and other Lua types are errors.
  `json.decode(s)` preserves null with `json.null`, and remembers whether
  an empty table was an object or array. An untagged empty table encodes as
  `[]`. Numbers use Lua's floating-point representation.

## Checks

Run `scripts/check.sh` for formatting, vet, modernize, module tidiness,
staticcheck, and uncached race tests. The pre-commit hook runs the same
checks without tests on a temporary snapshot of the staged repository.

The hook is per-worktree; enabling it here does not change atto's hook:

```sh
git config extensions.worktreeConfig true
git config --worktree core.hooksPath .githooks
```

Tests cover deterministic replay, helper semantics, removed world access,
schema checks/grants/aliases, purity accounting, exit, agent lifecycle,
and real macOS read/write sandbox behavior, caps and timeout.
The sequential local-model repository benchmark is in [`bench/`](bench/README.md).

Open work: Linux write sandboxing, read-access grants, resource/memory
quotas (Go helpers are not preempted by Lua's context checks), compaction,
and more agent/OS mechanisms. `sandbox-exec` is deprecated by Apple; it
is a prototype boundary, not a portable long-term sandbox API.

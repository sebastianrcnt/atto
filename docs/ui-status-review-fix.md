# Status review follow-up (2026-10-09)

## Correction

The first migration incorrectly priority-packed independent built-in slots. It
could split effort from the model, discard the directory, duplicate activity,
move jobs into the usage row and substitute worker heap for frontend RSS.

`app/ui_status.go` now feeds rendered portable slots into the **main
row/drop/path algorithm**, extracted as `layoutStatus`. It preserves:

- native item order, separators, leading margin and exact SGR scopes;
- model/effort positioning, two-row usage flow, front-compressed cwd/branch;
- the reserved first-row goal position and narrow-terminal goal indicator row;
- jobs/timers on the existing separate transient indicator row;
- frontend process RSS, built locally as a Text tree;
- locally advancing goal time, using the same clock as main;
- multiline custom status output and empty custom output; no accidental native
  model/path output when custom status is configured.

Activity is not a worker status slot: its existing above-editor drawing remains
native and appears exactly once. `ui/status.go` supplies a shared, pure builder
for both worker/build variants, separating context bar/size and path/branch for
native compression/drop semantics. Cache hit rate reads the latest response
(including the rebased main's `Usage.Last`), not stale zero legacy fields. Fresh
input, output-only usage, cache writes, cost/subscription, compaction warning and
cap, effort styling and long-context formatting retain main behavior.

Attached frontends no longer run a second custom-command refresh loop. Execution
and cache remain worker-owned; the UI receives themed Text. The existing catalog
safety boundary (semantic themes, control/OSC removal) remains unchanged.

No new visual differences are intended for the built-in status area. This fix
does not change the protocol or resurrect string extension setters.

## Independent reference and parity gate

`app/status_reference_test.go` freezes the actual `renderStatus`/`buildStatus`
functions from main commit **7694bccbafbb5af5c97df77ac0919b6dca7c4f94** (method
names adapted only to avoid collisions; caching bypassed for the reference).
It does **not** call the portable renderer or extracted layout function.

`TestStatusMainParity` generates its expected output exclusively with that frozen
renderer. **95 reference cases** cover 19 states at **40/80/100/120/160 columns**:
idle, busy, job running, goal active, all together, custom, custom plus all,
empty/long custom, warning, no usage, output-only usage, max effort, long
context, priced/subscription/cache writes, compact cap, timers, paused goal and
held goal. `app/testdata/status-main-*.json` preserves every styled byte and
trailing space; corresponding `.txt` files are reviewable screenshots. The UI
result must equal the reference string slices **byte-for-byte**, including SGR,
not just stripped text.

`TestLivePortableStatusMatchesMain` also compares all five widths through the
actual worker/app harness: idle, latest usage/cache, busy, busy plus running job,
active goal plus both, paused goal and worker-executed custom status. Dedicated
regressions cover the elapsed goal clock/RSS and worker source fields/slot roles.
The same references are consumed by full and noext builds. The adapter only
reads frontend-validated trees; invalid input/removed model slots are checked at
widths 1–40 without panic/overflow. Additional Go-owned status slots remain
visible rather than being mistaken for native slots.

## 100-column busy + job + active-goal reference (and UI)

```text
 ◆ Orca Local · low  ────────── 0% 2.3k/262k · cache 97% · ↑2.1k ↓107           Pursuing goal (10s)
                                                                /work/scratchpad/uiv/w (main) · 31MB
 ● 1 job running (/jobs)
```

## Checks

- `gofmt -l .` empty; `git diff --check` clean.
- `go test ./...` and `go test -tags noext ./...`: passed.
- `go test -race ./ui ./tui ./server ./app ./extensions ./session ./core/transcript`,
  both full and noext: passed.
- `go vet ./...`, `go vet -tags noext ./...`, `GOOS=windows go vet ./...`,
  `GOOS=windows go vet -tags noext ./...`: passed.
- Validator fuzzing, 30 seconds: passed, **4,049,827 executions**.
- No files in `daemon/` touched; no push.

## Local-model live check

Fresh build `go build -o /tmp/atto-status-live-bin ./cmd/atto`; PTY **100×24**,
local llama-cpp/orca-local only. Isolated ATTO_DIR copied only the llama-cpp
models.json and wrote selecting settings; **no auth.json copied**. In a fresh git
repo, a long active goal/model turn ran alongside a real `sleep 90` background
job. A decoded terminal frame showed:

```text
  ◆ Orca Local · medium  ────────── 0% 0/262k              …/repo (main) · 46MB Pursuing goal (2s)
  ● 1 job running (/jobs)
```

This low-usage state fits its path on one row, exactly as native main does; the
nonzero-usage reference above requires two rows plus the job indicator. Activity
was above the editor, not inside either status row. The captured elapsed goal
clock advanced, and memory was frontend RSS. The job was killed during cleanup.

Driver: `/tmp/atto-status-live.py`. Raw frame/capture:
`/var/folders/rq/w0f9cnvj6yddp8jbym2mh9h00000gn/T/atto-status-live-pqdb8754`.

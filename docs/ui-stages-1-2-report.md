# Shared UI stages 1–2 delivery (2026-10-09)

## Stage 1: portable UI and worker protocol

- `ui/` is independent of app, TUI, server and goja. It owns typed constructors,
  wire trees, schema/size/depth/key/theme/URL validation, passive unknown-element
  fallback, headless text, middleware/defaults, opaque native item references,
  binding ownership, throttling, deadlines, revisions and action checks.
- `server/ui*.go` supplies one registry per session, worker services, hub
  publication, capabilities, lane-consistent snapshots, helper questions and
  status-command refresh/cache. Accepted actions retire their revision before
  callbacks: duplicate/stale presses are rejected, not retried.
- Session `ui_block`, `ui_block_update` and completed `ui_item_display` entries
  are display-only. Replay/pages apply them without running historical code.
  Native item truth remains unchanged; saved controls remain disabled until
  explicitly rebound. Native show-original is outside extension drawing.
- Revision 3 additions documented in `docs/protocol.md`: initialize UI capability,
  `ui/capabilities`, `ui/open`, `ui/render`, `ui/close`, `ui/event`; attach/read
  `ui:{version:1,instances:[...]}`, typed `uiBlock` and item `uiDisplay`.
  Notifications use the existing eventId/serverInstanceId cursor. Routing uses
  **rev only**, with no uiEpoch, requestKey or dedupe cache.
- Tests cover schema rejection, malicious links/Markdown, cyclic/oversized props,
  sealed native references, renderer deadlines/cancellation, provider key
  ownership, local/passive controls, coalescing, stale actions, snapshot/reducer
  tombstones, display-only replay, item overlays and first-answer-wins dialogs.
  The protocol dispatcher/documentation consistency gate includes the new methods.

## Stage 2: TUI and built-ins

- `tui/elements.go` adapts the catalog to existing components and theme styles:
  grapheme/cell widths, four-cell tabs, clipping, borders, row allocation,
  local drafts/highlights/disclosures, hit rectangles, Tab/arrows/Enter/hotkeys,
  native focus and Escape. Input/Select state survives unrelated worker redraws.
- `app/ui*.go` handles pane docks/tabs/scroll/focus, above-prompt bands, priority
  status composition, toasts, modal dialogs and native engine resolution.
  Auto/side panes dock at >=120 columns with >=72 transcript and >=32 pane;
  narrower terminals retain the pane above the editor.
- Five independent migration commits move `/diff`, status, goal, jobs and helper
  dialogs. `/diff` retains the native git/error/truncation implementation and now
  persists Collapse + summary List + Diff. Custom statusLine execution stays
  worker-side; safe SGR becomes themed Text with a shared refresh cache.
- Goal controls call edit/pause/resume/clear worker services. Jobs stop controls
  call the existing job service. Select/confirm/input use the existing broker;
  only the first valid answer settles it, and cancel/reload retain broker rules.
- The old string extension API still works. No built-in was newly put on it.
  Typed transcript, paging, command center and pickers remain native.

## Observable differences and scope

- Goal/jobs are live shared panes instead of one-off transcript summaries. Their
  visibility is shared; local focus/tab/scroll/draft state is not. Direct buttons
  supplement the existing commands. Goal action labels fit the narrow default
  pane. A longer pane scrolls locally to keep its focused control visible.
- Diff uses a local disclosure instead of the old string display header. Content,
  summary, git semantics and native expansion shortcuts are preserved.
- Status prioritizes goal/activity, then model/context; lower-priority slots may
  move to the second row or disappear first. Fresh-input accounting and cache
  write totals are preserved. Memory reports worker heap, not frontend RSS.
- Helper dialogs now render portable controls; input shows Enter/Tab/Escape
  guidance. Command-center and private credential/trust UI are not wrapped.
- Image is validated and renders the specified `[image: alt]` fallback. There is
  no existing authenticated catalog-image resource transport or terminal bitmap
  renderer in this checkout to hook up; actual bitmap presentation/resource
  fetching is not implemented here. No arbitrary paths/remote URLs are read.
- A Go renderer that ignores cancellation cannot be forcibly killed. The
  watchdog returns defaults at the deadline and bounds it to one outstanding
  invocation per registration. Built-in defaults/state reads run on the worker
  lane; external middleware receives a precomputed safe tail.
- Stage 3 constructor/goja/JSX/store/removal work and later clients/providers are
  intentionally not part of these two stages.

## Validation and live evidence

Full and noext builds
use the same committed golden fixtures (`app/testdata/ui-*` and
`tui/testdata/elements-*`) at **40/80/120/160 columns**. App harness tests exercise
real pane actions, broker dialogs and native engine/show-original rendering.

The local-only PTY smoke runs a freshly built `cmd/atto` at 80 columns in an
isolated ATTO_DIR. Only the llama-cpp provider models.json and settings selecting
llama-cpp/orca-local are copied; no auth.json is copied. A fresh git repo exercises
`/diff`; goal open/pause, a running `sleep 60` job and its actual stop button, and
an extension helper select followed by its green callback are checked. Raw PTY
captures are retained outside the repository. No remote model is used. Final live run passed all checks (local service HTTP
200; stop button left the job `killed`; select callback returned `green`). The
artifacts are `/var/folders/rq/w0f9cnvj6yddp8jbym2mh9h00000gn/T/atto-ui-final-live-zfxd5wsm`,
and the driver is `/tmp/atto-ui-live-smoke.py`.

Validation includes the following commands (test runs unset inherited
ATTO_SESSION_ID/ATTO_AGENT/ATTO_SUBAGENT/ATTO_TOOL_CALL_ID/ATTO_VIEW_DIR so nested
fixtures use their own isolated identities):

- `gofmt -l .`: empty; `git diff --check`: clean.
- `go vet ./...`, `go vet -tags noext ./...`, `GOOS=windows go vet ./...`,
  `GOOS=windows go vet -tags noext ./...`: passed.
- `go test ./ui -run=^$ -fuzz=FuzzValidate -fuzztime=30s -parallel=4`: passed,
  3,283,973 executions on the final validator (plus an earlier 3.86M run).
- `go test ./...` and `go test -tags noext ./...`: passed.
- `go test -race ./ui ./tui ./server ./app ./extensions ./session ./core/transcript`
  and the same with `-tags noext`: passed.
- New byte-level scripted-provider regression compares complete request bodies
  before and after UI transcript/middleware changes, not just model text.
- A slim race run exposed an existing replay fixture calling snapshot mutation
  outside `TUI.Do`; the fixture now does its clear/replay/read on the TUI lane,
  and repeated full/slim race regressions pass.

No files under `daemon/` were changed. Work is committed on `atto/72ad52ac`;
no push was performed.

## 80-column screenshots as text

These are ANSI-stripped committed harness goldens, not invented terminal output.
Before fixtures reconstruct the prior native components using the same demo
content. Status after also demonstrates the new goal/activity slots.

### /diff

Before:

```text
± git diff · diff
  2 files changed, +4 -1 (1 staged, 1 unstaged, 1 untracked)
    M  util.go    +2 -0
     M main.go    +2 -1
    ?? notes.txt  untracked

  == Staged changes ==
  diff --git a/util.go b/util.go
  index 06ab7d0..5194a23 100644
  --- a/util.go
  +++ b/util.go
  @@ -1 +1,3 @@
   package main
  +
  +func util() {}
  == Unstaged changes ==
  diff --git a/main.go b/main.go
  index d6e0156..c337e17 100644
  --- a/main.go
  +++ b/main.go
    + 8 lines (click or ctrl+t to expand)
```

After:

```text
▸ git diff
  2 files changed, +4 -1 (1 staged, 1 unstaged, 1 untracked)
  •   M  util.go    +2 -0
  •    M main.go    +2 -1
  •   ?? notes.txt  untracked

  == Staged changes ==
  diff --git a/util.go b/util.go
  index 06ab7d0..5194a23 100644
  --- a/util.go
  +++ b/util.go
  @@ -1 +1,3 @@
   package main
  +
  +func util() {}
  == Unstaged changes ==
  diff --git a/main.go b/main.go
  index d6e0156..c337e17 100644
  --- a/main.go
  +++ b/main.go
  + 8 lines (click or ctrl+t to expand)
```

### Status

Before:

```text
 ◆ Orca  ━───────── 11% 31k/262k · cache 85% · ↑12k ↓3.4k · W2k · $0.123
                                                           /work/proj (main) · ?
```

After:

```text
◆ Orca · ━───────── 11% 31k/262k · cache 85% · Working…     ◉ Goal 0s · 0 tokens
↑12k ↓3.4k · W2k · $0.123                                      /work/proj (main)
```

### Goal

Before:

```text
  Goal
  Status: active
  Objective: ship it
  Time used: 0s
  Tokens used: 0

  Commands: /goal edit, /goal pause, /goal clear
```

After:

```text
Goal · tab focus · esc return
Status: active
Objective: ship it
Time used: 0s · Tokens used: 0
⠋ Working on goal
e: Edit  p: Pause  c: Clear
```

### Jobs

Before:

```text
  Background jobs  · /stop stops all · output: atto job output <id>
  1    agent turn running      0s       sleep 60
```

After:

```text
Job   Kind   State    Time  Command
1     agent  running  0s    sleep 60
[ Stop job 1 ]
Output: atto job output <id> · /stop stops all
```

### Select

Before:

```text
Pick one  (demo)
› red
  green
```

After:

```text
Pick one  (demo)

› red
  green
```

### Confirm

Before:

```text
Sure?
› Yes
  No
```

After:

```text
Sure?

› Yes
  No
```

### Input

Before:

```text
  Name?
  › Ann
  enter submit  esc cancel
```

After:

```text
Name?

Ann
enter submit  tab next  esc return
```

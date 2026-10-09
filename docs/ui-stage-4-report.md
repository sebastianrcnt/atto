# Stage 4 — new browser client

Implemented on 2026-10-09 in the `atto/4c72966a` worktree.

## Entry points and architecture

- `atto serve`: `0.0.0.0:7879`, ordinary HTTP page at `/`, WS protocol at `/ws`.
- `atto app-server --listen ws://HOST:PORT --web`: explicit opt-in; existing
  WS-only listeners and stdio/Unix transports keep their behavior.
- TUI `/remote [ws://HOST:PORT]` shares its in-process Server, or uses the same
  daemon worker routes as every other facade; `/remote off` closes transport only.
- Static assets contain no secrets. (Changed 2026-10-10: the web listener has
  no token; it is for a trusted LAN or Tailscale. Origin and Host checks remain,
  the latter against DNS rebinding.) `/ws` rejects query parameters. No HTTPS, PWA, service worker, QR or new runtime dependency.
- Plain TypeScript DOM is the smaller allowed frontend alternative. Go esbuild
  + pinned/checksummed standalone Tailwind builds embedded committed assets;
  generation is not part of normal builds. Source-hash staleness test included.

## Client behavior

All v1 elements and sites are implemented with safe DOM text and CSSOM theme
properties. Worker callbacks are revision-bound; local values/disclosures/focus
survive unrelated updates. Unknown catalog types are passive text; replayed
controls stay disabled until freshly bound. Engine refs resolve native typed
items, with show-original outside overlays. `ui.ImageResource(itemId,index)`
resolves authenticated item/image, never an HTTP file or arbitrary remote URL.

Multi-session inventory includes needs-you and parent-linked agents, archived
reads/restore and confirmed archive/delete, tabs, rename/start/resume, tail-first
paging with anchor preservation, streaming, recovery, fork/checkpoint navigation,
model/effort, runtime command suggestions and image file/paste/drop. Shared Go
queue previews and context trees join the existing goal/jobs/diff/dialog/status
providers. Dialog keyboard focus is trapped, native editor keys remain native.

No new RPC methods or protocol revision: additive `ContextInfo.tree`, uiBlock
`actionsEnabled` and transcript ui/render binding metadata; documented opaque
image-resource naming and WS subprotocol auth/negotiation. Rev-only routing is
unchanged: no uiEpoch, requestKey, dedupe or uncertain-action retries.

## Live local-model check

Isolated `/tmp/atto-ui4-live-4c72966a/config`: models.json contains **only**
`llama-cpp/orca-local` (`http://192.168.0.235:8081/v1`), settings explicitly select
it with effort off; max output was bounded to 256. No auth.json copied. Fresh
HOME/project and a minimal subprocess environment exclude inherited provider
keys and agent IDs. The executable is built from this worktree.

`app-server --listen ws://127.0.0.1:0 --web` fetched page/JS/CSS with CSP; a raw
RFC 6455 client offered atto.rpc.v3, initialized revision 3 with surface web,
started a session and sent input/submit as the page does. Worker PID 29350 was
separate from gateway PID 29348. The local model completed in four deltas with
exact answer `WEB_STAGE4_OK`. Context returned a Go Box tree; a limit-2 tail had
hasMore and returned an earlier page; `/jobs` opened the Go jobs pane. After
closing/restarting the gateway, resume retained the **same worker PID** and the
answer. The test explicitly closed the session and stopped its isolated daemon.
The live script/log are in that temporary directory and the atto job 9 log.

The expanded rerun (job 17, session `95989c35`, worker 39292 / gateway 39287)
also loaded a fresh trusted user extension in the isolated config. Its pane
button incremented exactly once, received originating surface **web**, and the
same-rev duplicate was rejected with revisionConflict. A second local-model turn
attached a generated 8×8 PNG and completed; authenticated item/image returned
PNG bytes. `/goal` opened the Go pane without setting/resuming a goal; `/diff`
returned a Go transcript tree after its asynchronous git work; `/jobs` opened
its pane. Gateway restart again retained the same worker and transcript. No
original auth.json was copied or created. The rerun needed two harness-only
fixes: wait for asynchronous `/diff`, and allow an empty git-fixture commit on
repeat runs; neither was a server/client defect.

## Original delivery QA scope and behavior differences

The tests execute TypeScript in goja with an explicit DOM shim, not a headless
browser. Phone and desktop structural paths, token consumption, controls,
streaming, paging, settings mutations, dialogs and disconnect-without-resend are
covered. Shared catalog fixture also drives TUI goldens. Real CLI process e2e
fetches assets and drives the WS turn for both --web and serve.

**Not visually checked:** browser layout/paint and actual CSP enforcement,
light/dark appearance, touch keyboard, image paste/drop and decode rendering,
focus/scroll geometry and LAN/Tailscale access from another physical device.
Manual browser QA remains needed; no screenshot claim is made.

Deliberate differences: conservative Markdown (no raw HTML or arbitrary URL
schemes), no syntax highlighting dependency, one command disclosure per tool
rather than TUI grouping, system/login/trust/diagnostic flows use the local CLI,
HTTP clipboard falls back to selectable text. Inventory polls while visible and
supports explicit refresh (the protocol has no inventory push event). Pickers
and checkpoint navigation remain client-local rather than new shared dialogs.

## Verification commands

Run from this worktree with inherited model-agent tracking variables cleared:

```sh
unset ATTO_SESSION_ID ATTO_AGENT ATTO_SUBAGENT ATTO_TOOL_CALL_ID
gofmt -l .                        # empty
go vet ./...
go vet -tags noext ./...
GOOS=windows GOARCH=amd64 go vet ./...
GOOS=windows GOARCH=amd64 go vet -tags noext ./...
go test ./...
go test -tags noext ./...
go test -race ./server ./server/web ./app ./ui ./tui ./cmd/atto
go test -race -tags noext ./server ./server/web ./app ./ui ./tui ./cmd/atto
```

The full/noext/race matrix passed (job 10), native/Windows vet and the latest
focused packages passed (job 11), and final TS workflow tests plus cmd race
passed (job 12). The final complete full/noext/race rerun passed (job 16). One prior full rerun hit
existing CLI `TestAgentsNestToAnyDepth`'s intermittent “no agent is running”;
its focused ten-run check passed before the final full rerun. An earlier run
also exposed inherited ATTO_TOOL_CALL_ID contamination in a CLI provenance
assertion; clearing that tracking variable fixed the environment, with no
unrelated CLI source changes. These are recorded rather than concealed.

Normal builds and noext builds use the same embedded assets and Go-built queue,
context, goal/jobs/diff trees. Cross-Windows vet is not a Windows runtime test.


## Stage-4 polish — 2026-10-10

Implemented in worktree `e18024af`, branch `atto/e18024af`. This section
supersedes the original delivery's “not visually checked” note above.

### Changes

- A safe, streaming-tolerant Markdown renderer follows `tui/markdown.go`:
  semantic GFM tables (alignment, escaped/code pipes and empty headers), nested
  ordered/unordered/task lists, headings, recursive blockquotes, rules,
  backtick/tilde fences using the catalog Code look, inline code, nested
  bold/italic/strike, allowlisted links and hard/soft breaks. Incomplete fences
  remain code; incomplete table separators do not switch into tables early.
  Raw HTML stays literal DOM text; no HTML parser, `innerHTML` or remote scripts.
- Human transcript labels, quiet centered notices, collapsed “Thinking” with
  live shimmer, and Fork only on user messages (hover/focus, visible on touch).
  Completed command chips show a status dot, short title, exit code and duration;
  running commands expand with live output. Expansion reveals command/output,
  long output has Show more/less, and server-truncated output keeps Full output.
  Only explicit user disclosure choices persist: Chrome's insertion-triggered
  toggle events must not keep finished tools expanded. Unchanged transcript DOM
  is reused across stream/status/composer paints and pruned to loaded pages.
- Sidebar titles are one-line ellipses; Markdown-free previews clamp to two
  lines while retaining literal code punctuation. State dots, relative ages,
  root-project grouping, indented children and finished-subtree folding follow
  the command center. Live attached snapshots immediately update busy/needs-you.
  Active/All/Archived is a segmented filter. Per-row ⋯ actions contain Archive /
  Restore / Delete, retaining confirmation and busy-stop protection. The sidebar
  close button is phone-only.
- Beautiful UI–inspired typography, spacing, radii, empty states, tab strip and
  inset Prompt Bar. The editor grows, icon controls have accessible names,
  Send becomes Stop during a model turn, and Steer/Queue only appear while busy.
  Send-now/Background remain in the busy overflow disclosure. Efforts come from
  the selected model's catalog, not stale thread metadata. Queue retains the
  shared Go band (without duplicating previews) and native takeback controls.
  Context from the status tokens is a bounded, scrollable card containing the
  shared Go tree. Theme roles and strict CSP are unchanged. Adaptations are
  recorded in `THIRD_PARTY_NOTICES`; AI Elements remains design-reference only.
- Expanded TS/goja tests cover Markdown safety/partial input, nesting/tables/
  tasks/breaks, preview punctuation, valid efforts, project/fold/filter/menu/
  confirmation behavior, tool completion/metrics/clipping, user-only Fork,
  queue preview/takeback presence and unchanged transcript DOM. Existing paging,
  trees, dialogs, images and uncertain-action disconnect tests remain green.

### Real Chrome / isolated local-model verification

Used the supplied `/tmp/atto-runs/w23/cdp.mjs` with the system Node, with a
verification-only copy `cdp-polish.mjs` adding CDP theme/viewport steps and fixing
its logging of undefined eval results. No Node dependency or browser harness
was added to the repository or Go build; a temporary local formatter was used
outside the checkout. No virtual-time budget was used. Every visual iteration
was captured and PNGs were opened for inspection.

Config: `/tmp/atto-runs/w23/polish/config`, fresh HOME and git project alongside
it. `models.json` has **only** `llama-cpp/orca-local`, selecting it in settings
with effort off and a 768-token output cap; `~/.atto/cache` was copied for the
catalog. **No auth.json or provider environment keys were copied.** Built this
worktree's executable, then launched in a minimal environment:

```sh
cd /tmp/atto-runs/w23/polish/project
env -i HOME=/tmp/atto-runs/w23/polish/home USER=coolguy \
  PATH=/usr/bin:/bin:/usr/sbin:/sbin \
  ATTO_DIR=/tmp/atto-runs/w23/polish/config \
  /tmp/atto-runs/w23/polish/atto app-server \
  --listen ws://127.0.0.1:17843 --web
```

Only **two short local-model turns** were used: (1) one printf tool command,
then a tiny table, nested list and bash fence; (2) one 4-second sleep/printf tool
and exact `DONE`, allowing all three busy-composer sizes to be captured during
one turn. Other walkthroughs reused the saved session; `/diff`, extension
select/context and user-shell checks did not call the model. The verification
user extension lives only under the isolated config, uses `ui.select`, and
selected “Run the tests” through the real revision-bound shared dialog. Chrome
confirmed that the dialog closed and exactly the expected notice appeared.
`/diff` is the existing shared Go transcript Collapse (expanded in screenshots),
not a second implementation of the diff pane.

Both themes were set with `Emulation.setEmulatedMedia`. At each width Chrome
reported no document-width overflow, one semantic Markdown table, completed
tools closed, selected-model efforts only, and only the same-origin script.
Busy screenshots confirmed Stop and Steer were present at all widths. No CSP
violations or client exceptions appeared in the successful runs.

An additional no-model check attached a PNG draft and grew the editor to five
lines, then killed/restarted the gateway while Chrome stayed open. The socket
closed and reopened (101); transcript/table, image attachment and the exact
draft survived. The textarea grew to 124 px. Evidence is in
`/tmp/atto-runs/w23/polish/reconnect.log` and the reconnect screenshots.

### Screenshots (outside the repository)

All final images are in **`/tmp/atto-runs/w23/out/`**. Each filename family below
has both `-dark.png` and `-light.png`; the directory is intentionally not copied
into docs or git.

| Size | Final screenshot filename families |
| --- | --- |
| 1400×900 | `final-desktop-empty-{dark,light}.png`, `final-desktop-turn-{dark,light}.png`, `final-desktop-tool-{dark,light}.png`, `final-desktop-busy-{dark,light}.png`, `final-desktop-diff-{dark,light}.png`, `final-desktop-select-{dark,light}.png`, `final-desktop-context-{dark,light}.png` |
| 1024×768 | `final-tablet-empty-{dark,light}.png`, `final-tablet-turn-{dark,light}.png`, `final-tablet-tool-{dark,light}.png`, `final-tablet-busy-{dark,light}.png`, `final-tablet-diff-{dark,light}.png`, `final-tablet-select-{dark,light}.png`, `final-tablet-context-{dark,light}.png` |
| 390×844 | `final-phone-empty-{dark,light}.png`, `final-phone-turn-{dark,light}.png`, `final-phone-tool-{dark,light}.png`, `final-phone-busy-{dark,light}.png`, `final-phone-diff-{dark,light}.png`, `final-phone-select-{dark,light}.png`, `final-phone-context-{dark,light}.png`, `final-phone-sidebar-{dark,light}.png`, `final-phone-menu-{dark,light}.png` |
| Reconnect / growing draft | `final-reconnect-before.png`, `final-reconnect-after.png` (1400×900, dark) |

Earlier `desktop-*`, `tablet-*` and `phone-*` images in the same directory are
iteration evidence, not the final set. CDP step JSON and logs stay under
`/tmp/atto-runs/w23/polish/`.

### Verification and limits

The final verification uses the same command matrix listed above: clean
`gofmt -l .`; full/noext native and Windows vet; full/noext `go test ./...`;
full/noext race tests for server/web, server, app, ui, tui and cmd/atto; and
`go generate ./server/web` with the embedded-dist staleness test. TS core/catalog
and page workflows run through Go/esbuild/goja, not Node.

An initial noext matrix run hit the pre-existing intermittent CLI
`TestAgentsNestToAnyDepth` writer-lease error (“session is running in the
background (pid 0)”). Its focused ten-run check passed, followed by successful
full/noext reruns; no unrelated CLI changes were made. The final complete matrix (atto job 16) passed with exit 0 in 4m7s,
including both race variants after the final source/assets edits. Its full log
is `/Users/coolguy/.atto/jobs/e18024af/16/output.log`. Job 10 also passed the
ten-run CLI check and full/noext suites; job 11 passed the earlier race matrix.

Remaining manual scope: physical touch/soft keyboards, another device's LAN /
TLS access and platform-native screen-reader QA. Screenshots emulate phone size
and theme, not a physical phone keyboard. Markdown deliberately follows the
TUI conventions, not every CommonMark extension; no raw HTML or syntax-highlighter
dependency was introduced. Tool grouping remains one disclosure per command.

## Incremental browser renderer — 2026-10-10

Implemented alone in worktree `b4ec2337`, branch `atto/b4ec2337`.

### Design and behavior

- The page layout, sidebar, topbar and tabs are persistent shells. Every open
  thread owns a persistent conversation/transcript scroller, composer area/form
  and **one textarea**; inactive thread views are detached, not reconstructed.
  Small metadata input tuples memoize pane/band/queue/suggestions/activity/image
  strip/toolbar/status/toast/dialog regions. Dirty versions for list membership,
  inventory, tab titles and shared UI sites live in `View`. Deltas dirty only an
  item ID, not those regions; tab keys do not serialize transcript text.
- Transcript cache keys are `threadId + NUL + itemId`. Full item events replace
  only their row. Delta-only dirty items retain the article, disclosure, headers,
  images and Markdown/output container, replacing that container's children
  once in the scheduled animation frame. Paging also schedules that same frame,
  inserts older keyed rows and preserves the scroll-height anchor. Tail following
  applies only when already near the bottom. Snapshot-only comparisons retain
  unchanged rows through rehydration; generations still reject stale pages.
- Shared trees are cached by thread/site/id/rev, including transcript uiBlocks.
  Enablement updates the DOM controls and mutable callback gate without rendering
  a new tree. Native engine content can update within a revision-bound drawing;
  constant engine overrides remain constant during deltas. Existing `Local`
  values/drafts/disclosures and focus restoration continue to apply. Closed-site,
  removed-page and detached-tab caches are pruned.
- Inventory results are compared at refresh, and inventory versions exclude
  deltas. Busy/needs-you, filters, active thread, folding/menu choices and relative
  ages invalidate the sidebar; its outer node and scroll offset remain stable.
  Region offsets are captured **before** builders can move cached trees. Hidden
  phone sidebars retain their last visible offset. Thread scroll and pane state
  are explicitly saved/restored around tab detachment (a detached element has
  no CSS scrolling box).
- Ordinary typing still paints nothing except changing slash suggestions. All
  scheduled paints still wait for IME composition to finish. Editor growth is on
  input/draft changes/viewport or container resizing, not each streaming frame.
  Initial growth is measured after attachment so phone field metrics match the
  previous UI. Status priority fitting also tracks its actual container width.
  Connected focus/selection is left alone; a tab switch does not copy another
  thread's cursor into the persistent editor.
- No framework, new runtime dependency, HTML parsing, CSP or wire-protocol change.
  The existing image, paging, dialog, steer/queue/interrupt, event-row, phone-field
  and reconnect/reset paths remain in place.

### Real Chrome measurement procedure

Verification tools and all generated sessions/config/screenshots are outside the
repository, under **`/tmp/atto-runs/w23/incremental/`**. The supplied
`/tmp/atto-runs/w23/cdp.mjs` was used with system Node and real headless Chrome/CDP,
without virtual time. Its JSON steps are `desktop.json` / `phone.json` (baseline)
and `desktop-final.json` / `phone-final.json` (after), generated by `steps.py`.
`reset.py` creates 40 fixture sessions; the long one has 300 messages plus one
native model/context row. The script selects that fixture explicitly, loads all
older pages, verifies 301 loaded rows and 40 inventory sessions, then submits a
real worker turn, types/selects a draft during streaming and captures idle/busy/
completed screenshots.

The baseline used the original `main.ts` with only `performance.mark/measure`
around `paint`. The new renderer exposes bounded `atto-paint` entries (cleared
at 600 entries). Each run clears initial loading measurements, observes child-
list mutations under `#app`, and measures the streamed turn: 180 response pieces,
30 ms apart. Mutations whose target has a `[data-item]` ancestor are classified
as item mutations; all others as region mutations. These are synchronous paint
function durations (including its forced layout), **not** GPU/compositor timings
or physical iPhone measurements. Both viewport runs use the same Mac/Chrome.

Strata at `192.168.0.235:8081` was unreachable (curl exit 7; recorded in
`local-model-check.log`). Therefore this check used the repository's **providertest**
through a small external Go test harness, not another hosted model. The repeatable
harness is `harness/live_test.go` / `harness/go.mod`, compiling to
`server-live.test`. Its models.json contains only provider `llama-cpp`, model
`orca-local`, pointing to the loopback scripted provider; settings explicitly
select `llama-cpp/orca-local`, effort off, daemon false. A fresh isolated HOME and
ATTO_DIR were used and `~/.atto/cache` copied. **No auth.json was copied/created.**
The final server runs used a minimal `env -i` (only isolated HOME/ATTO_DIR, USER
and system PATH). No user configuration/session was modified and no server on
win was contacted. The harness was temporarily compiled in the server package
for early trials, then moved completely outside the repo; no harness or Node
package is part of the committed build.

### Before / after results

| Viewport | Renderer | Paints | Mean ms | p95 ms | Max ms | Region childLists | Item childLists |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 1400×900 | before | 182 | 10.48 | 11.10 | 11.80 | 55,146 | 0 |
| 1400×900 | after | 182 | 0.70 | 0.80 | 3.80 | 148 | 179 |
| 390×844 | before | 182 | 9.14 | 9.60 | 11.70 | 55,692 | 0 |
| 390×844 | after | 183 | 0.63 | 0.80 | 3.10 | 148 | 179 |

About **93% less synchronous paint time** and **99.7% fewer non-item child-list
mutations**. Both baseline runs replaced the textarea/sidebar nodes; both after
runs retained them. Detailed results remain in `before-*.log` / `after-*.log`.


Whole-turn region mutations after the change include legitimate lifecycle,
usage/status revision and inventory changes. Baseline item mutations are zero
because unchanged cached rows are moved out of the observed tree into a new
transcript, and the new layout is inserted at `#app`; those removals/replacements
are classified as region mutations. A zero baseline sidebar-subtree count would
likewise *not* mean the sidebar was retained: its node identity changed on every
paint. After the change, the separate middle-of-stream observation window saw
**zero region mutations and zero sidebar mutations**, only streaming-item body
updates. The textarea and sidebar child identities remained identical; the
selection stayed `[5,10]`, the draft survived, a busy overflow disclosure remained
open, and a reader at scrollTop 1 stayed at 1.

Additional real-browser checks (`checks.json`, `checks.log`) retained the older
page's existing anchor node, with less than one CSS pixel of height-rounding
movement; tab switches retained the same editor/transcript, draft, selection
`[4,8]` and exact scroll offset. A simulated Korean composition held **all**
child-list mutations at zero until compositionend, then retained the same
textarea, syllable and `[1,1]` selection. No document-width overflow appeared;
Chrome reports 14 px desktop prompt fields and **16 px phone fields**. A real
isolated server restart (`reconnect.json`, `reconnect.log`) produced a closed WS
and a fresh 101 handshake, then the **same textarea**, exact Korean draft and
`[1,1]` selection with the connection status restored; no input was resent.

### Screenshots and visual review

Opened and visually compared idle and busy screenshots at **1400×900** and
**390×844**. The layout, transcript typography/disclosures, tabs, prompt bar,
controls and status line are unchanged. The visual comparison caught an initial
phone sizing discrepancy from measuring a detached textarea; measuring its first
size after attachment fixed it before the final capture. Streaming sidebar
previews update at lifecycle/inventory changes rather than rebuilding on every
delta, as required; naturally the preview text can differ at a busy capture.

All screenshot files are in `/tmp/atto-runs/w23/incremental/out/`:

- `before-desktop-{idle,busy,done}.png`, `after-desktop-{idle,busy,done}.png`
- `before-phone-{idle,busy,done}.png`, `after-phone-{idle,busy,done}.png`
- `browser-checks.png` (extra paging/tab/IME check), `browser-reconnect.png`

### Tests, build and limits

The expanded Go/esbuild/goja DOM tests cover stable streamed-turn textarea
identity; delta-only body updates; untouched sidebar nodes/children and preserved
sidebar scrolling; revision-only shared trees; older-page node and height-anchor
preservation; open disclosures during deltas/unrelated updates; detached tab
editor/cursor/transcript/scroll retention; and unchanged-row/editor retention
through disconnect/rehydration, plus independent disclosure state for same-key
controls in different pane instances; uiBlock instance/rev identity; and tail
appends with an empty first uiBlock placeholder. Empty placeholders carry their
item key too, so a tail append cannot be mistaken for an older-page insertion.
The shim explicitly simulates detached scrollers losing their CSS box for the tab test. Existing core/catalog, phone/desktop,
Markdown, image, queue, mutation, dialog and no-resend tests remain green.

`go generate ./server/web` regenerated committed dist using Go esbuild/standalone
Tailwind only, with the staleness test passing. A temporary formatter and optional
strict `tsc --noEmit --strict --target ES2020 --lib DOM,ES2020` check lived outside
the checkout; neither is a repo dependency or generation requirement.

Final source/assets verification passed with exit 0 (atto job **47**, 4m7s).
Full log: `/Users/coolguy/.atto/jobs/b4ec2337/47/output.log`.

```sh
unset ATTO_SESSION_ID ATTO_AGENT ATTO_SUBAGENT ATTO_TOOL_CALL_ID
gofmt -l .                       # empty
go vet ./...
go vet -tags noext ./...
GOOS=windows GOARCH=amd64 go vet ./...
GOOS=windows GOARCH=amd64 go vet -tags noext ./...
go test ./...
go test -tags noext ./...
go test -race ./server/web ./server ./app ./ui ./tui ./cmd/atto
go test -race -tags noext ./server/web ./server ./app ./ui ./tui ./cmd/atto
```

The final TS/core/catalog/workflow and embedded-dist checks also passed after
regeneration. Earlier complete matrices passed too; the final run was repeated
on the frozen source after the edge-case regression tests. Isolated verification
servers and Chrome processes were stopped; only evidence files remain under /tmp.

Remaining scope is physical iPhone/iOS keyboard/touch and screen-reader QA, not
emulated viewport screenshots; the unavailable real local model was replaced by
the explicit scripted-provider fallback. This does not claim model-quality,
physical-phone frame timings, or a Windows runtime test.

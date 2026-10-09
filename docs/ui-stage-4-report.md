# Stage 4 — new browser client

Implemented on 2026-10-09 in the `atto/4c72966a` worktree.

## Entry points and architecture

- `atto serve`: `0.0.0.0:7879`, ordinary HTTP page at `/`, WS protocol at `/ws`.
- `atto app-server --listen ws://HOST:PORT --web`: explicit opt-in; existing
  WS-only listeners and stdio/Unix transports keep their behavior.
- TUI `/remote [ws://HOST:PORT]` shares its in-process Server, or uses the same
  daemon worker routes as every other facade; `/remote off` closes transport only.
- Static assets contain no secrets. Public-bind auth uses the existing token
  file and Origin policy. Fragment token is consumed once into sessionStorage;
  `atto.auth.<token>` is a WS offer, only `atto.rpc.v3` is echoed. `/ws` rejects
  query parameters. No HTTPS, PWA, service worker, QR or new runtime dependency.
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

## QA scope and behavior differences

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

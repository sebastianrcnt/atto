# The TUI as a client of atto's execution server

Research/design, 2026-10-07. This is a plan for **atto's own protocol**, not
Codex wire compatibility. Method names below are proposals unless explicitly
identified as existing. No implementation change is part of this report.

## Recommendation

Make the TUI a renderer/editor of protocol state, and make a session runtime in
`server` the sole execution owner. First run that server inside the TUI process,
with an actual JSON-RPC client boundary; then run the same runtime in a
per-session worker supervised by the daemon. Web, Java desktop, terminal and SSH
clients must use that runtime, not forward execution through the TUI.

This is not just replacing `a.agent.Run` with `turn/start`. The current standalone
server lacks important TUI semantics, while `/remote` gets those semantics only
by calling back into the TUI. Extract the TUI's scheduling semantics into the
server **before** switching its renderer. Keep `agent` and `core/transcript` as
shared, UI-independent foundations.

The most consequential decisions are session lifetime, whether client-local
pickers should hold automatic work, how interactive prompts behave with no
clients, and whether ordinary agent CLI turns join the same server ownership
model. Detach must not mean session end. An update of the frontend must not be an
update/restart of a busy execution worker.

## 1. What exists today

### 1.1 Three execution paths

| Entry point | Execution owner | Input/control | Persistence/lifetime |
| --- | --- | --- | --- |
| Interactive `atto`, `app.Run` | `app.App`: agent, hooks, extensions, MCP, goal, cancel funcs, scheduler | Editor/key handlers/slash callbacks on TUI goroutine | App holds TUI writer lease; clear/resume/normal exit clean up session jobs/tree |
| `atto serve`, `server.New` | `server.Server`'s `thread` objects | JSON-RPC `Handle`, HTTP POST + SSE | Server lease for each loaded thread; `Server.Close` ends all loaded sessions |
| TUI `/remote`, `server.NewLive` | **Still App** | `server.Live` adapter calls App through `ui.Do` | Live server owns no writer or execution; App publishes protocol notifications |
| `atto -p`, `_continue`, `_agent-turn` | `cli.RunPrint`, fresh agent/core per run | CLI flags, inbox/step boundaries | Run/background lease; normally ends jobs at completion; agent runs preserve agent children |

Interactive startup (`cmd/atto/main.go`) normally asks `daemon.Run` to start the
whole interactive binary in a PTY pane. Inside that pane `app.Run` still does all
execution. `atto attach` is a terminal byte relay, not a protocol client. Windows,
non-terminals, `daemon: false` and `ATTO_NO_DAEMON` bypass this path.

`App` in `app/app.go` creates `core.NewAgentSources`, `core.LoadExtensions` with
`tuiHost`, and `core.LoadMCP`; binds a `session.Writer`; starts agent turns in a
goroutine; sends agent events through `ui.Do` into its transcript builder; then
runs `afterRun` on the UI goroutine. Consequently queue settlement, goals,
notifications, tool-result context insertion and session switching depend on the
frontend being alive and responsive, even though the model/tool loop itself is
UI-independent.

`server.Server` (`server/server.go`) is already a real execution owner. It holds
an agent, writer, hooks, extension/MCP managers, transcript builder, cancellation,
usage and pending steers per thread; `begin` uses a server-owned background
context. It polls inboxes at 500 ms and starts/steers loaded threads. Its loaded
thread map allows multiple sessions, so "one server per session" is a deployment
and routing change, not a description of today's `Server`.

`server.Live` (`server/live.go`, `app/remote.go`) reverses that relationship:
requests call `remoteSession`, which synchronously marshals into the UI goroutine;
`Send` runs `App.submit`, including slash commands. App serializes snapshots and
publishes turn/items/goal/prompt/usage/UI notifications. Browser availability
therefore depends on App. A live thread follows the TUI's `/clear`, `/resume` or
branch movement using `thread/switched`. `turn/start` and `turn/steer` mean
"submit as typed" there, but mean explicit start/steer on the standalone server.

### 1.2 Reusable foundations, and what they do not own

* `agent.Agent` owns provider requests, conversation messages, retries, tools,
  compaction and hooks/extension calls. `RunWithImages`, `Continue`, `Compact`,
  `SummarizeBranch`, `Steer`, `Unsteer`, `DrainSteers`, `Background`,
  `StopAtBoundary`, `AtBoundary` and settings access are the execution surface.
  Settings are sampled per model request; conversation mutations/restores must
  happen outside an active run. Steers commit at step boundaries, after the
  step's tools or at a model stop, not instantly on Enter.
* `core.Bind` sets start time/prompt, session/environment, writer callbacks and
  hook/extension/MCP session identity; it opens the agent tree. It **does not
  acquire the writer lease**. `core.Open` likewise returns a writer without
  taking a lease; `core.Read` is the read-only load path. Ownership must be
  enforced before these are used for writing.
* `core.GoalDriver` already centralizes accounting, active/held/paused states,
  stop conditions, reports adopted from the goal file and transient retry
  deadlines. Its frontend must schedule `BeginTurn/Event/EndTurn/Next`, retry
  timers, priorities, snapshots and notices. Restoring an active saved goal
  pauses it; attaching to an already-running runtime must **not** restore/pause it.
* `core.Poll` fires due timers and drains the file inbox. One execution consumer
  must own it. `core.Leave` closes the tree and recursively kills jobs/clears goal
  files; this is session shutdown, not client detach.
* `core/transcript.Builder` maps agent events and saved entries to items. Live
  and replay use the same builder. Each frontend currently adds its own item
  bookkeeping/rendering and non-transcript notices. The builder is not a
  second execution engine and should stay server-side in the target.
* `session.Writer` serializes entries and branch markers, but is not the
  cross-process lock. `session/lock.go` uses OS advisory locks with diagnostic
  metadata. Persistent `.lock` files do not imply a live owner. Release funcs
  are lease-specific/idempotent; no manual lock-file deletion for takeover.

### 1.3 Duplication and material mismatches

Both App and standalone server implement agent/core construction and binding,
run start/end, settings recording, cancellation/background requests, inbox
polling, boundary reloads, transcript replay, item/block-display state, extension
status/widgets/text, session naming, usage accumulation, job/agent projection
and snapshots. Shared `core` reduces construction duplication; it has not removed
the orchestration duplication.

Important mismatches to pin before migration:

1. Standalone `newThread` sets `ag.NoGoals = true`; there is no GoalDriver or
   goal control RPC. Goal snapshots/status/prompt notifications are Live-only.
2. Standalone server has steers, not TUI follow-up queue/paused queue/send-now.
   TUI wakes `atto sleep`/job waits on user steer. Its end-of-run settlement
   drains steers, prioritizes send-now, carries over race-at-end steers, restores
   failed input and pauses queued work. Standalone interrupt just cancels.
3. Standalone extension host embeds `Headless`: `hasUI=false`, select/input
   immediately answer undefined, confirm false. App has asynchronous real
   dialogs; `sendMessage` starts an idle turn or queues after non-turn work,
   rather than merely `ag.Steer` as standalone's host does.
4. App holds events/queue/goal continuations while a modal is open and while
   queue-paused; standalone inbox work does not. This is behavior, not styling.
5. App distinguishes shell commands, compaction and branch-summary runs.
   Standalone has no user-shell control RPC and only turn-count rollback.
6. TUI model/effort changes update default settings and record the session's
   current values on the next run. Standalone setters append immediately and
   don't update defaults. App's `ctxTokens`/usage behavior across navigation is
   not identical to server's active-branch totals. App resume initially totals
   **all saved entries**; server restore totals the **active branch**. Preserve
   or explicitly change the accounting definition, not accidentally choose one.
7. App resume keeps the current execution cwd and warns if the session header's
   cwd differs; server resumes using the header cwd. A worker's cwd cannot
   silently depend on whichever client attached last.
8. Wire items currently lose data the TUI needs: tool timeout/cancellation/error,
   output clipping/full-output reference; user images; shell excluded/truncated/
   deferred-context state; branch/entry IDs. A reasoning item's `blockId` can
   become known in the builder's Saved callback **after** its completed event;
   server updates its stored item without a corresponding saved/update event.
   Already-attached clients need that update to apply subsequent `item/display`.
9. Existing SSE snapshot/event-ID coordination is useful, but events live in the
   HTTP transport. `HTTPHandler` and `ServeStdio` replace the single `Notify`
   callback: stacking transports on one instance does not compose fan-out.
   Stdio has no replay cursor/subscription/connection identity. Numeric event IDs
   reset on restart and alone cannot reliably distinguish old/new incarnations.
10. Explicit cleanup exists in `Server.Close`, but `RunStdio`/`RunHTTP` do not
    arrange that cleanup themselves. Stdio scanning does not independently
    unblock on context cancellation. Transport EOF, HTTP shutdown, process exit
    and session shutdown need deliberately separate contracts in the redesign.

## 2. TUI ↔ execution inventory

**Existing** means the standalone server implements it; **partial** includes
Live-only support or a lossy projection; **missing** means no equivalent RPC.
Local presentation belongs to clients; accepted work, its scheduling and durable
mutations belong to the server. Names in the last column are detailed in §4.

| Feature and source | Coverage now | Boundary / needed change |
| --- | --- | --- |
| Prompt/turn start (`app.go:submit/runTurn/start`) | Existing `turn/start`, `turn/started`, item stream, `turn/completed`; Live submission has different meaning | Add `input/submit` with explicit intent; server handles typed dispatch/no-model failures, input notes, settings recording and origins. Emit accepted input IDs and recoverable failed drafts; render/editor remains local. |
| Enter steer; Tab queue; Shift+Left unsteer (`queue.go`) | Partial `turn/steer`, `turn/unsteer`, `turn/pending`; queued=true only works on Live | Runtime owns queue, images, pause and carryover; add pending IDs, queue intent and `queue/resume`. Atomic ID-based takeback, not text equality. Wake waiting tools on user input. |
| Ctrl+Enter/send-now (`queue.go`) | Missing | `input/submit {intent:replace}` atomically cancels current turn, schedules leftovers then draft, and invokes GoalDriver.Replace so this interruption does not pause the goal. Return recovery on unrelated failure. |
| Esc / Ctrl+C / idle double-Esc (`app.go`, `navigate.go`) | Partial `turn/interrupt` calls cancel; Live calls App.interrupt | Encode cancel vs Esc-with-pending vs replacement; Esc first cancels user shell, else interrupts turn/retry; Ctrl+C does not auto-send leftover steers. Idle double-Esc opens local tree/previous-prompt UI, not a generic server interrupt. Modal Ctrl+C/Esc closes that modal, including agent center, without stopping underlying work. |
| Ctrl+B running tool | Existing `turn/background` and command `job/background` fields | Preserve cursor-left fallback when no eligible command. Return accepted/eligible state (acceptance is not proof background move succeeded); actual tool completion reports result. Not a session-detach method. |
| `!` / `!!` (`usershell.go`, `agent/usershell.go`) | Partial replay maps shell to generic command; no execution RPC | `shell/start`, `shell/interrupt`; one concurrent user shell per session, separate from turn cancellation. No prompt/tool hooks. Flush AddShell only after active run, preventing tool/result interleaving; `!!` persisted but absent from model context. Stream full shell-specific metadata. |
| Jobs and `/stop` (`inbox.go`, `jobs/`) | Existing `job/list/output/stop` (single job) | Add stop-all, state/count notifications; optional job/start for non-shell frontends. Supervisors can remain child processes; server controls lifetime and consumes completion events, clients do not poll/drain execution inbox. |
| Monitors/timers/inbox (`inbox.go`, `events/`, `cli/jobs.go`) | Partial automatic inbox turns and `event`; monitors listed as jobs | Add monitor create, timer list/create/cancel, pending counts and event policy. Runtime handles quiet vs waking messages, requeue, delivery during compaction/summary/prompt/paused queue. Existing tool CLI can initially retain file ingress. |
| Goals (`goal.go`, `core/goal.go`, `goal/`) | Partial `goal/updated`, GoalInfo and snapshots only Live; no goal RPC; standalone NoGoals | Adopt full GoalDriver with server retry timers. Add goal/read/set/edit/pause/resume/clear (limits and revision); hold after user input, pause on real interrupt/backtrack, explicit release, report adoption and blocked/idle/background Notification hooks. |
| Compaction (`commands.go`, agent) | Existing `thread/compact`, compaction items and auto compaction | Add run kind, queue compaction while busy, preserve unfinished-item removal, compact duration/trim notices and no goal-turn accounting for manual compact. |
| Model, effort, long context (`commands.go`, `context.go`) | Existing models/list, thread/setModel/setEffort; long context only saved/restored | Add thread/setContextMode and state fields (images, long mode, tier cap, origins); setter defaults scope explicit. Next-request application, not mid-request mutation. Preserve settings-default side effects for TUI initially. |
| `/context`, system prompt, `/request` (`context.go`) | Partial context/Loaded, token estimate, usage/limits; no breakdown/system/last request RPC | thread/context and thread/debugRequest; safe snapshot/boundary read while busy, mark deferred breakdown. Return data/download artifact, don't require client access to server paths. Debug profile belongs to worker diagnostics; frontend profile remains local. |
| Tree, navigate, labels (`navigate.go`, `tree.go`) | Partial thread/rollback by user-turn count, active items; Live idle rollback calls App.finishMove | Add thread/tree, thread/navigate, thread/setLabel and entry IDs; interrupt-then-move is one serialized operation. Preserve old branches, pending drafts, cache-identical restore, active-goal pause, unchanged files/jobs warnings. Broadcast branch invalidation to every client. |
| Branch summary / fork (`branchsummary.go`) | Partial replay branchSummary item; no summary/fork control | thread/navigate with summary none/auto/custom, run kind + streaming summary; move only after success, cancellation retains leaf. thread/fork creates separate session before selected user entry and returns input/images. |
| Resume, new, clear (`resume.go`, `commands.go`) | Existing thread/start/resume/read/list; Live refuses start and follows App switches | Attach to existing worker before opening disk. Separate cold resume from attach, client selection from session close. Add cwd policy, include inactive/read-only/archive metadata and empty sessions; new/clear creates another session. First in-process phase preserves old leave cleanup, later lifetime change needs approval. |
| Rename/archive/unarchive/session previews (`resume.go`) | Partial thread/list summaries/name; extension can name thread | Add thread/setName/archive/unarchive and read-only thread/read for unopened files. Enforce writer lease, idle or explicit close requirements and archive atomicity, not client-side filesystem writes. |
| Images (`images.go`, server/images.go, cli/view.go) | Existing base64 image start/validation/storage; tool image metadata only | Server stores/validates accepted attachments. Images during turn queue instead of steer. Add image references/fetch for replay and rollback; user images to wire items; clipboard/paste path reading stays client-local. No arbitrary server filesystem reads. |
| Extension command discovery/run/approval (`extensions.go`) | Partial Loaded reports names; commands execute only through Live text submit | commands/list/run (+ catalog changed), extensions/list/approve; server dispatches builtin/extension/skill precedence. Validate content hash on project approval, reload centrally; side model calls and extension filesystem/exec/fetch stay worker-side. |
| Extension widgets/status/blocks/text (`extensions/host.go`, `blockdisplay.go`, `exttext.go`) | Existing extension/ui, extension/notify, item/display, extText; saved displays/text replay | Reuse data-only payloads and UIState, add saved block binding notification. Server owns state/persistence; clients only style/collapse/toggle original. Local renderer must not invoke extensions or write block_display/ext_text. |
| Extension questions/confirm/input (`extensions.go`, `remoteprompt.go`) | Partial Live prompt/open/closed + prompt/answer; standalone auto-defaults | Server prompt registry + asynchronous broker replaces tuiHost/Headless split; preserve confirm/select/input types, first answer wins; explicit capability/headless/disconnect policies. No blocking dispatcher/actor while awaiting an extension promise. |
| Hooks notices, lifecycle, Notification (`app.go`, `items.go`, `hooks/`) | Existing hook notification and items for loop; partial session lifecycle; TUI idle/background/goal notifications missing standalone | Server emits lifecycle/attention notices once per session event, not once per client. Hook `permissionDecision:ask` currently means deny: don't silently add an approval mechanism. Avoid double rendering notification + same hook item. |
| MCP (`mcp.go`, `core/mcp.go`) | Partial Loaded and worker manager; no approval/status control RPC | mcp/list/approve/deny and approval prompt with config hash/scope; approval starts in server, not whichever frontend wins. Keep lazy start, manager/session identity, extension bridge and cleanup on session end only. |
| Skills/AGENTS/context reload (`loaded.go`, commands.go, core/reload.go) | Partial Loaded on start/resume, thread/reloaded from inbox `atto reload`; no direct reload/skill invocation | thread/reload with forModel distinction; context read and command catalog. Expand skill instructions server-side from loaded snapshot. Boundary reload and transactional error behavior; frontend settings updates notified separately. |
| Read-only/background sessions (`background_exit.go`, resume.go) | Partial TUI snapshots/ctrl+r; standalone resume refuses locked owner | Same-owner sessions attach as equal clients, not read-only because busy. Explicit read-only capability for legacy/foreign owner, offline history or observer, no writer/goal/inbox consumption. Refresh snapshot until legacy owner exits, then reacquire lease deliberately. |
| Usage/activity/status (`statusline.go`, activity.go, remote.go) | Existing ThreadInfo usage, TurnInfo, thread/usage; partial activity and priced/subscription metadata | Add activity/retry/tool timing/tokens and counts, long context, git/workspace, runtime memory. Distinguish session lifetime spend from active-context usage. Run custom statusLine command once in server; publish output/error, with explicit ANSI policy and memory semantics. Width/spinner/clock/cost display stay local. |
| Agent center, all-session tree (`agents.go`, cli/daemon.go) | Partial agent/list/read scoped to parent; thread/list omits full global live hierarchy | Daemon registry sessions/list + sessions/changed merges workers/saved/agent/external parent metadata; server parent/agent APIs. Client groups/filters/folds locally, selects target without moving another client. No pane-state proxy for execution truth. |
| Agents, agent messages/tasks (`cli/agent.go`, agentstate/) | Partial list/read only; execution via _agent-turn processes/jobs and inbox files | Migrate spawn/task/send/stop/close/report/wait to agent RPC backed by workers/registry, retaining hierarchy/slots/worktrees/final answers. Attach to a busy child worker, never concurrently resume its writer. External parents remain durable tree anchors, not fake running agents. |
| Exit/background continuation/update (`background_exit.go`, `cli/bgrun.go`, `pane.go`) | Partial tool-background only; today detach leaves whole TUI, non-daemon background cancels/replays turn | Separate client/detach from session/close. Keep old menu behavior during in-process parity phase; daemon mode detach leaves current request/tool/queue/goal/extensions/MCP untouched. Explicit shutdown stops tree/jobs/hooks and releases lease. Updating TUI reconnects to old worker. |
| Ancillary UI: copy/mentions/login/update/render settings (`copy.go`, mention.go, login.go, updatecmd.go) | Not a general execution protocol | Copy/OSC52, search, editor history, selection, renderer, mouse, expansion and frontend update are local. Mentions need workspace/list when client lacks worker FS; login/logout operate server-host credentials through local privileged auth flow, never broadcast secrets in prompt/SSE. Reload model availability afterwards; preserve first-run no-model UI. |

## 3. Target architecture and ownership

### 3.1 In-process first, with a real boundary

```
TUI editor/render state ── JSON-RPC client ─┐
web client / Java client ── HTTP/stdio ─────┼─ protocol dispatcher/event hub
                                         └─ session runtime (one per session)
                                              agent + scheduler + transcript
                                              writer lease + writer
                                              goal + inbox + hooks + extensions
                                              MCP + jobs/agent coordination
```

Use `server.Server` as the protocol facade with a session-runtime component;
extract an internal package only if it avoids import cycles, not a second public
execution API for App. A local Go client may use channels/`io.Pipe` instead of
kernel sockets, but must marshal/decode the **same JSON payloads** and pass
through the same dispatcher/subscriptions as real clients. An App pointer, agent
pointer, arbitrary closure or `server.Live` callback is not that boundary.

App retains editor/drafts, key interpretation, modal rendering, selection,
scrollback/expansion, local notifications (clipboard failure, redraw, update),
connection state and a protocol-state reducer. It no longer owns `Agent`, writer,
GoalDriver, hooks/extensions/MCP, inbox poller or execution cancel funcs. The
server produces completed/open items; App does not replay raw session entries
or build live items from raw agent events. A lossless DTO-to-render adapter can
reuse existing block rendering and `transcript.Item` shapes without running a
second Builder.

Use one serialized session command/scheduler lane, plus the existing agent
run goroutine. Agent events and run completion re-enter that lane. Slow shell,
provider, hook, extension, MCP and disk operations cannot hold a dispatcher or
notification lock while awaiting frontend input. Define permitted concurrent
Agent calls rather than treating its several mutexes as a general safety proof.
Every accepted input/operation is placed in server state before acknowledgement;
start/interrupt/branch/close races have one arbiter.

Unify notifications in a transport-independent hub. It orders session state and
items, takes consistent snapshots, assigns an incarnation/cursor, and has
bounded nonblocking subscriber queues/replay. `Server.Notify` can remain a test
observer temporarily, not the only transport destination. Slow/disconnected
clients never stop a model/tool turn; gaps cause reset/resnapshot. Responses and
notifications can race on separate transports, so clients must correlate IDs and
reduce idempotently, not assume the start response arrives before turn/started.

For exact TUI parity, client-local modal-open/close signals temporarily install
an **automatic-work gate**: pending events/queue/goal continuation wait, running
turns do not. Scope the gate to that client, release it on detach/connection loss,
and cap its life. Shared execution prompts supply their own gate. Do not let
an indefinitely open unrelated browser model picker stall the session by default.
Whether to retain this old modal scheduling effect long-term is a user decision.

### 3.2 Out-of-process: daemon supervises session workers, not execution TUIs

Recommended deployment: one `_session-server` process per active session, each
running one runtime (and potentially one HTTP-free protocol endpoint). The daemon
owns a private directory/socket and a registry mapping session ID → worker
identity, endpoint, protocol range, cwd, state, parent/worktree and client count.
A worker holds its own `KindServer` OS writer lease. Daemon owns discovery,
creation/deduplication, supervision and user-scoped routing; it does not write
that worker's session or drain its inbox. A gateway may route `thread/*` methods
across workers, so `atto serve` can still offer a multi-session web interface.
A single process hosting multiple runtimes is possible, but is not the recommended
first deployment: it increases failure and version-update blast radius.

Start path: registry serializes find-or-start → worker acquires lease → creates or
loads/binds runtime → publishes ready endpoint → client attaches and reads a
snapshot. Repeated starts converge on one worker. Never trust endpoint/lock PID
metadata instead of the OS lease; stale endpoints are checked against worker
identity and peer authentication. If a legacy writer owns the lease, attach to
its protocol if available, otherwise provide a read-only disk snapshot or a clear
"owned elsewhere" error. Do not steal its lease.

Unix sockets use same-user peer checks/private permissions. Stdio remains useful
for local embedding and SSH forwarding; **transport lifetime is not session
lifetime**. For a durable worker, use a stdio bridge to its socket; EOF detaches
that bridge, not the worker. Direct embedded/no-daemon mode can explicitly retain
old process-bound lifetime. Windows needs a named-pipe or authenticated loopback
counterpart, or a documented initial in-process fallback; today's daemon is Unix.

Cold load retains original session start time and exact active branch messages
for prefix caching, but may pause a saved active goal as today. Reattach does not
call Bind/Restore/SessionStart again; it snapshots live state. SessionEnd and
`core.Leave` happen only on session close/retirement policy, not detach. Decide
how long idle workers remain resident; before retiring, snapshot all necessary
state and ensure jobs/timers/active goals/prompt obligations are accounted for.
A session with a future timer is not equivalent to an empty idle pane.

### 3.3 Mapping the current pane model

Today a pane is a PTY + TUI process + output mode tracker; multiple attachments
share the **same editor, cursor, size and keys**. OSC 7337 markers authenticate
ready/session/name/state/detach/switch/new/open; attach requests SIGUSR1 redraw.
This is screen sharing, not equal independent clients.

Use a transition with two layers:

1. Keep PTY panes and legacy `atto attach` working while the pane's TUI becomes
   an RPC client of a daemon worker. A TUI crash then kills a view, not execution.
   Daemon panes reference a stable session ID; worker state is authoritative.
2. Make normal `atto attach <session>` launch a fresh local TUI protocol client.
   Each terminal has its own draft/size/navigation. Keep explicit legacy screen
   share only if desired. New/open/switch markers become local client navigation
   or registry requests, not global session switches. Execution discovery/status
   no longer relies on OSC markers or a pane's repaint loop.

The daemon's idle-exit condition becomes no live workers/required schedules and
no views, not no panes. `daemon kill`/stop UX must distinguish closing a view
from stopping execution; `stop -force` must clearly warn it closes sessions and
children. Pane numeric IDs can be transitional aliases; session IDs are stable.

### 3.4 `/remote`, background continuation and lease handoff

`/remote` enables/revokes an HTTP gateway/link scoped to the selected session;
it does not start `NewLive`. Use the same event hub and runtime as the TUI. Link
creation/revocation belongs to the daemon/gateway, with tokens never exposed to
other clients accidentally. `/remote off` closes remote connections/revokes the
link, not the session. Keep current explicit link lifecycle/QR behavior initially;
decide whether the link survives its enabling TUI's detach. `atto serve` should
attach or create via the registry rather than independently opening a session
already owned by a worker; its shutdown normally stops the gateway only.

Today's non-daemon "Run in background" is a **restart of execution**:
DiscardPartial cancels a request, tries to background a running tool, records
cancelled remaining tools and drops pending input, then starts `_continue`.
`session.StartBackground` transfers the inherited advisory lock without an
unlock gap; an adoption acknowledgement updates metadata, and failed transfer
retains the TUI lease. `_continue` runs `RunPrint`, restores the goal and uses
`Agent.Continue` to repeat unfinished work. This cannot preserve an in-flight
provider request, JS promises or MCP connections.

Do not build the daemon migration around exporting that in-memory state. Start
new durable sessions in workers **from the beginning**. In-process phase may
keep `_continue` and its handoff for legacy non-daemon exit. After daemon workers
are default, "Run in background" is just detach: request, tool, pending queue,
goal retry, extensions and MCP all continue unchanged; no DiscardPartial, no
restart/repeated request and no writer lease transfer. Retain handoff only for
explicit legacy/print workflows until those migrate. A busy *server* binary
update still cannot preserve arbitrary in-memory execution: finish it on the
old binary, or explicitly cancel/recover, never pretend a frontend update moves it.

### 3.5 External parents and agent turns

`cli/agent_external.go` supplies a per-project mapping (canonical project-root
hash) to a durable `session.NewExternal` parent when `atto agent` is run from a
shell without session env. It serializes mapping creation/removal. This parent is
an organizational session with name/External metadata, not a model loop.

`atto agent spawn/task/send/stop/close` currently manipulates agent state,
turn locks, tree shutdown markers, slots, worktrees and inbox files. Spawn makes a
child session immediately. Tasks/messages use envelopes `NEW_TASK`, `MESSAGE`,
`FINAL_ANSWER`; `_agent-turn` runs as a **job process of the parent**, acquires a
slot, calls `RunPrint` with the child's session run lease and agent prompt,
reports usage/final answer, then retires or starts a successor when waking inbox
work raced with its final poll. Child agents can start descendants. Closing a
root uses tree locks/closed markers so teardown cannot race with new descendants;
worktree removal refuses dirty trees unless forced. None of this is the server's
current agent/list/read execution model.

Stage that migration separately, but include it in the target: each child has
one worker holding its writer lease; `atto agent` becomes a thin local protocol
CLI. Preserve root/relative addressing, presets, instructions captured at spawn,
model/effort restrictions on external callers, slots, depth limit, worktrees,
final-answer delivery/consumption and race-at-retirement behavior. Parent workers
send envelopes via session routing; final answers must not be emitted twice by a
worker and a leftover supervisor. Busy child UI attachment is now allowed.
An attached client does not replace the child's preset/role or bypass its policy.

External anchors need no worker until actually used for execution; registry can
list them and route child state/events without a model. Their mapping disappears
only when its children are closed, matching current lifecycle. Preserve recursive
shutdown as an explicit operation. Detaching any parent frontend never closes
its tree. During mixed-version transition, `_agent-turn` still owns its child:
read it but don't simultaneously start a child worker; either finish old turn
then migrate or keep it on the legacy path.

## 4. Proposed atto protocol revision

Use an atto protocol revision/capability set, not external compatibility aliases.
Document DTOs and semantics before implementation. Keep useful existing names,
but remove "Live vs standalone" as a source of different execution behavior.

### 4.1 Common envelope, connections, identity and recovery

* `initialize {client:{name,version}, protocolVersions:[...], capabilities:{
  prompts, images, interactive, ansiStatus?}}` returns protocolVersion,
  serverInstanceId, clientId, capabilities, settings, limits and defaultThreadId
  if scoped. Never return provider secrets/config ModelRef with API keys.
* `thread/attach {threadId, after?:Cursor, readOnly?:bool}` returns the same
  snapshot DTO as read plus a subscription. `thread/detach {threadId}` drops
  that client's subscription/gates; no cancel or lease release. HTTP clients
  can keep snapshot + SSE instead, with the same cursor semantics.
* Cursor is `{serverInstanceId,eventId}`; every notification has that identity,
  threadId and state revision/generation where applicable. `events/reset`
  instructs snapshot replacement when replay is unavailable. Stdio/socket
  notifications carry cursors too. Hub is independent of HTTP.
* Client changes do not create a writer: they hold capabilities/subscriptions,
  not `session.LockTUI`. Read-only is an authorization/legacy-view capability,
  not a synonym for "another client attached".
* Mutation requests accept `requestKey` (dedupe within a bounded worker history)
  and, for destructive/config/prompt operations, `expectedRevision`. A lost
  response to accepted input must not cause a second model turn on retry.
  Don't dedupe inputs merely because their text matches; identical intentional
  prompts are valid. Process-restart guarantees require durable accepted input
  records if promised; otherwise report the dedupe horizon explicitly.
* Errors add machine-readable `data:{reason,currentRevision?,retryable?}`:
  busy, noModel, readOnly, stalePrompt, alreadyCommitted, revisionConflict,
  ownedByLegacyWriter, unsupportedCapability. Display strings remain friendly.
* Stable pending/operation/turn IDs include incarnation or persisted sequences;
  items include `entryId` where recorded and a `transcriptGeneration` when a
  branch/replay replaces item identity. Today's sequential replay item IDs can
  be reused after navigation: do not apply an old delta/display to a new branch.

### 4.2 Execution inputs and controls

| Request | Params / result and semantics |
| --- | --- |
| `input/submit` | `{threadId, input?, images?, intent: auto\|steer\|queue\|replace, requestKey}` → `{inputId,status:started\|steered\|queued\|done,turnId?,operationId?}`. Auto reproduces Enter, including image queueing and non-turn busy queueing; replace reproduces Ctrl+Enter atomically. Text/command parsing is a documented server execution grammar, not arbitrary TUI key simulation. |
| existing `turn/start`, `turn/steer` | Keep explicit strict APIs (busy/no turn are errors), delegating to same scheduler. `turn/start` is not a hidden slash-command interface; prefer `input/submit`/commands/run for typed behavior. Steer support remains text-only for parity. |
| existing `turn/unsteer` | Extend `{inputId}`; return removed input/attachments/origin for editing. Remove matching text fallback in the new revision; queued bool becomes unnecessary when IDs encode kind. Rejection after commitment is atomic. |
| `queue/resume` | `{threadId}` releases queue-paused; an empty Enter while idle chooses this, else goal held release. `turn/pending` contains ID-bearing steers/queued, pause state and pending replacement, not just string arrays. |
| existing `turn/interrupt` | `{threadId,turnId?,mode:cancel\|sendPending}` with no turn may pause a retry as Esc does; result `{interrupted,operationId?}`. Cancelled input returns to its originating client; sendPending launches leftover steers after cancellation. Replacement uses input/submit, not two client requests. |
| existing `turn/background` | `{threadId,turnId?}` → `{accepted}` or not-eligible error; tool item completion is authoritative. |
| `shell/start`, `shell/interrupt` | `{threadId,command,exclude?,requestKey}` → `{shellId}`; interrupt by shellId. Stream independent user-shell item state; completion may show `contextPending:true` until flushed after run. |
| `thread/compact` | Explicit strict compact still supported; commands/run or input queue schedules compact if busy. Return operation/turn ID and kind. |
| `goal/read`, `goal/set`, `goal/edit`, `goal/pause`, `goal/resume`, `goal/clear` | Session-scoped objective/limits/revision; set may return a replace-confirmation prompt/operation. Resume resets audit streaks/releases hold as current UI does. Pause/clear send internal context and StopAtBoundary for active turn. Never infer new objective from arbitrary client state. |

New notifications: `input/accepted`, `input/committed`, `input/recovered`
(target origin client, replayable via its attach state), richer `turn/pending`,
`turn/activity {phase,runKind,startedAt,toolsRunning,estimatedOutputTokens,
retry?:{attempt,of,retryAt,error}}`, and `thread/status` for counts/workspace/custom
status output. Retain turn/items/usage/goal/reloaded/UI notifications.

Draft recovery is **not** a global shared editor. Server retains failed/taken-back
input until delivered/acknowledged by its submitting client, or offers it as an
explicit pending recovery on reconnect. Client merges it with current draft just
as App.restoreToEditor does. Detached client input doesn't disappear or get pushed
into every other client's editor. Define TTL/persistence; don't claim crash-safe
queues without journaling their state.

### 4.3 Session/context/branch APIs

* `thread/start {cwd?,model?,effort?,options?}` creates a session; noModel can be a
  valid initialized runtime state so first-run `/login` still works.
  `thread/resume {threadId,cwdPolicy?:saved|explicit,cwd?}` cold-opens only when no
  worker owns it; otherwise attaches. Reject ambiguous cwd changes on busy/live
  workers. `thread/read {threadId,include?:[items,context,tree],offline?:bool}` can
  serve read-only history without opening a writer or extension/MCP host.
* `thread/setModel` / `thread/setEffort` add `{saveDefault?:bool}` and origin/
  revision behavior; TUI sends saveDefault=true initially. Publish thread/updated
  to **all** clients when settings change. `thread/setContextMode {mode:normal|
  long}` records the context entry and reports cap/limit.
* `thread/context {view:summary|breakdown|system}` returns Loaded, breakdown,
  estimates/mode/limits and busy availability. `thread/reload {forModel?:bool}`
  schedules at boundary; `forModel=false` must not start an idle model turn.
  Include safe frontend settings/catalog changes in notifications.
* `thread/debugRequest {format?:json}` returns a privileged artifact/reference
  for the last raw request, with explicit sensitive-data warning. Do not
  broadcast it; `runtime/diagnostics` can provide worker RSS/profile artifacts.
* `thread/tree {threadId}` returns nodes, entry IDs/types/text previews/parents/
  leaf/labels, plus image refs and revision. `thread/navigate {entryId,
  summary?:{mode:none|auto|custom,instructions?},interrupt?:bool,
  expectedRevision}` → operation then `thread/branchChanged` + snapshot cursor,
  input/images to edit. TUI may choose summary locally; headless caller can
  explicitly choose none. `thread/rollback {numTurns}` delegates to navigation.
* `thread/fork {entryId,cwd?}` → new thread + input/images, leaving source workers
  alone unless explicit close requested. `thread/setLabel {entryId,label}`,
  `thread/setName {name}`, `thread/archive`, `thread/unarchive` handle writes with
  the owning runtime/catalog lease. Archive while running is rejected or requires
  explicit close first; detached observers do not move an active writer's file.
* `thread/close {reason,stopChildren?:bool}` is explicit execution termination:
  cancel and await runs, flush writer, hooks/session_end/extensions, stop relevant
  jobs/tree, close MCP and release lease. State/closed notification reaches all
  clients. Order must let end hooks still record, as Server.Close already does.
* `sessions/list {cwd?,archived?,branch?,includeAgents?,includeExternal?}` and
  `sessions/changed` are registry/gateway APIs, not per-worker filesystem scans
  by every frontend. Include durable/live status, parent/path/role/worktree,
  goal held/retry/prompt state, last answer, usage, timestamps and legacy ownership.
  Extend existing thread/list for detailed saved summaries if useful; don't
  expose private socket paths over publicly scoped web links.

`thread/switched` currently follows the TUI globally. Replace that model with
client-local selected thread. Branch change of one session invalidates everyone's
view of **that session**; a TUI selecting another session does not redirect other
clients. A legacy scoped `/remote` link may optionally follow its creator's
selection only as an explicit view policy, not an execution primitive.

### 4.4 Jobs, timers, commands, approvals and attachments

* Retain `job/list/output/stop`; add `job/stopAll`, `job/start {command,cwd?,label?}`,
  `monitor/start {command,every,until,...}` and `job/updated`. Background tool CLI
  can keep writing supervisor state/inbox files initially. Server owns polling
  and counts; job/output remains bounded; optionally subscribe to job output.
* `timer/list`, `timer/create {when|dueAt,message,schedule?}`, `timer/cancel {id}`
  + `timer/updated`; parse relative time in worker clock and return absolute due
  time. `event` retains source/title and reference; event consumption isn't a
  frontend acknowledgement. `inbox/push` is restricted local tool/agent ingress,
  not an unauthenticated injection channel.
* `commands/list {threadId}` → entries with stable command ID/name/args/help,
  extension/skill origin and version; `commands/run {name,args,requestKey}` →
  done/queued/operation/prompt. Server handles execution builtins/skills/extensions
  and precedence; clients own `/copy`, renderer/navigation/attach/quit frontend
  commands. Unknown local-only commands on web receive a clear unsupported result,
  not forwarded keypresses. `commands/changed` follows reload/registration.
* `extensions/list/approve {name,hash}` and `mcp/list/approve/deny
  {name,hash,scope:once|project}` with threadId + expected config identity. Actual
  supported approval scopes must match current MCP semantics; don't invent
  persistence for once without defining it. List reports approval target/hash;
  stale config cannot be approved by answering an old prompt.
* Images get content-addressed `imageId`/metadata in user and tool items;
  `image/read {imageId}` or an authenticated bounded artifact URL returns bytes.
  Base64 start remains usable within negotiated total request limits; optional
  `image/upload` avoids repeated queued-image payloads. Server path fields are
  diagnostic, not permission to read arbitrary files. Rollback/fork return image
  refs a client can fetch. Prepared-placeholder generation is idempotent so the
  TUI and server cannot insert placeholders twice.
* `workspace/list {threadId,query,...}` supports mentions when a remote client
  lacks the cwd; scope it to that workspace. No general file-read protocol is
  needed merely to migrate UI.

### 4.5 Lossless item and state DTOs

Keep the current item stream and display-only extension data. Extend `Item` with
entryId, origin/clientId/inputId, startedAt, timeoutMs, canceled/error, dropped/
truncated/full-output artifact, user images, shell source/excluded/contextPending
and full goalStatus data needed by UI. Compaction/branch summary duration must
survive wire mapping. Stream a metadata update when MessageSaved assigns blockId
(`item/updated` must cover non-command items) before later display changes.
Don't send provider tool schemas/API keys as UI model configuration.

Snapshot contains completed **and open** items, current turn/operation/runKind,
pending inputs/recoveries, goal incl. held/retry, prompts, Loaded, model/effort/
image support/context mode, usage definitions, jobs/timer counts, extension UI,
workspace/git status and latest custom status. Reconnect restores execution
state, not scroll/cursor/modal selections. Distinguish ephemeral notices from
persisted transcript: preserve session-local notices during worker life and
explicitly document which do not survive cold replay.

### 4.6 Interactive prompts: asynchronous server-owned objects

Prefer extending existing `prompt/open`, `prompt/closed`, `prompt/answer`, rather
than introducing mandatory server→client JSON-RPC requests. The current HTTP +
SSE path cannot receive a synchronous RPC response on its outbound channel, and
routing one request to several equal clients needs a prompt arbiter anyway.
The prompt registry works across every transport and reconnect.

Proposed prompt fields: `id,revision,threadId,kind:select|input|confirm,title,
subtitle,options:[{id,label,description}],default?,origin:{extension|mcp|goal|
operation},operationId?,audience:session|client,clientId?,expiresAt?,onNoClient`.
Answer: `{threadId,id,revision,optionId?|text?|confirmed?|cancel?}`. One valid
answer wins atomically, emits closed with answering clientId, settles callback
exactly once; stale/duplicate answers get typed errors. Support selection by
stable IDs and paged catalogs for large trees/resume lists, not a capped list
whose indices drift. Snapshot includes outstanding prompts.

* Execution questions (extension select/confirm/input, MCP approval, goal replace
  confirmation) live in server state; every eligible session client can answer.
  No code/markup/Go callback crosses the wire. Original extension promise settles
  on extension runtime goroutine; extension timeout pause while asking remains.
* Client convenience pickers (model catalog, session search, tree display,
  renderer selection) are normally local; they send explicit mutation requests.
  A "summarize branch?" operation can be server-prompted when caller requests
  interactive behavior, or explicit summary mode when chosen locally.
* Prompt waits do not block dispatcher/scheduler locks, otherwise extension
  callbacks during reload/session_end can deadlock. On reload/end/cancel,
  close relevant prompts and settle promises with documented cancellation/default.
* Compatibility phase: no interactive capable client → preserve Headless default
  answers; another shared prompt open → preserve current default-on-busy behavior
  initially. Durable-worker phase needs explicit per-kind policies: deny/default
  for approval, or wait with deadline for an input, never auto-approve. Losing the
  last frontend must not manufacture an affirmative answer. Decide whether
  pending questions survive reconnect indefinitely or timeout/default.
* `ctx.hasUI` currently is true for TUI and false for standalone server. Define
  it as an interactive **host policy/capability**, not "web is second-class" or
  a value that unpredictably flips per extension call. Pick a stable per-session
  policy and separately expose connection presence if needed. No-client idle
  session-start questions and a client attaching late are explicit test cases.
* Credential login prompts remain client/private auth flow, never session-wide
  prompts. Auth may run as a local privileged CLI/daemon service returning only
  availability/error/browser flow data; no raw key broadcast. Token/key entry
  and reconnect logs must not leak credentials.

## 5. Migration in commit-sized steps

Estimates are **one capable coding agent's focused working days**, including
fixture/tests/review, not wall-clock calendar promises. Phases are mostly serial;
protocol/daemon/renderer work can be parallelized only after DTOs and lifecycle
invariants are agreed. Allow a further 20–30% integration contingency if frontend
or daemon behavior decisions change. Rough full target: **40–63 working days**;
in-process parity milestone: **20–31 days**. Native Windows durable transport and
Java/phone UI feature work are not included (they consume the same protocol).

| Phase | Commit-sized sequence | Effort | Main risks / exit gate |
| --- | --- | --- | --- |
| A: parity contract & harness | (1) protocol revision/DTO fixtures; (2) scripted fake-provider/execution harness; (3) capture TUI scheduling + transcript golden cases and document intentional differences | 3–4 d | Missing subtle UI-dependent scheduling; compare request bytes, entries, items and outcomes, not just rendered final answer. |
| B: event hub & client reducer | (1) transport-independent ordered replay hub; (2) connection/subscription/cursor/dedupe/error DTOs; (3) in-process JSON client + reducer and snapshot tests; (4) lossless items incl. late block binding | 4–6 d | Snapshot/event race, reused IDs, backpressure; simultaneous stdio/SSE/local clients see identical ordered state. |
| C: scheduler parity in server | (1) input IDs/steer/wake/takeback; (2) queue/pause/recovery; (3) Esc/Ctrl+C/send-now settlement; (4) inbox priorities; (5) GoalDriver/retry accounting; (6) user shell context flush | 6–9 d | Largest behavior risk: tool-result adjacency, errors/race-at-end, active-goal hold. Harness tests old/new execution traces before App conversion. |
| D: remaining server features | (1) prompt host and extension catalog/commands; (2) MCP/extension approval; (3) reload/context/long mode/default settings; (4) tree/navigation/summary/fork/labels/archive; (5) status command + images + diagnostics | 5–8 d | Deadlocks with extension promises; exact restore/cwd/accounting; destructive operation conflicts. Every inventory row has RPC fixture and parity test. |
| E: TUI becomes local client | (1) protocol item-to-block rendering adapter; (2) replace input/key execution calls; (3) replace goal/jobs/status/session/extension reads; (4) remove direct ownership fields; (5) `/remote` uses common facade, retain legacy background path | 2–4 d | Optimistic UI duplication, changed notices/focus, no-model/login; app rendering golden tests + local/web simultaneous parity; App cannot reach agent/writer. |
| F: daemon session workers | (1) worker entrypoint/socket readiness/lease; (2) registry find-or-start/routing; (3) connect/attach/stdio bridge; (4) PTY view-to-worker reference and crash survival; (5) gateway serve/remote routing + revocation | 6–9 d | Writer split-brain, stale sockets, cwd/env and secrets, mixed protocols; kill view during fake stream/tool and prove same worker/turn continues. |
| G: frontend lifecycle & center | (1) detach vs close UX; (2) independent local atto attach; (3) sessions registry center + tree; (4) idle policy/worker-version update; (5) retire handoff path for managed sessions | 4–7 d | Intentional lifetime change, orphan resources, scheduling timers without views; restart/update TUI does not repeat provider request; session close still cleans tree. |
| H: agent/print convergence | (1) protocol-backed agent spawn/tasks/messages; (2) child worker slots/worktree/shutdown; (3) final-answer/wait/report parity and external anchors; (4) migrate saved print/background workflows; (5) remove obsolete execution paths | 7–11 d | Duplicate final answers/turn consumers, close/spawn races, print output contract; existing CLI/agent regressions plus protocol tree tests. |
| I: release hardening | (1) chaos/backpressure/security/version tests; (2) legacy read-only fallback + rollout docs; (3) cleanup imports/dead adapters, review all inventory rows | 3–5 d | Lost accepted input, schema skew, platform shutdown; full offline test matrix and no-code-path execution fallback on managed sessions. |

Implementation should not force a one-shot App rewrite. Keep the legacy runtime
only behind an explicit temporary test/rollout switch; run the same scenarios
against both. Do not "shadow" production model turns twice. A transitional
NewLive wrapper around the old App can validate a client reducer, but it is not
the in-process architecture exit gate: the UI must stop owning execution.

### 5.1 Test strategy

1. **Protocol-level harness**: fake streaming provider (`httptest`, like
   server/server_test.go), deterministic clock/retry/random verb, fake tool
   executor with barriers for tool start/output/end, isolated ATTO_DIR/HOME,
   temp workspace. Drive the same JSON scenario over channel transport,
   `io.Pipe` stdio, HTTP/SSE and later Unix socket. Assert responses, event
   sequence/cursors, snapshots, written session entries, request bodies and
   cleanup. No real credentials/models required.
2. **Golden request/transcript parity**: keep `agent/golden_test.go` exact byte
   assertions for prefix cache; extend `core/transcript` replay/display/shell/
   branchsummary fixtures and App golden rendering (`app/testdata`). Normalize
   timestamps/IDs only where not semantically meaningful. Pin hook/goal notes,
   model-change notes, image placeholders and branch restore; never approve a
   golden rewrite merely because final text looks right.
3. **Scheduling matrix**: idle/turn/tool/model-stream/compact/summary/user-shell;
   Enter/Tab/Ctrl+Enter/Esc/Ctrl+C/Ctrl+B; queued slash command and images; normal
   completion/error/prompt-block/interrupt. Check steers at boundary and during
   completion race, duplicate text takeback by ID, FIFO, queue-paused release,
   recovered draft origin, events quiet/waking and sleep wake behavior.
4. **Goals/prompts/extensions**: active/held/paused/retry/blocked/completed,
   user steer accounting, pause/edit/clear at boundary, resume vs reattach,
   goal files/report adoption; two clients race to answer, detach all, timeout,
   reload/session-end while asking, late display against abandoned branch,
   extension messages idle/compact/running, MCP stale approval hash. Assert one
   hook invocation/answer/notice, not one per client.
5. **Snapshot/reconnect**: client attaches during open text/tool; snapshot cursor
   boundary; notifications arriving before request response; slow reader, byte
   ring overflow, reset, worker incarnation reset, branch generation change,
   unknown optional fields, incompatible revision. Queues/recoveries/prompts/UI
   appear after reconnect. No event gap or duplicate item on snapshot+replay.
6. **Lease/process tests**: session lock/handoff existing OS tests, worker creation
   race, symlink canonicalization, stale metadata/socket, archive vs active owner,
   file close on Windows, worker crash, TUI SIGKILL/update, transport EOF and
   dropped SSH connection. Show the same live turn/tool PID continues when only
   a view dies; explicit session close still stops jobs and descendants. No
   automatic resumption of uncertain side-effecting tools after worker crash.
7. **Agent tree**: external-parent reuse, nested addressing, queued task vs
   consumer retirement, slots/depth, final answer once with wait/report,
   close/spawn serialization, dirty worktree refusal, mixed legacy child leases,
   independent child client attachment and server-only shutdown propagation.
8. Run `go test ./...`, focused `-race` suites for server/app/events/agentstate/
   agent/session/daemon, and platform CI. Add dependency checks prohibiting App imports
   of agent execution/writer APIs (render value types may need extraction);
   frontend tests assert no filesystem inbox drains or background subprocesses
   started by render code.

Research verification here: selected isolated offline tests passed:

```
ATTO_DIR=<temporary directory> go test ./core/transcript ./server \
  -run 'Test(StdioTurn|HTTPAndSSE|.*Rollback.*|.*Display.*|.*Shell.*|.*Snapshot.*|.*Pending.*)' \
  -count=1
```

These use fake providers, not real models. This verifies existing foundations,
not the proposed architecture. No full-suite or prototype claim is implied.

### 5.2 What can disappear or become smaller

After parity + daemon rollout, remove `server.Live`, `remoteSession`, App remote
item/usage/pending/goal/prompt mirroring, raw-event transcript builder and replay,
App writer/lease/bind/goal/inbox/execution cleanup, duplicate extension hosts and
UI-persistence code, App queue/send-now/background state machine. `/remote` shrinks
to gateway controls/QR rendering. Keep block renderers, editor/input grammar,
client reducer, local dialogs, original-display toggle and tool grouping.

For daemon-managed sessions, remove DiscardPartial-on-exit, `_continue` spawn,
background lease handoff and read-only-while-another-atto-runs UX. Legacy/standalone
print compatibility may keep those until explicitly migrated. Replace agents
center filesystem scans/pane markers with catalog state. PTY screen/mode sharing
and OSC redraw markers can be dropped if legacy screen sharing is not retained.
`_agent-turn` RunPrint orchestration and per-turn extension/MCP teardown go away
when agent workers take ownership; job supervisors themselves need not.

Do not prematurely remove `core`, `agent`, `session` OS locks, durable tree/slot
coordination or the file ingress used by `atto job/goal/reload` from tool shells.
They protect different boundaries. Later RPC CLI ingress can simplify polling,
but persistent inbox/job state remains useful for recovery/external callers.

## 6. Decisions for the user

1. **Lifetime/default UX:** should `/quit`, Ctrl+D and terminal loss detach by
   default once worker mode exists, with a separate `/close` to stop everything?
   Should `/clear` and session selection leave the old session running (equal
   clients) or explicitly stop it as direct TUI resume does today? Recommend
   detach/select keep alive; explicit close stops. This is an intentional behavior
   change deferred until after in-process parity.
2. **Durability policy:** keep idle workers indefinitely, idle-expire, or keep
   only while busy/jobs/timers/goals/prompts exist? What resource limits and
   orphan-session management? Timers with no attached client need a live scheduler
   or daemon wake scheduling; restarting from disk is not live continuation.
3. **Multiple clients:** independent drafts/navigation is recommended. Does any
   authorized client get to interrupt/rollback/change model/close, or should
   destructive operations require confirmation/roles? Equal clients does not
   require shared drafts or last-attached-client ownership.
4. **Interactive unattended behavior:** default/deny vs wait/deadline for
   extension prompts; what does stable `ctx.hasUI` mean? Should client-local
   pickers still pause automatic work? Recommend server-owned execution prompts,
   local pickers with bounded parity gate initially, no automatic approval.
5. **Execution cwd and settings scope:** saved cwd on cold resume vs the TUI's
   current-cwd convention? Are model/effort switches global defaults or session
   only? Recommend explicit saved/override cwd at creation and explicit
   saveDefault flag; don't let attachment change a worker's cwd.
6. **Accounting:** lifetime spend vs active-branch context usage; which does
   current statusLine receive after rollback? Recommend exposing both and pinning
   old TUI display until intentionally changed. Also decide whether memory means
   worker RSS, client RSS, or both in custom status input.
7. **Transport/screen sharing:** retain legacy PTY `attach` separately, or replace
   it fully with independent protocol TUI clients? Initial Unix durability only
   with Windows in-process fallback, or fund Windows transport immediately?
8. **Gateway exposure:** should `/remote` links outlive the enabling TUI and
   follow its selection, or be stable session-scoped? Should `atto serve` manage
   new session workers or only expose existing ones? Recommend scoped/revocable
   links and daemon routing; shutting a gateway never shuts sessions.
9. **Worker/print scope:** finish child-worker/CLI convergence in this project
   or ship TUI worker attachment first with legacy child read-only fallback?
   Full "one owner per session, every frontend equal" requires the former.
   Should idle child workers keep extensions/MCP resident between tasks (semantic
   change from today's fresh RunPrint turns) or reset them at task boundaries?
10. **Upgrade/crash contract:** frontend updates reconnect without cancellation;
    worker updates drain old version. How long to support mixed frontend/worker
    revisions? On worker crash should uncertain tools require explicit recovery?
    Recommend yes: surviving a TUI exit is not exactly-once execution across a
    server crash. Decide whether accepted queues/recoveries must be crash-durable;
    that adds journal format/recovery work beyond transport replay.
11. **Credentials/diagnostics:** local privileged login/logout only initially,
    or remote authentication UI as a separately scoped service? Last-request and
    profile downloads can contain sensitive context; decide access policy instead
    of exposing them on every bearer-linked session client.

## Source map

Primary ownership/input: `app/app.go`, `queue.go`, `goal.go`, `inbox.go`,
`usershell.go`, `commands.go`, `remote.go`, `remoteprompt.go`, `background_exit.go`.
Session/context/UI: `app/resume.go`, `navigate.go`, `branchsummary.go`, `context.go`,
`loaded.go`, `items.go`, `extensions.go`, `blockdisplay.go`, `exttext.go`, `mcp.go`,
`images.go`, `statusline.go`, `activity.go`, `agents.go`, `tree.go`, `pane*.go`.
Shared execution: `core/core.go`, `goal.go`, `reload.go`, `extensions.go`, `mcp.go`,
`loaded.go`, `core/transcript/{builder,transcript,display}.go`;
`agent/{agent,branch,usershell,bash}.go`.
Protocol: `server/{protocol,server,live,transport,items,rollback,background,extui,
images,cmd}.go`. Lifecycle/control: `daemon/{proto,server_unix,client_unix,stream}.go`,
`session/{session,lock,lock_unix,lock_windows,handoff,handoff_unix,handoff_windows,
tree}.go`, `cli/{print,bgrun,agent,agent_external,agent_worktree,daemon,jobs,goal,
context,mcp}.go`, `cmd/atto/main.go`. Extension/notification/tree semantics:
`extensions/{host,extensions,api_ui,api_events,runtime,uistate}.go`,
`hooks/hooks.go`, `goal/goal.go`, `agentstate/{state,tree,turnlock,shutdown,slots*}.go`.

## 7. Adopted decisions

The split follows the archived implementation's decisions: detach does not stop
execution; goals, jobs and timers continue without clients; prompts are server
objects, the first answer wins and unattended prompts are not auto-answered;
clients are equal; daemon PTY panes and `atto attach` remain; the protocol is
versioned and negotiated; Windows retains an in-process runtime. The TUI keeps
its present execution path during phase 1 and becomes a runtime client in phase 2.

## 8. Implementation status on main

### Phase 1 A — protocol contract

`initialize` negotiates the newest supported revision from `protocolVersions`
(revisions 1–2); omitting the list preserves existing clients. It returns a
per-run `serverInstanceId` alongside the existing name, version, event cursor and
settings. Standalone and Live use the same negotiation. `clientInfo` accepts the
Codex name/title/version shape and `capabilities` accepts the same extensible
object shape. `initialized` is explicitly recognized, but optional: existing
HTTP and stdio clients do not gain a handshake gate. This is not a Codex-dialect
adapter and it does not claim Codex's response DTOs or execution safety policies.

JSON-RPC errors retain their existing codes/messages and add `data.reason`.
Negotiation failures use `unsupportedProtocol`; busy starts and committed steers
have specific reasons; legacy error paths receive a code-derived reason until
they move into the runtime. `provider/providertest` supplies a scripted streaming
model with gates, slow replies and saved request bodies for protocol tests.

### Phase 1 B — shared event hub and client

Every transport now observes one server-owned hub. JSON-lines clients use
`ServeConn`, receive a connection `clientId` from initialize, and detach on EOF
without cancelling execution. HTTP/SSE no longer replace the server's notification
sink. Notifications carry `eventId`; snapshots carry the corresponding cursor.
The Go `Client`/`Connect` speak the same stream protocol, and `ThreadView` combines
snapshots and events, ignoring duplicate events as well as snapshot-covered ones.
Connection subscription is established before requests can start emitting events.

Items preserve transcript entry IDs, command timeouts/results/cancellation/errors,
clipping, full result text, user images, shell fields and compaction reason/cap.
`TranscriptItem` maps them back for a future TUI client. Late saved entry/block IDs
are published as `item/updated`, before extension display events reference them.
Tests compare simultaneous in-process, stdio and mid-stream clients with fresh
snapshots; they cover lossless rendering, duplicate reduction and unattended work
surviving transport EOF.

### Phase 1 C/D — the session runtime

Each loaded thread now has one lane (`server/runtime.go`): requests, agent events,
inbox ticks, retry/retirement callbacks and extension host calls enter it in order.
The lane owns the agent, session writer/lease, hooks, extensions, MCP, transcript,
goal driver, pending input, shell results, prompts and attachment/gate state.
`core.TurnRunner[*pendingInput]` owns busy/cancel causes, steering, queue, send-now,
settlement and inbox delivery; input IDs and client provenance remain runtime data.
The port keeps today's `GoalDriver`, split agent package, request logging,
`step_end`, connection handling and session summary cache. Resume uses
`core.OpenDisplay`, restores only `session.Context`, reads session-wide snapshots
and uses the lightweight accumulated usage instead of decoding abandoned branches.
Project trust warnings remain enabled and execution still requires existing trust.

Added protocol surface (documented in `server/protocol.go`):

- `input/submit` with auto/queue/replace/steer intents; ID-based `turn/unsteer`,
  `queue/resume`, and `input/recovered` addressed to the sending client.
- `turn/interrupt` with sendPending (Esc, default) or cancel (Ctrl+C); actual turns
  use `agent.ErrUserInterrupt`, while compaction/navigation/close use ordinary
  cancellation. Hosted commands detach on a user interrupt, preserving quiet exits.
- `shell/start`, `shell/interrupt`, and shell-aware `turn/background`; results that
  finish during a run wait for its boundary and publish `contextPending` changes.
  Model completion leaves independent shells open, preserving their later output;
  snapshots preserve item start order even when concurrent commands finish late.
- `goal/read|set|edit|pause|resume|clear`, goal retries, server-owned confirmation
  prompts, first-answer-wins `prompt/answer`, and `client/gate` for local pickers.
- `commands/list|run`, `thread/setContextMode|setName|setLabel`, `thread/tree`,
  `thread/navigate|fork`, `thread/context|reload|debugRequest`, and legacy handoff.
- `thread/attach|detach|close`, `job/stopAll`, `timer/list|create|cancel`, activity,
  status, branch-change and closed notifications.

Retirement is configurable with `Retire` and `Retention`: in-process retention is
zero; `DefaultSessionRetention` is one minute for future workers. The standalone
servers retain their existing process lifetime (retirement off until configured).
Busy runs, shell commands, queued input, jobs, timers, active unheld goals and open
prompts prevent retirement. Timers recheck this condition on the lane, so a stale
retirement callback cannot close a newly attached client. Explicit close waits for
runs and user shells before stopping jobs and releasing the writer lease.

Behavior changes and deliberate compatibility choices:

- Standalone servers now advertise and execute goals. Display-only runtime notices
  are included in snapshots and notifications, as in the terminal.
- Revision-1 methods/shapes remain; the default interrupt now has Esc semantics,
  while clients wanting Ctrl+C explicitly select `mode: "cancel"`.
- Connection EOF detaches; CLI process shutdown still explicitly closes its runtime.
  Stdio cancellation now closes its pipes so a blocked scanner can stop.
- Queued prompts wait rather than receiving unattended defaults. Explicit close or
  extension reload cancels disposed questions; this is not an approval decision.
- Quiet interrupt exits cannot wake an idle thread. They are polled when the next
  turn starts as well as by ticks, so a fast turn cannot miss them indefinitely.
- HTTP/SSE keeps legacy bearer clients working without requiring them to echo a
  client ID: they share an anonymous handler identity, and connected SSE streams
  count as interactive. JSON-lines clients have independent connection identities.
- Front-end-only slash commands remain marked local. Pickers/rendering/login and
  other local presentation stay with the front end; execution is exposed via RPC.
- Legacy background-owner resume is still refused, not silently converted into a
  read-only resume. The `agent/*` methods and frozen-web `subagent/*` aliases remain.

Verification includes scripted-provider tests for start/steer/queue/takeback,
Ctrl+Enter, both interrupt modes and per-client recovery; failed-turn queue pause;
picker gates; goal execution, pause and retry interruption; first-answer prompts,
unattended/queued prompts, real extension approvals and `step_end`; deferred and
excluded shells, shells outliving turns and snapshot order; live output clipping;
tree/fork/labels/branch summaries; commands/context/reload/debug;
timers; detach/retirement and shutdown. `TestServerCLIEndToEnd` builds the real
binary and exercises both `atto app-server` and `atto serve`; on Unix it also
verifies an actual hosted command survives a protocol user interrupt and its quiet
exit does not start another model turn. Existing trust, locking, replay, extension,
usage, HTTP/SSE and current cancellation regression tests remain in place.

### What phase 2 needs

The TUI still executes through App; `server.Live` and `/remote` remain functional.
Phase 2 must replace App execution with `Client`/`ThreadView`, translate editor
intents and recovered drafts (including image labels), attach/detach/gate pickers,
render protocol prompts/activity/notices/items, refresh snapshots on branch/reset,
and use `thread/tree` rather than reading the runtime's writer. Keep local UI/login
commands and PTY pane behavior. Test especially reconnect, snapshot/event ordering,
late block IDs, input recovery, prompt races, trust gates and shell interruption.
Daemon workers, registry routing, print-on-worker execution, durable accepted-input
journaling/request dedupe and a full Codex-dialect adapter remain later phases.
The web client is unchanged.

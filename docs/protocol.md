# atto native protocol: a client author's reference

Protocol revision **3** is the only revision served: `initialize` must list 3 in
`protocolVersions` (see the handshake below). This is atto's native
JSON-RPC API, inspired by Codex app-server, **not a Codex wire adapter**. See
[the detailed Codex v2 comparison](codex-app-server-compat.md) and
[implementation status](tui-as-client.md#8-implementation-status-on-main).
The Go DTOs and specification are in `server/protocol.go`; `server.Client` and
`server.ThreadView` are a reference client and snapshot/event reducer.

## Transports and security

```sh
atto app-server                              # default: JSON lines on stdio
atto app-server --listen stdio://             # explicit default
atto app-server --listen unix:///tmp/atto.sock
atto app-server --listen ws://127.0.0.1:7878
atto app-server --listen ws://0.0.0.0:7878 --allow-origin https://client.example
```

- **stdio:** start a subprocess; write one JSON message per line to stdin and
  read replies and notifications from stdout. Diagnostics go to stderr.
- **Unix socket:** connect to the absolute path and speak JSON lines. Socket
  mode is 0600; it is removed on normal exit. An existing path is never unlinked
  at startup: remove a stale socket yourself after checking its owner is gone.
  This is **raw JSON lines**, not Codex's WebSocket-over-UDS. Unix listeners are
  unavailable on Windows versions without Unix-socket support.
- **WebSocket:** HTTP upgrade at any path (use `/`). One JSON-RPC message per
  text message, no trailing newline required. RFC 6455 masking, fragmentation, ping/pong and close are supported;
  binary messages are refused. Max message size is 64 MiB (aggregate fragments).
  Server frames are unmasked. No compression or subprotocol is negotiated.
  Slow writes have a 10-second deadline. Framing errors close with 1002;
  binary messages with 1003, invalid UTF-8 with 1007, oversize with 1009.

There is no HTTP RPC or SSE endpoint, and no web page: the web UI is being
rebuilt, and `atto serve` and `/remote` only say so. Attach other clients with
`atto app-server`.

app-server WS requires the bearer token when bound beyond loopback, but not when
bound only to loopback. The persistent token is generated in `~/.atto/server-token` (or
`$ATTO_DIR/server-token`, 0600); app-server prints the token and file on stderr
for non-loopback listeners. Supply `Authorization: Bearer <token>` or
`?token=<token>` (browser WebSocket cannot set headers). Do not log
query tokens. Non-loopback listeners print a no-TLS warning: use a trusted
private network such as Tailscale or a TLS reverse proxy. There is no built-in
TLS termination, sandbox or per-command approval policy.

Browser WS Origin is checked: HTTP(S) same-host (including port) and loopback
origins are allowed; other origins need a repeatable exact `--allow-origin`
flag. Missing Origin is allowed for non-browser clients; `null`/`file://` is
refused unless an HTTP(S) origin is used instead. Origin permission is not
a substitute for the bearer token. Tokens are transport auth, not provider keys.

Each stream/socket is an independent equal client with a `clientId`. All
transports use the same dispatcher, event hub and worker routing. There is no
hidden TUI owner.

## Envelope and handshake

Send `{id, method, params?}`; `jsonrpc: "2.0"` is accepted but not required
(Codex-style envelopes work). Replies include `{jsonrpc, id, result}` or
`{jsonrpc, id, error}`. Notifications omit `id`. Match replies by ID, not arrival
order: notifications may appear before a response. Use distinct integer or
string IDs. Requests on a connection are handled in order; `ping` is a fence.

```json
{"id":1,"method":"initialize","params":{"protocolVersions":[3],"clientInfo":{"name":"my-client","title":"My client","version":"1"},"capabilities":{"interactive":true,"images":true}}}
```

```json
{"jsonrpc":"2.0","id":1,"result":{"clientId":"c1","eventId":0,"name":"atto","protocolVersion":3,"serverInstanceId":"7e07d7f89fa33b3c","settings":{"toolGroups":true},"version":"test"}}
```

```json
{"method":"initialized"}
```

`protocolVersions` is required and must include 3. A client that offers only
other revisions, or no list, gets `unsupportedProtocol` with a message naming
the revision served (older revisions are no longer accepted).
`clientInfo` uses Codex's name/title/version shape; capabilities currently use
`interactive` (can answer prompts) and `images`. Unknown capability fields,
such as Codex's `experimentalApi`, are tolerated, not a promise to implement
that feature. `settings.toolGroups` is a display preference. Send `initialized`
after the result; it is an acknowledgment, **not a mandatory gate**. Repeated
initialize is also tolerated.

## Thread → turn → item lifecycle

1. `thread/start` creates a session (optionally cwd/model/effort); `thread/resume`
   opens an existing ID or unique ID prefix; `thread/attach` joins a loaded session.
2. Hydrate a `ThreadInfo` snapshot (see below). Follow notifications immediately;
   retain those newer than the snapshot cursor.
3. `turn/start` starts an idle turn. `turn/steer` adds text at a model step
   boundary, not instantly. `input/submit` gives Enter/queue/replace semantics.
4. Render `item/started`, append `item/delta`, replace on `item/updated` and
   `item/completed`. A user item can complete before `turn/started`; notices
   and user shells may have an empty turn ID. Don't assume all items are text.
5. `turn/completed` reports completed/failed/interrupted and usage. It is a turn
   boundary, not a promise of permanent idleness: queued input, goals or inbox
   events can immediately start another turn.

The following messages are from `go test ./server -run TestProtocolExampleTrace
-v`, using `providertest` ("Hello" in two chunks). IDs, times and usage here are
from that run, not constants. Loaded notices and other status events are omitted;
therefore the event IDs need not be consecutive. The Go client decodes away
the redundant `jsonrpc` field on notifications.

```json
{"id":3,"method":"turn/start","params":{"threadId":"ff2a29c3","input":"Say hello"}}
```

```json
{"id":3,"jsonrpc":"2.0","result":{"inputId":"ff2a29c3-in1","turnId":"ff2a29c3-t1"}}
```

```json
{"method":"item/started","params":{"item":{"id":"ff2a29c3-i1","type":"userMessage","text":"Say hello","status":"completed","clientId":"c1","inputId":"ff2a29c3-in1"},"threadId":"ff2a29c3","turnId":""},"eventId":2}
```

```json
{"method":"turn/started","params":{"activity":"Thinking","runKind":"turn","startedAt":1791498055048,"threadId":"ff2a29c3","turnId":"ff2a29c3-t1"},"eventId":4}
```

```json
{"method":"item/started","params":{"item":{"id":"ff2a29c3-i2","type":"agentMessage","status":"inProgress"},"threadId":"ff2a29c3","turnId":"ff2a29c3-t1"},"eventId":5}
```

```json
{"method":"item/delta","params":{"delta":"He","itemId":"ff2a29c3-i2","threadId":"ff2a29c3","turnId":"ff2a29c3-t1"},"eventId":6}
```

```json
{"method":"item/delta","params":{"delta":"llo","itemId":"ff2a29c3-i2","threadId":"ff2a29c3","turnId":"ff2a29c3-t1"},"eventId":7}
```

```json
{"method":"item/completed","params":{"item":{"id":"ff2a29c3-i2","type":"agentMessage","text":"Hello","status":"completed","blockId":"ff2a29c3.f1fd4575f8eefa05:text","entryId":"f1fd4575f8eefa05"},"threadId":"ff2a29c3","turnId":"ff2a29c3-t1"},"eventId":8}
```

```json
{"method":"turn/completed","params":{"contextTokens":3,"durationMs":11,"runKind":"turn","status":"completed","threadId":"ff2a29c3","turnId":"ff2a29c3-t1","usage":{"cachedInputTokens":0,"inputTokens":0,"outputTokens":0}},"eventId":10}
```

### Snapshot and data types

`ThreadInfo` is flat (no `{thread: ...}` wrapper): required `threadId`, `cwd`,
`model` (`provider/id`), `effort`, `contextTokens`, `busy`; optional `name`,
`efforts`, `contextWindow`, `modelName`, `autoCompactLimit`, `autoCompactCap`,
`priced`, `subscription`, `turnId`, `items`, `usage`, `turn`, `pending`, `context`,
`eventId`, `serverInstanceId`, `prompt`, `goal`, `extensionUi`, `runKind`,
`activity`, `jobs`, `timers`, `readOnly`, `offline`, `sessionPath`, `longContext`,
`hasMore`, `before`. Snapshot replies always include `items`,
`hasMore` and `before`, even for an empty tail. `context` is the loaded AGENTS/skills/hooks/extensions/MCP/config report.
A read-only/offline snapshot is not an execution owner. Use resume before writes.

- **Item:** `id`, `type`, optional `status` (inProgress/completed/failed), `text`.
  Types: userMessage, reasoning, agentMessage, commandExecution, compaction,
  branchSummary, event, goal, hook, notice, goalStatus, extText.
  Common provenance: `entryId`, `callId`, `clientId`, `inputId`, `steerGroup`.
  Command: `description`, `command`, `output`, `exitCode`, `durationMs`,
  `timeoutMs`, `timedOut`, `pending`, `startedMs`, `job`, `background`, `images`,
  `dropped`, `canceled`, `error`, `resultText`, `reason`, `cap`.
  User shell: `shell`, `excluded`, `truncated`, `fullOutput`, `contextPending`.
  Compaction: `auto`, `tokensBefore`, `tokensAfter`. Hook: `hookEvent`, `blocked`.
  Display: `blockId`, `display:{statuses:[{ext,text}],ext?,text?}`. extText:
  `title`, `ext`, `lang`, `preview`. Notice: `level`, `title`, `loaded`,
  `reloaded`, `changes`, `note`. Goal: `goalStatus`, `goalState`.
  Omitted fields aren't default display text. Display replacements never change
  the model's original text. Completed items may later get block/entry IDs. User messages receive their
  persisted `entryId` via `item/updated` after recording (also on replay), so
  message context menus can pass it directly to `thread/fork`/`thread/navigate`.
- **Usage:** `inputTokens`, `cachedInputTokens`, `outputTokens`, optional
  `cacheWriteTokens`, `cost`, `last`, `lastCost`, `lastInputTokens`,
  `lastCachedInputTokens`. Input totals include cache reads/writes; cost is USD.
- **TurnInfo:** `startedAt`, optional `verb`, `inputTokens`, `outputTokens`.
  **Activity:** `phase`, `runKind`, `startedAt`, `toolsRunning`.
- **PendingInput:** `steers` (strings), `queued?` (strings),
  `items?:[{id,kind:steer|queued,text,clientId?,images?}]`, `paused?`.
- **ImageInput:** `{mimeType,data}` (base64 or data URL); PNG/JPEG/GIF/WebP,
  ≤10 images, each ≤10 MB, and a model accepting images. `input/submit` also
  accepts `{file,mimeType,width,height,name?}` for files already in the local
  image store. Output **ItemImage**: `{name?,width?,height?,file?,mimeType?}`.
- **Prompt:** `{id,kind:select|multiSelect|input,title,selected,options?:[{label,description?}],
  text?,placeholder?,subtitle?,filterable?,total?,note?,origin?,confirm?,
  clientId?,requestId?}`.
- **GoalInfo:** `{objective,status,statusLabel,indicator,summary,tokens,tokensUsed,
  elapsed,seconds,note?,held?,state?,turnStartedAt?}`; state is the persisted goal.
- **ExtensionUI:** `{status:[{key,text}],widgets:[{key,lines}]}`. Display text,
  never code/HTML to execute.
- **Job:** `{id,label,kind,command,status,started,runtimeMs,exitCode?,error?,
  resultText?,reason?,cap?}`. **Timer:** `{id,due,message,schedule?}`.
- **Agent:** `{name,parentThreadId?,path?,rootThreadId?,depth,origin?,project?,
  lifecycle?,jobOwner?,job?,spawnedBy?,preset,model,effort?,threadId,task,prompt,
  turn,status,durationMs?,error?,inputTokens?,cachedInputTokens?,outputTokens?,
  cost?,created}`. An agent is its session: `threadId` is its identity. The tree
  fields are attributes, additive to the older ones: `parentThreadId` is omitted
  for an agent started from a shell (the root of a tree of its own, `depth` 0,
  `path` `/root`, `origin` `external`, `name` only its label among the open
  agents of its `project`); `lifecycle` is `open`, `closing` or `closed`;
  `jobOwner`/`job` name the job running its latest turn (the parent's job for a
  child, the agent's own for a root). `spawnedBy` is `{session|null,model?,
  effort?,turn?,toolCallId?,origin,cwd?}`: who started it, with the model and
  effort that session used and its turn at the time, `origin` `model`, `outside`
  or `explicit-session`. It is tracking, not proof: the tool call ID comes from
  an environment variable the model could change. Agent read is observational;
  this API does not move `atto agent` execution.
- **CommandInfo:** `{name,args?,description,local?,origin,extension?}`. A local
  command needs a client renderer/picker; don't silently execute a substitute.
- **ContextInfo:** `{loaded,contextTokens,contextWindow?,compactLimit?,cap?,
  priceCap?,longContext?,breakdown?,systemPrompt?,usage,busy}`. `breakdown` is
  unavailable while busy.

**ServerInfo** (MCP): `{name,scope,transport,target,path,status,tools,error?,invalid?,hash?}`.
**SentRequest** (diagnostics): `{Time,URL,Body}`; Time is RFC3339, Body is base64
bytes. thread/list `updatedAt` is also a JSON-encoded RFC3339 time.

All time fields (`startedAt`, `startedMs`, `due`, `created`, retry `at`) are Unix
milliseconds; duration fields are milliseconds. Unknown fields/types should be
handled conservatively and ignored or shown generically for forward compatibility.

## Methods

Every method uses an object for params; optional fields have `?`. Below, **T**
means the required `{threadId}` plus the listed fields. **Snapshot** means
ThreadInfo; **{}** means empty success. Tables are checked against the dispatcher
by `TestProtocolReferenceMethods`; adding a method without documenting it fails.

### Connection, models and workers

| Method | Params | Result / semantics |
| --- | --- | --- |
| `initialize` | `{protocolVersions,clientInfo?,capabilities?}` | `{name,version,protocolVersion,serverInstanceId,clientId?,eventId,settings}`; `protocolVersions` must include 3 |
| `initialized` | `{}` (normally notification) | `{}`; handshake acknowledgment |
| `ping` | `{}` | `{}`; ordering fence on the same connection |
| `models/list` | `{}` | `{models:[{id,name,contextWindow,efforts,hasKey,images}]}` |
| `models/reload` | T | `{}`; reload configured models |
| `worker/state` | T | `{id,session,name,state:idle|working|waiting,cwd,clients,busy,version,pid}`; diagnostics, no attachment added |

### Threads and navigation

| Method | Params | Result / semantics |
| --- | --- | --- |
| `thread/start` | `{cwd?,model?,effort?,deferStart?}` | Snapshot + loaded context; starts/attaches a worker when available |
| `thread/resume` | T + `cwd?,deferStart?,limit?` | Snapshot + context, items; joins owner, ID/prefix resolution |
| `thread/attach` | T + `limit?` | Snapshot + items; follows a loaded thread |
| `thread/read` | T + `offline?,limit?` | Snapshot + items, cursor; offline reads file without loading |
| `thread/items` | `{threadId,before,limit?,offline?}` | Earlier transcript page `{items,hasMore,before}`, oldest first; does not change the live event cursor. |
| `thread/entry` | `{threadId,entryId,offline?}` | Read one complete session entry from disk, including unloaded or off-branch messages (tree copy/edit). |
| `thread/list` | `{cwd?,archived?,includeAgents?,includeClosedAgents?,includeArchived?}` | `{threads:[{threadId,name?,preview?,lastMessage?,cwd?,model?,branch?,updatedAt?,messages?,loaded?,live?,busy?,archived?,external?,openPrompt?,goalWaiting?,agent?,clients?,version?,pid?}]}`; inventory without attaching or starting workers |
| `thread/detach` | T + `reason?` | `{closed,stoppedJobs?,notices?}`; releases client, not work |
| `thread/close` | T + `reason?` | `{closed,stoppedJobs?,notices?}`; explicit session end, stops jobs/turns |
| `thread/setModel` | T + `model,saveDefault?` | Snapshot; provider/model selection |
| `thread/setEffort` | T + `effort,saveDefault?` | Snapshot; one of model's efforts |
| `thread/setContextMode` | T + `contextMode:normal|long` | Snapshot |
| `thread/setName` | T + `name` | Snapshot |
| `thread/setLabel` | T + `entryId,label` | `{}`; label session tree entry |
| `thread/compact` | T | `{turnId}`; idle only, asynchronous compaction |
| `thread/rollback` | T + `numTurns?` | Snapshot + `{input}`; idle only, default 1 user message |
| `thread/tree` | T + `offline?,query?` | `{entries,leaf}`; all saved branches, entry IDs and labels. Rows contain bounded display previews; use thread/entry for full text. A nonempty query searches full text on disk and returns `{matches:[entryId]}`. |
| `thread/navigate` | T + `entryId,summary?:{mode:none|auto|custom,instructions?}` | `{}`; branch movement, optional async summary; follow branchChanged |
| `thread/fork` | T + `entryId` | `{threadId,path,input,images}`; new saved branch (even before the first message); resume using threadId, no access to the server filesystem required |
| `thread/files` | T + `query?,limit?` | `{files:[{path,directory}],truncated}`; workspace-relative paths, case-insensitive substring filter, default 100/max 1000 matches; gitignore-aware walk capped at 50,000 entries/20 levels; cancellation supported |
| `item/image` | T + `itemId,index,preview?,offline?` | `{mimeType,data}`; preview returns a ≤600×350 PNG for clients that cannot decode every format; otherwise base64 stored image selected by zero-based image index of a transcript item, max 10 MiB; never accepts a client filesystem path |
| `item/output` | T + `itemId,offline?` | `{output,truncated}`; stored full user-shell output or available tool output (saved output is zstd-compressed on disk; the cap is on the text after decompression), capped at 10 MiB; truncated is true if full output is unavailable or exceeds cap |
| `thread/archive` | `{threadId,stop?}` | `{threadId,path}`; close runtime/stop jobs, release writer, compress into archive; busy workers require confirmed `stop:true` |
| `thread/unarchive` | `{threadId}` | `{threadId,path}`; restore archived transcript; does not reopen a closed agent record |
| `thread/delete` | `{threadId,stop?}` | `{threadId,notices?}`; remove transcript copies, jobs, inbox/timers, goal, outputs and unshared images; busy workers require confirmed `stop:true` |
| `thread/statusLine` | T | `{configured,lines,refreshInterval?,truncated?}`; run configured server statusLine with snapshot input, off execution lane; 2s timeout/16 KiB output cap; no client command accepted |
| `thread/debug` | T | `{heap,goroutines,memory}`; runtime heap profile (base64), goroutine dump and Go MemStats; diagnostic data can contain private process information; client saves files locally |
| `thread/context` | T + `view?:system` | ContextInfo; system includes systemPrompt |
| `thread/reload` | T | `{}`; reload at safe boundary; follow thread/reloaded |
| `thread/sessionStart` | T | `{}`; release deferStart after client project-trust decision |
| `thread/debugRequest` | T | `{request}` or `{}`; last provider request body |
| `thread/debugRequests` | T | `{sets:{recent:[SentRequest],...pinned}}`; recent/pinned provider request bodies |
| `thread/handoff` | T | `{}`; legacy background continuation; worker clients normally detach instead |

### Input, turns, queue and user shell

| Method | Params | Result / semantics |
| --- | --- | --- |
| `turn/start` | T + `input,images?` | `{turnId,inputId}`; explicit idle start, busy refused |
| `turn/steer` | T + `input` | `{inputId}`; active model turn only; boundary delivery |
| `turn/interrupt` | T + `mode?:cancel|sendPending` | `{interrupted}`; default sendPending (Esc), cancel returns steers (Ctrl+C) |
| `turn/background` | T | `{accepted:true}`; running hosted command becomes a job |
| `turn/unsteer` | T + `inputId?` | `{inputId,text,images,clientId}`; most recent if no ID; committed input refused |
| `input/submit` | T + `input,images?,intent?:auto|queue|replace|steer` | `{inputId?,status:started|steered|queued|done,turnId?}`; typed input, slash commands and !shell; empty resumes queue/held goal |
| `queue/resume` | T | `{}`; unpause and run queued input |
| `shell/start` | T + `command,exclude?` | `{}`; user shell (!, or !! excluded from model) |
| `shell/interrupt` | T | `{interrupted}`; stop user shell |

User-interrupt preserves hosted running commands as quiet background jobs when
supported, rather than destroying them. Interrupt is not transport detach. A
failed turn pauses queued work; `input/recovered` returns input to its originating
client, not every client's editor.

### Prompts, commands, goals and client UI

| Method | Params | Result / semantics |
| --- | --- | --- |
| `prompt/answer` | T + `id,index?` or `indexes?` or `text?` or `cancel?` | `{}`; first valid answer wins |
| `prompt/clientOpen` | T + `prompt` | Prompt; mirror a local client picker, requestId required |
| `prompt/clientClose` | T + `id` | `{}`; id is owner's requestId, withdraw without answering |
| `client/gate` | T + `open` | `{}`; balanced gate for local picker, automatic work waits; detach releases gates |
| `commands/list` | T | `{commands:[CommandInfo]}`; builtin, extensions, skills |
| `commands/run` | T + `name,args?` | `{inputId?,status,turnId?}` (input/submit result); local-only commands aren't server renderer actions |
| `auth/list` | T | `{providers:[{id,name,oauth,status}],stored:[providerId],login?:{provider,status,url?,note?}}`; current pending login survives client reconnect; authentication status only, never credentials |
| `auth/login` | T + `provider,oauth?,apiKey?` | `{status:pending|completed}`; API key stored on the server, or asynchronous browser OAuth; follow auth/updated and answer redirect prompt; idle only |
| `auth/logout` | T + `provider` | `{removed}`; remove stored credentials, reload this thread's models; environment/models.json keys are unchanged |
| `auth/cancel` | T | `{}`; cancel the runtime's pending OAuth flow; detach does not cancel it |
| `goal/read` | T | `{goal:GoalInfo|null}` |
| `goal/set` | T + `input` | `{goal:GoalInfo|null}`; objective, may ask confirmation |
| `goal/edit` | T + `input` | `{goal:GoalInfo|null}`; change objective |
| `goal/pause` | T | `{goal:GoalInfo|null}`; pause automatic work |
| `goal/resume` | T | `{goal:GoalInfo|null}`; resume saved objective |
| `goal/clear` | T | `{}`; clear objective |

### Jobs, timers and agents (observational agent API)

| Method | Params | Result / semantics |
| --- | --- | --- |
| `job/list` | T | `{jobs:[Job]}` |
| `job/output` | T + `job,lines?` | `{output}`; default 200 lines, at most 2000 |
| `job/stop` | T + `job` | `{job:Job}` |
| `job/stopAll` | T | `{stopped}` |
| `timer/list` | T | `{timers:[Timer]}` |
| `timer/create` | T + `when,message` | `{timer:Timer}`; e.g. when "10m", "15:30" |
| `timer/cancel` | T + `id` | `{}` |
| `agent/list` | T | `{agents:[Agent]}`; direct children |
| `agent/tree` | T | `{rootThreadId,agents:[Agent]}`; observational tree (up to 1,000 sessions), including descendants and, when the root is itself an agent started from a shell, the root; Agent adds parentThreadId and absolute `/root/…` path |
| `agent/read` | T + `name` (name/path, `..`, or `@<session id>`) or `agentId` (a session ID or unique prefix) | `{agent:Agent,message,items:[Item]}`; read-only transcript/report |
| `agent/turn` | T + `turn` | `{turn,status:"accepted"}`; local, for `atto agent` only: run turn N that the agent's record names (task, spawn, successor) in this worker, which holds the agent's session. Refused (`unsupportedCapability`) by a thread that is not an agent session of a daemon worker. It is idempotent; it attaches nothing. The worker records the turn (`~/.atto/agent-state`), stops it on an interrupt request, and tells the parent when it ends |
| `mcp/list` | T | `{servers:[ServerInfo]}`; configured MCP servers/status/tool counts |

For `agent/read`, a `name` beginning with `@`
addresses the agent's **own** session (`Agent.threadId`), not its parent's.
Accepts a full ID or a unique prefix of at least 6 characters. Resolution is
limited to the tree rooted at `threadId`'s root: another tree's agent is
indistinguishable from an unknown ID. Ambiguous prefixes return an invalid-params
error listing matching candidates in that tree; too-short prefixes are rejected,
and not-found errors suggest `atto agent list`. Closed/removed agents return a
clear closed error; their archived transcripts remain readable through the
existing session/thread transcript APIs. `agent/list` and `agent/tree` already return
`Agent.threadId`, usable as `@<threadId>`, and have no address parameter. The CLI's
outside-caller, cross-tree `@ID` scope and `list -all` do not apply to these
thread-scoped protocol methods.

An agent's session runs in a worker like any other: a client can `thread/attach`
to it while a turn runs and sees the live items, and the worker is its writer. The
worker of an agent session differs from others in four ways: an idle agent is not
woken by inbox events (they wait for its next turn, as they did when an idle agent
had no process); it is not retired while a turn runs, waits or is being recorded;
a turn ends with its status, usage and answer recorded and pushed to the parent
(an agent started from a shell has none), and the jobs of the turn stopped except
those a user interrupt detached; and an idle one retires after the usual
retention. Turns appear in `job/list` of the parent (of the agent itself for a root)
as jobs of kind `agent`, labelled `agent NAME`.

Live session listings include started/forked runtimes before their first message
is saved, with `loaded` and `busy` state. Cwd filtering and archived listings do
not pull in unrelated live sessions.

## Notifications (server → client)

All normal notifications have `{jsonrpc:"2.0",method,params,eventId}`. **T** in
this table means `params.threadId` plus fields shown. Filter by threadId;
connection hubs may expose events of other threads. Recovery/answers also need
clientId filtering. `events/reset` may be global or worker-scoped.

### Execution and transcript

| Method | Params / meaning |
| --- | --- |
| `turn/started` | T + `{turnId,startedAt,runKind,activity,verb?}`; activity here is phase text |
| `turn/activity` | T + `{turnId,activity:Activity}` |
| `turn/pending` | T + `{pending:PendingInput|null}` |
| `turn/completed` | T + `{turnId,status,error?,usage,contextTokens,runKind,durationMs}` |
| `item/started` | T + `{turnId,item:Item}` |
| `item/delta` | T + `{turnId,itemId,delta}`; command output or message/reasoning text |
| `item/updated` | T + `{turnId?,item:Item}`; replace whole item, including late block/entry IDs |
| `item/completed` | T + `{turnId,item:Item}` |
| `item/display` | T + `{itemId,blockId,display}`; display-only overlay |
| `input/recovered` | T + `{clientId,text,images,ifEmpty}`; draft recovery for the named client |
| `hook` | T + `{turnId?,event,message,blocked?}`; hook result |
| `event` | T + `{title,source}`; inbox/job/timer notice |

### Thread state, reconnect and extension UI

| Method | Params / meaning |
| --- | --- |
| `thread/updated` | T + `{thread:ThreadInfo}`; changed model/name/busy/settings |
| `thread/usage` | T + `{usage:Usage,step:Usage,contextTokens}` |
| `thread/status` | T + `{jobs,timers}` |
| `thread/branchChanged` | T; reread snapshot after branch movement |
| `thread/reloaded` | T + `{context,changes,promptChanged,note}` or `{error}` |
| `thread/closed` | T + `{reason,handoff}`; explicit close/idle retirement |
| `thread/handedOff` | T + `{line?}` or `{finished:true}` |
| `thread/handoffFailed` | T + `{error}` |
| `events/reset` | `{eventId,serverInstanceId,threadId?}`; replace snapshot, don't append replay twice |
| `commands/changed` | T; refresh commands/list |
| `extension/notify` | T + `{extension,message,level}` |
| `extension/ui` | T + `{ui:ExtensionUI}` |

### Goals and questions

| Method | Params / meaning |
| --- | --- |
| `auth/updated` | T + `{provider,status:pending|completed|failed|cancelled,url?,note?,error?}`; URL opens in the **client** browser, never the server; credentials are never included |
| `goal/updated` | T + `{goal:GoalInfo|null}` |
| `goal/retry` | T + `{at}`; transient retry deadline |
| `prompt/open` | T + `{prompt:Prompt}`; server-owned question |
| `prompt/closed` | T + `{id,how,by}`; answered/canceled/withdrawn; by is answering client ID |
| `prompt/clientAnswered` | T + `{clientId,requestId,answer:{index?|indexes?|text?|cancel?}}`; owner applies local picker answer |

## Server-to-client questions and approvals

Questions are server objects, represented by `prompt/open` notifications, **not
Codex-style JSON-RPC requests with a server-chosen envelope ID**. Reply by calling
`prompt/answer` with the prompt's ID. Every attached client sees the same question;
the first valid answer wins. A late answer gets `stalePrompt`. An interactive
client should display questions explicitly and never auto-approve by default.

```json
{"id":8,"method":"prompt/answer","params":{"threadId":"ff2a29c3","id":"prompt-1","index":0}}
```

Select uses a zero-based index; multiSelect uses an array of distinct zero-based
indexes (an empty array is valid); input uses text; cancellation uses `cancel:true`.
Prompts cover extension dialogs, MCP approvals and goal confirmations. No attached
clients means execution questions wait, not auto-answer. A fresh thread/read or
resume snapshot includes the current prompt. Owner-local pickers are mirrored
with clientOpen/clientClose; owner detach withdraws those without supplying an
answer. It does not withdraw execution prompts. Gate counts are client-scoped
and released on detach. Startup project trust remains a client's decision via
`deferStart`/sessionStart or prior CLI trust approval; don't mistake a capability
flag for blanket approval of executable repository content.

## Errors

```json
{"jsonrpc":"2.0","id":9,"error":{"code":-32000,"message":"a turn is already running; use turn/steer or turn/interrupt","data":{"reason":"busy"}}}
```

`error.data.reason` is stable; messages are for people. `retryable?` and
`currentRevision?` may provide more detail. Codes: parse -32700, invalid request
-32600, method not found -32601, invalid params -32602, server error -32000.
Reasons: `parseError`, `invalidRequest`, `invalidParams`, `methodNotFound`,
`internalError`, `busy`, `noModel`, `readOnly`, `stalePrompt`, `alreadyCommitted`,
`revisionConflict`, `ownedByLegacyWriter`, `unsupportedCapability`,
`unsupportedProtocol`, `notFound`. Don't automatically resend accepted inputs
after reconnect: a lost reply doesn't prove the server didn't accept the input.

## Cursors, replay and snapshots

Every hub notification carries an increasing `eventId`; IDs need not be
consecutive for any one thread. `serverInstanceId` changes when the hub restarts.
A snapshot's `{serverInstanceId,eventId}` is its cursor: replace local state and
apply only later notifications. Subscribe before taking the snapshot, buffer
concurrent events, then discard those at or below the cursor. This prevents
missed or doubled text during attach. Never reuse an old cursor after instance
change. `server.ThreadView` implements this rule for Go clients.

There is no cursor replay: socket, stdio and WS connections get events from
connection time, so on reconnect read or resume a fresh snapshot. A lagging
subscriber gets `events/reset` and continues from the newest event. Treat a
reset as a snapshot boundary. Facades translate worker event cursors into their
own hub sequence, including worker restarts.

## Detach, close and workers

EOF, WebSocket close and `thread/detach` release only that client's attachment
and gates. With daemon workers, turns, goals, jobs, timers and prompts continue.
`thread/close` explicitly ends the session and stops jobs. Unattended idle workers
unload after ~60 seconds; active work, queues, timers, jobs, retries, unheld goals
and unanswered prompts prevent retirement. All frontends join the same worker
and writer lease; `thread/resume` never creates a second execution owner.

`--in-process`, `ATTO_NO_DAEMON=1`, disabled daemon and Windows keep execution
in the app-server process. Disconnecting one client still doesn't stop its
thread, but ending that **server process** closes its runtimes. Closing a worker
facade detaches; stopping the daemon ends its workers. A worker crash restores
saved session state, not unsaved accepted inputs, in-flight requests, prompts or
extension promises. Durable input journaling/deduplication is not implemented.

## Differences from Codex app-server v2

Shared concepts and method names do not imply compatible DTOs. atto snapshots
are flat, text turn input is a string, item/delta is generic, usage/settings/goals
have native shapes, and initialization requires revision 3, though no handshake gate
stops other requests. Prompt questions use notifications plus prompt/answer, not
bidirectional RPC envelopes. Event cursors, input/submit, queue/gate
controls, jobs and extension UI are additional API.
Unix sockets use JSON lines rather than Codex WS-over-UDS. Browser origins and
query tokens are supported intentionally. There is no Codex account/config/
sandbox/approval-policy adapter. See [the full compatibility research](codex-app-server-compat.md)
for details, and [working example clients](../examples/clients/README.md).

### Resource, status and debug methods

`thread/files`, `item/image` and `item/output` are native client resources, not
an unrestricted filesystem API. Reads are resolved from the current thread's
cwd/transcript on its lane, then performed off the execution lane. Set `offline:true` to resolve resources from a saved read-only session without
loading or acquiring a writer. Item output
can only return bytes still retained by the runtime/file; it cannot reconstruct
output that the engine discarded. clients that cannot decode WebP request `item/image` with `preview:true` to render PNG
previews of every native format, including WebP. Omitting preview returns the
original bytes.

`thread/statusLine` uses the same configuration as the TUI. Input includes
`hook_event_name`, `session_id`, `session_name`, `transcript_path`, `cwd`,
`version`, `effort`, `busy`, `model`, `workspace`, `context_window`, `git_branch`
and `cache` with the TUI's snake_case fields. `memory.heap_bytes` describes the
runtime heap, not frontend RSS (the TUI's `memory.rss_bytes` is frontend-local).
ANSI sequences are display text, not commands. Clients debounce on changed
inputs and respect `refreshInterval` for forced refresh. A status command and
its descendants are killed after completion/timeout. This read is opt-in,
so headless clients that do not display status lines do not execute it.

`auth/login` stores provider credentials **on the server**; do not send keys
over an untrusted plain-WS network. Keys must not appear in client settings,
transcripts, notifications or diagnostic logging. OAuth URLs open on the client.
The native protocol exposes select, multiSelect and input prompts; confirms
are single-select Yes/No prompts, not separate bidirectional JSON-RPC requests.

## Daemon control (local CLI discovery)

The daemon's framed Unix-socket control protocol is revision **4**, distinct from
JSON-RPC protocol revisions. Operations are `worker` (find/start), `workers` and
`status` (list), `kill` (close session/work), and `stop` (force required for live
workers). It no longer hosts PTYs or transports terminal screens. Every TUI is
an independent worker client; center navigation only changes that client's
attachment. Only `status`/`stop` downgrade to revisions 2/3 for upgrades; execution
falls back in-process until an incompatible old daemon is stopped. No JSON-RPC
method was removed; worker/state adds name and state diagnostics. Worker retention defaults to one minute;
`ATTO_WORKER_RETENTION` accepts a duration override for process tests/deployments.

## Revision 3: lazy transcript loading

Clients receive only the transcript after the last active
compaction, capped to the latest `limit` items (default 200), on `thread/resume`,
`thread/attach` and `thread/read`. Results carry `hasMore` and an exclusive
`before` cursor. Pass that cursor to `thread/items` to prepend an earlier page;
continue with its returned cursor while `hasMore` is true. Pages cross compaction
boundaries but stay on the current active branch. Discard in-flight pages when
a branch change or replacement snapshot arrives. Items are always oldest-first.

Paging does not replace a snapshot or advance `eventId`: merge by item ID,
preferring an already loaded live item. Events retain their existing IDs and
exactly-once snapshot boundary. There is no full-snapshot mode.

```json
{"jsonrpc":"2.0","id":5,"method":"thread/items","params":{"threadId":"example","before":"example-i201","limit":200}}
```

The TUI requests older pages at the top of loaded content. Prepending preserves
the visible anchor and does not reset the live reducer; the TUI shows a
temporary `loading earlier messages` line. Tree search/copy/fork and saved
output/image access resolve unloaded entries from disk.

An unattached worker retains no completed transcript items, including the tail.
It keeps the active model context and fixed runtime state; session entries and
live events are still recorded/emitted. Attaching reconstructs a tail from disk;
the last detach drops it again. Archived JSONL.zst reads stream without a temporary
decompressed file. Shared opens for print, workers and app-server
use the same context-only reader. A default 32 MiB **soft** Go memory budget
bounds transient allocations (not large model contexts); `GOMEMLIMIT` or an
embedded application's explicit limit takes precedence. Rare large reads,
pages and compactions release unused heap pages with `debug.FreeOSMemory`.
See [the synthetic memory measurements](session-memory.md) for methodology.

### Thread inventory for pickers and command centers

`thread/list` keeps its defaults: active ordinary sessions, including empty
loaded sessions; managed agents stay excluded even when loaded. `archived`
selects archived instead of active sessions; `includeArchived` combines both.
`includeAgents` opts into agent sessions, including records without a transcript;
`includeClosedAgents` additionally admits closed agents in the requested scope.
Recorded ancestors anchor the tree even before their first prompt. `cwd` uses
server OS directory case rules. A listing never starts or attaches a worker.

An agent row's `agent` object contains `parentThreadId` (empty for shell roots),
`rootThreadId`, `depth`, `path`, `name`, `role`, `origin`, `project`, `spawnedBy`
(`session`, `model`, `effort`, `turn`, `toolCallId`, plus provenance `origin`/`cwd`),
`lifecycle` (`open`/`closed`; teardown may transiently show `closing`), `lastTurn`
(turn number, status, queued/started/ended RFC3339 times, prompt/cached/output
tokens, cost, steps/error), `durationMs`, and `worktreeBranch`. `preview` is the
first prompt/task; `lastMessage` is the bounded last assistant answer on the
active branch. `branch` is the saved git branch, overridden by worktree branch.
`openPrompt` and `goalWaiting` are independent needs-you flags: the latter also
includes paused, blocked and usage-limited goals, or an active held goal.
`loaded` identifies a live runtime; `busy` includes a turn or user shell.

Frontends build trees from parent links, derive status/tabs, and refresh on
opening and explicit refresh. There is no overview method or inventory change
notification.

Archive/delete of an open agent closes its subtree deepest-first under the
agent tree lock. Any running/queued descendant turn is refused even with
`stop:true`: interrupt it first. Dirty worktrees are refused; clean worktrees
are removed and branches retained, like agent close. Closed records and IDs
remain after deletion. Descendant transcripts are archived; deletion removes
only the selected transcript. Unarchiving a closed agent restores its transcript,
not its lifecycle. Frontends must confirm deletion and ask "stop it and
archive/delete?" before stopping a live worker. Other processes' read-only
writers are never silently taken over.

## Shared UI catalog v1 (revision 3)

UI is portable data, not executable code. The complete element/site contract is
in [ui.md](ui.md). `initialize.capabilities.ui` may announce
`{version:1,surface:"terminal",width:80,elements:["Box","Text",...]}`.
The surface is terminal, web, gui, flutter or headless; width is logical columns
(0 if unmeasured). Renderers run once per session with surface `shared`.

| Method | Parameters / result |
| --- | --- |
| `ui/capabilities` | Client notification: `{surface,width,elements}`; reannounce on resize. |
| `ui/event` | Request: `{threadId,site,id,key,type,value?,rev}` → `{accepted:true,rev}`. |
| `ui/open` | Server notification: `{threadId,site,id,rev,options,focusClientId?}`. |
| `ui/render` | Server notification: `{threadId,site,id,rev,tree}`; full tree or null. |
| `ui/close` | Server notification: `{threadId,site,id,rev,reason}`. |

Attach/read snapshots include `ui:{version:1,instances:[{site,id,rev,options,tree}]}`
for live panes, band/status slots, dialogs and unexpired toasts. UI shares the
snapshot's existing hub event cursor. Subscribe before reading; discard events
at or below that cursor, then reduce each later event once. Keep close revision
 tombstones so late trees cannot reopen a closed site. On reset/reconnect replace
UI from a snapshot; do not replay uncertain actions.

Routing uses site/id/key and **rev only** (the 2026-10-09 simplification): no
`uiEpoch`, `requestKey` or dedupe cache. Every publication gets a new monotonically
increasing safe integer revision, including acceptance before the callback, so
an old/duplicate press is rejected with `revisionConflict` and
`currentRevision`. Press and close forbid value; input/submit/select require it.
Close uses `$site`; callbacks must be bound and declared, enabled, with validated
input lengths and enabled option membership. Originating client/surface comes
from the transport, never action payload. Read-only sessions cannot act.

`uiBlock` typed items carry `title`, `ext` (owner), `entryId`, `uiId`, `rev` and
`tree`. Session entries `ui_block`, `ui_block_update` (including close tombstones)
and `ui_item_display` contain only display data, excluded from model history.
Pages replay the active branch and apply latest overlays without running any
historical provider code. Saved controls remain unbound until fresh rendering.

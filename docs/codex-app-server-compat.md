# Can Codex app-server clients drive atto?

## Executive conclusion

**Yes, with an explicit compatibility layer; no, not with the current wire protocol, and not by just adding method aliases.** atto already has the right conceptual skeleton (threads, turns, streaming items), but almost every shared operation has a different request, response, or notification shape. Codex is also a bidirectional, connection-scoped protocol with substantially more runtime semantics.

Recommend **a separate Codex-dialect endpoint**, provisionally `atto app-server --codex`, backed by atto's existing server/agent/session services. Preserve the native atto API and web/live clients. First target a pinned, tested core-chat profile, then the open-source Codex TUI remote client, then a particular released IDE client after verifying how to redirect its backend. Do not promise that the unmodified desktop app or every IDE build will work merely because schema validation passes.

Rough one-agent estimates: **4–7 working days for a deliberately limited text-chat prototype; 8–15 days for a credible core-chat adapter with resume, images, cancellation, and tests; 15–30 days total for a pinned TUI/IDE-oriented release**, assuming clients can be redirected and unrestricted execution is explicitly acceptable. A broader local-feature adapter is approximately **40–80 days total**. True safety-policy parity is another substantial project; OpenAI-hosted account, billing, Apps, and cloud/remote-control features cannot be obtained by translating atto messages. These are engineering estimates, not results of an implementation experiment.

The major safety rule: **never acknowledge `workspace-write`, `read-only`, permission profiles, or approval policy as enforced when they are not.** atto currently runs shell commands without Codex's execution sandbox or per-command user-approval broker. Honest initial compatibility is full-access execution only, explicitly selected, or externally enforced isolation—not a fictitious safe mode.

## 1. Scope, sources, and reproducibility

Research date: **2026-10-07**. No atto code changes, no pushes, no model calls, and no reading of `~/.atto/auth.json` or `~/.atto/server-token`. Only this report is added. Source inspection, rather than running clients against models, establishes the findings below.

- atto baseline: `cd4785285b533208fd76a4d7c59e9aaf6c39e23e`.
- Codex source: shallow-cloned with `git clone --depth 1 https://github.com/openai/codex /tmp/atto-codex-protocol-research`.
- Codex snapshot: **`044da98b4a7ff2dd0079f706280e0a6934048222`**, commit date `2026-10-06T23:07:48Z`, “Expose selected environments to MCP contributors (#51503)”. All protocol statements refer to this snapshot, not all released versions.
- Subsequently fetched `--deepen=500`, then `--deepen=2000` for history; the checked-out snapshot did not change.

Primary Codex references (paths relative to that checkout):

1. `codex-rs/app-server-protocol/src/protocol/common.rs`: complete method/notification registry, typed request and response unions, experimental markers, deprecated methods.
2. `.../src/protocol/v1.rs`: **initialize still uses v1 types**, despite the chat lifecycle being v2.
3. `.../src/protocol/v2/{thread,thread_data,turn,item,shared,permissions,notification,model,config,account,mcp,review}.rs`: actual v2 shapes and semantics. The v2 module is now split into files, not one `v2.rs`.
4. `.../src/rpc.rs`, `export.rs`, `experimental_api.rs`, `schema/{typescript,json,precomputed}`: envelopes and checked-in generated contracts. Default generated schemas/TS **filter experimental methods/fields**; source registry is needed for the full inventory. `codex app-server generate-ts` and `generate-json-schema` are CLI exporters; their `--experimental` export option is separate from runtime initialize opt-in.
5. `codex-rs/app-server/README.md`, `src/message_processor.rs`, `src/request_processors/initialize_processor.rs`, `tests/`: lifecycle, gating, and implementation/tests. README at this snapshot is mainly detailed feature notes, not a complete introductory spec. Historical `codex-rs/docs/protocol_v1.md` is not the v2 authority.
6. `codex-rs/app-server-transport/src/transport/{stdio,websocket,unix_socket}.rs`, their tests, and `codex-rs/websocket-auth/src/lib.rs`: transports and authentication (split out of app-server).
7. `codex-rs/app-server-client/{README.md,src/lib.rs,src/remote.rs}`, `codex-rs/tui/src/{lib.rs,app_server_session.rs,app_server_session/,app/background_requests.rs,config_update.rs,collaboration_modes.rs}`, and `codex-rs/exec/src/lib.rs`: real open-source consumers.

Primary atto references: `server/protocol.go` (spec header and structs), `server/{server,transport,items,live,rollback,background}.go`, `app/remote.go`, `core/transcript/`, `session/{session,tree}.go`, `agent/`, `shell/`, `config/config.go`, and hook/MCP/extension integration. There is no `server/http.go` in this baseline; HTTP is in `server/transport.go`.

This report inventories **all 173 explicit Codex client request methods, all 86 registered server notifications, and all 11 server requests** (requests/notifications in the appendices; server requests in §5) at this snapshot (including deprecated/experimental entries). Registry membership does not mean a method is enabled by default or usable on every backend/platform.

## 2. Transport, envelopes, initialization, and versioning

| Area | Codex | atto | Compatibility consequence |
|---|---|---|---|
| JSON envelope | `{id,method,params?}`; `{id,result}` or `{id,error:{code,message,data?}}`; notifications `{method,params?}`. Int/string request IDs; optional request `trace`. `rpc.rs` explicitly says it does **not** send/require `jsonrpc:"2.0"`. Notification envelope may add top-level `emittedAtMs`. | Accepts omitted `jsonrpc`; replies/notifications include `jsonrpc:"2.0"`; arbitrary raw JSON IDs; errors only `code,message`. | The extra `jsonrpc` field is usually tolerated by the inspected serde client, but strict schema clients may reject it. Emit Codex envelopes on the adapter. Add typed error data, response dispatch, ID validation, and optional envelope timestamp. |
| stdio | One JSON message per line, both directions; stdout must stay protocol-only. | Already JSON-lines stdio (`ServeStdio`, 16 MiB scanner limit). | Framing is reusable. Duplex RPC processing, connection state, concurrency, and lifecycle are not yet equivalent. |
| TCP WebSocket | JSON text frames; `/readyz` readiness, upgrade handling, ping/close/backpressure; optional bearer auth, required on non-loopback listeners. | HTTP `POST /rpc` plus resumable SSE `GET /events`; no WebSocket endpoint. | SSE is not a Codex transport. Implement WS, not a URL rewrite. |
| Unix socket | **WebSocket HTTP upgrade over UDS**, not raw JSON lines. Client handshake uses `ws://localhost/rpc`; tests verify ping/pong and size-limit response header. Socket ownership/startup lock/daemon shutdown matter for default-daemon behavior. | No Codex-compatible UDS server. | Add WS-over-UDS if supporting `codex --remote unix://...`; plain net.UnixListener + JSON-lines is insufficient. |
| Transport auth | `--ws-auth capability-token` (token-file or SHA-256 digest) or `signed-bearer-token` (JWT, secret/issuer/audience/skew). Browser Origin rejected. Remote TUI sends `Authorization: Bearer ...`; only permits tokens on WSS or loopback WS. UDS relies on local access controls. | Bearer or query token on HTTP/SSE; persistent server token for `serve`, per-start ephemeral token for `/remote`; supports LAN HTTP with warning. | Separate transport authorization from provider login. Start local-only; require bearer/TLS for remote use, avoid importing query-token behavior into WS. Signed JWT and OpenAI relay pairing need not be phase-one features. |
| initialize | `{clientInfo:{name,title?,version}, capabilities?:{experimentalApi,requestAttestation,optOutNotificationMethods?,extensions?,explicitGatewayOauth?,mcpServerOpenaiFormElicitation?}}` → `{userAgent,codexHome,platformFamily,platformOs}`; then client `initialized` notification. Non-initialize requests are gated until initialized connection session state; duplicate initialize rejected. | Ignores client identity/capabilities; returns `{name,version,protocolVersion:1,eventId,settings}`, plus `{live,threadId}` on live server. No handshake gate. `initialized` currently falls through as unknown notification and has no effect. | Add connection-scoped initialize state, correct required response fields, identity/notification opt-outs, experimental gating. `codexHome` must truthfully identify adapter storage, not imply atto uses Codex config/rollouts. |
| Version selection | v1/v2 Rust modules and generated `v2/` TS paths, **not** an RPC `/v2` prefix or negotiated numeric protocol version. `experimentalApi` gates features, not a feature-by-feature support matrix. `userAgent` exposes version metadata; TUI probes/falls back for specific capabilities. | `ProtocolVersion=1`, no dialect negotiation. | Pin a Codex revision/release and test profile. Do not treat `protocolVersion:2` or blanket experimental opt-in as a solution. |

atto's SSE replay (`eventId`, `events/reset`) is useful native functionality but not Codex's reconnect model. The adapter needs read/resume hydration, per-connection subscriptions, request cancellation/disconnect rules, and ordered events. atto installs one `Notify` callback on a server; it does not already have Codex's client-scoped subscription and pending-server-request machinery.

## 3. Shared lifecycle and method mapping

Classification: **shape differs** means a name matches but the wire contract does not; **renamed** means a conceptual counterpart also needs conversion; **missing** means no native server RPC at that name (an internal feature may exist). There are **no complete shared core operations that are identical end-to-end**. `turn/interrupt` has the same empty success acknowledgment, and item start/completion has the same outer keys, but their inputs/items/semantics still differ.

| Codex method | atto counterpart | Exact differences / work |
|---|---|---|
| `initialize` | same name; shape differs | See handshake above. |
| `model/list` | `models/list`; renamed + shape differs | Codex `{cursor?,limit?,includeHidden?}` → `{data:[Model],nextCursor}` vs atto `{models:[{id,name,contextWindow,efforts,hasKey,images}]}`. Codex Model requires `id,model,displayName,description,hidden,supportedReasoningEfforts:[{reasoningEffort,description}],defaultReasoningEffort,inputModalities,isDefault` and nullable upgrade/access metadata, service tiers, etc. Derive truthful catalog defaults; don't advertise nonexistent models/tiers. At this snapshot ReasoningEffort is a string, not a closed enum; map only efforts atto actually supports. |
| `thread/start` | same name; shape differs | Both have `cwd?,model?`. Codex adds `modelProvider,approvalPolicy,approvalsReviewer,sandbox,config,baseInstructions,developerInstructions,ephemeral,serviceTier`; experimental `historyMode,permissions,dynamicTools,environments,runtimeWorkspaceRoots`, etc. atto has `effort?` directly, unlike stable Codex start (effort comes through config/defaults). Codex response `{thread,model,modelProvider,cwd,instructionSources,approvalPolicy,approvalsReviewer,sandbox,reasoningEffort,serviceTier,disabledPluginIds,...}`; atto returns flat ThreadInfo with `context`. Must enforce or reject options, not silently ignore safety fields. |
| `thread/resume` | same name; shape differs | Codex `threadId` plus configuration overrides, `excludeTurns`, experimental `path/history/initialTurnsPage`. Same effective-settings wrapper as start plus collaboration/history cursors. atto accepts `threadId` only and returns flat `items`/context. Resume hydration and settings semantics need conversion. |
| `thread/read` | same name; shape differs | Codex `{threadId,includeTurns?}` → `{thread}` and can read stored metadata/history without resuming. atto `{threadId}` → flat ThreadInfo + items, normally requires loaded thread (`thread/start`/`thread/resume` first). |
| `thread/list` | same name; shape differs | Codex cursor/limit/sort/provider/source/title/archive/section filters; `cwd` accepts string **or array**; returns `{data:[Thread],nextCursor,backwardsCursor}`. atto only `{cwd?,archived?}` → `{threads:[summary]}`. Need correct filtering/page semantics or explicit unsupported errors. |
| `turn/start` | same name; shape differs | Codex `input:[UserInput]`, `threadId`, optional `clientUserMessageId,cwd,model,effort,summary,approvalPolicy,sandboxPolicy,outputSchema,serviceTier` and experimental collaboration/tools/context/environment settings. atto `input:string`, separate `images:[{mimeType,data}]`, no per-turn settings override; returns `{turnId}` instead of `{turn:Turn}`. Native standalone atto rejects an already busy thread; Codex source supports turn/start adding input to an active turn in applicable cases (see comments). |
| `turn/steer` | same name; shape differs | Codex typed input + required `expectedTurnId` → `{turnId}`. atto string input, no expected-turn precondition → `{}`; live mode may start or queue instead. Adapter must not steer the wrong turn. |
| `turn/interrupt` | same name; partially shared | Codex requires `{threadId,turnId}`; atto only thread ID and cancels whichever turn runs. Validate target turn. Both acknowledge `{}`; completion event is separate. |
| `thread/compact/start` | `thread/compact`; renamed | Codex `{threadId}` → `{}` then lifecycle/item events; atto returns `{turnId}`. Map atto compaction item to `contextCompaction`. |
| `thread/revert` | `thread/rollback`; conceptual only | Current Codex `{threadId,beforeTurnId}` for **paginated** history → `{thread,turnsBackwardsCursor,itemsBackwardsCursor}` and `thread/reverted`; no filesystem undo. atto `{numTurns?:1}` branches before the nth last user message and returns flat thread + editable `input`; old branch remains. Not merely an alias. The atto comment naming “codex's method” refers to older API shape: this checkout has no registered `thread/rollback`. |
| `thread/settings/update`, `turn/settings/update` | `thread/setModel`, `thread/setEffort`; conceptual only | Codex sticky thread settings versus active-turn-only changes, nullable/omitted field semantics, reviewer and service-tier changes. atto has simple explicit model/effort setters (setters take effect on the next model request and persist model/effort entries, without Codex's active-turn-only distinction). Don't replace the running agent to emulate a per-turn update. |
| `thread/backgroundTerminals/list`, `/terminate`, `/clean` | `job/list`, `job/stop`; conceptual only | Codex terminals carry process/call IDs and terminal-specific state. atto jobs have numeric IDs, commands, status, monitors and timers. Not a safe blanket mapping; `job/output` also has no direct Codex terminal-read equivalent. |
| `thread/goal/set`, `/get`, `/clear` | internal atto goal feature, live `goal/updated` | No native goal mutation RPC. Different object/status/budget/origin semantics; do not translate only the status labels. |

### Thread/turn representation is a real state-model gap

- atto ThreadInfo uses `threadId,cwd,name,model,effort,busy,turnId,items,usage,turn,pending,context`; `TurnInfo` is a running-status-line summary (`startedAt` in **milliseconds**, tokens, verb), not a persisted turn containing items.
- Codex Thread uses `id,sessionId,forkedFromId,parentThreadId,preview,ephemeral,historyMode,modelProvider,model,reasoningEffort,createdAt,updatedAt,status,path,cwd,cliVersion,originator,source,name,gitInfo,turns`, plus section/project/agent metadata. Thread timestamps are **seconds**. `status` is tagged `{type:"notLoaded"|"idle"|"systemError"|"active",activeFlags?}` rather than `busy`.
- Codex Turn uses `id,rootTurnId,items,itemsView,status,error,startedAt,completedAt,durationMs`. Turn status literals (`inProgress,completed,interrupted,failed`) conceptually align. atto failure `error` is text; Codex `TurnError` is `{message,codexErrorInfo,additionalDetails,misalignment}`.
- atto session IDs are **8 hex characters** (`session.newID`); its transient turn IDs are `<session>-tN`, sequence scoped to loaded runtime. Codex-generated thread/turn IDs are UUIDv7, and the TUI explicitly parses thread IDs through `ThreadId::from_string` (UUID parsing). **A stable persisted external-ID mapping is necessary**, even if a loose JS third-party client accepts arbitrary strings. Do not directly expose atto IDs.
- atto persisted branch entries can replay flat items, but don't supply the Codex durable turn pagination contract. An adapter must preserve/group turns and IDs across restart, steering, interruption, compaction, and branches. Reconstructing turns merely by splitting every user-role message would misclassify goal/events/hooks/compaction input; use the actual transcript/session classifications. Historical reconstruction may need explicitly limited metadata; newly adapter-created threads should save enough sidecar state.
- Full-history `historyMode:"legacy"` is a sensible initial profile. Latest TUI requests/probes paginated history; its fallback paths exist, but must be tested, not assumed. Supporting `thread/turns/list`, `thread/items/list`, and `thread/revert` requires genuine stable cursors and history boundaries.

### Remaining feature families (all absent as native Codex RPCs)

The complete per-method appendix below distinguishes these from shared lifecycle methods. Important concrete contracts:

- **Configuration:** `config/read {includeLayers?,cwd?}` → `{config,origins,layers}`. Config deliberately uses snake_case keys such as `model_provider,approval_policy,sandbox_mode,model_reasoning_effort`, unlike most camelCase RPC envelopes. `config/value/write {keyPath,value,mergeStrategy,filePath?,expectedVersion?}` and `config/batchWrite {edits,...}` have version/conflict semantics. `configRequirements/read` returns nullable enforced `requirements` and `supportsIndependentSpeedModes`; `permissionProfile/list`, `modelProvider/capabilities/read`, `experimentalFeature/list`/enablement and `collaborationMode/list` cannot simply read atto initialize.settings. A read-only projection is feasible; arbitrary Codex TOML writes are not equivalent to atto settings/models JSON. Reject unsupported writes and managed/safety options.
- **Provider/account authentication:** `account/read {refreshToken?...}` → `{account:null|Account,requiresOpenaiAuth,...}`; `account/login/start` tagged params include `apiKey`, `chatgpt`, `chatgptDeviceCode`, `chatgptAuthTokens {accessToken,chatgptAccountId,chatgptPlanType?}`, and Bedrock variants. Responses and `account/login/completed` depend on flow (`loginId,authUrl`, device code, etc.). Cancellation/logout, gateway OAuth and server-request token refresh are separate operations. atto has provider login code, but **no public RPC mapping**. A truthful externally-configured profile could return `{account:null,requiresOpenaiAuth:false}`; whether an official GUI accepts this requires testing. Do not invent a paid ChatGPT account to bypass its onboarding.
- **Rate limits/account services:** `account/rateLimits/read` → `{ordinaryUsageAllowed,rateLimits,rateLimitsByLimitId,rateLimitResetCredits,accountId,rateLimitUpsell}`. Snapshot includes nullable `primary,secondary,credits,planType` and limit metadata; windows have used percentage/reset time. atto token/cost Usage is **not** an account quota. Return unavailable/null fields as the pinned schema permits, or explicit unsupported error; never fabricate remaining entitlement. Credits/reset, workspace messages, account usage, nudge email and Bedrock setup are backend-specific services.
- **File discovery:** `fuzzyFileSearch {query,roots,cancellationToken?}` → `{files:[{root,path,match_type,file_name,score,indices}]}`. Result retains snake_case `match_type,file_name` in source. Session start `{sessionId,roots}`, update `{sessionId,query}`, stop `{sessionId}` → `{}`; async session notifications carry `sessionId,query,files` or completion. atto's local mention/file picker logic can inform implementation but is not exposed over server RPC. Need cancellation, limits, ignore rules, root authorization.
- **Review:** `review/start {threadId,target,delivery?}` → `{turn,reviewThreadId}`. Targets: `{type:"uncommittedChanges"}`, `{type:"baseBranch",branch}`, `{type:"commit",sha,title?}`, `{type:"custom",instructions}`. Inline versus deprecated detached review; review entry/exit items, real review instructions and findings semantics are missing. A prompt saying “review” is not parity.
- **Skills/hooks/MCP:** atto loads skills, has hooks, and imports approved MCP tools, but does not expose `skills/list`, roots/config writes, `hooks/list`, MCP catalog/status/OAuth/resource/tool/event calls. Could project existing catalogs gradually; MCP application UI/resources/elicitation are additional semantics. Some atto MCP tools become extension/shell commands rather than structured MCP transcript items.
- **Direct execution/filesystem:** `command/exec` + write/resize/terminate and experimental `process/spawn` + stdin/PTY/kill; `fs/*` read/write/copy/remove/watch APIs. atto agent commands/jobs aren't direct arbitrary client exec sessions. Exposing these adds authorization, output ownership, process and watch lifetimes; avoid “helpful” unrestricted file/exec RPCs on a remote listener.
- **Large unsupported systems:** plugins/marketplaces/share, Apps/connectors, projects/sections/search, persisted attachments, memory management, structured dynamic tools, Guardian/auto-review, native user verification/attestation, realtime audio/speech, browser/computer-use settings, remote relay enrollment/pairing and Windows sandbox setup. Names may overlap atto features conceptually; wire compatibility does not implement their runtime or external services.

## 4. Items, inputs, and notifications

### Item mapping

Codex ThreadItem is a **tagged union**; atto Item is a struct with a string `type` and many optional fields. Unknown atto-only variants should not be sent to a closed Codex union. Extra fields on known variants may be tolerated by some clients, but portable compatibility needs the pinned Codex shape.

| Codex item type | atto | Conversion / fidelity |
|---|---|---|
| `userMessage` | same type string, different fields | atto `{id,text,status?}` → Codex `{id,clientId,content:[UserInput]}`. Preserve client message correlation and original typed inputs separately; atto text placeholders don't recover image URLs/mentions. |
| `agentMessage` | closest match | `id,text` align; supply nullable `phase,memoryCitation,delivery,questions` as appropriate. atto `blockId,display,status` are not Codex fields; extension replacement text is display-only and must not change model transcript content. |
| `reasoning` | same tag, different fields | atto `text,durationMs` → Codex `summary:[string],content:[string]`. Choose one honest stream (provider summary vs raw reasoning); don't duplicate or invent hidden chain-of-thought. |
| `commandExecution` | same tag, different fields | atto `command,output,description,status,exitCode,durationMs,pending,job,background,timedOut,images` → Codex `command,cwd,processId,source,status,commandActions,aggregatedOutput,exitCode,durationMs,pluginId,scriptPath`. Supply cwd and unknown parsed action; output is renamed. `declined` is an extra Codex status absent in atto. Pending command-writing updates and background job completion are not Codex PTY state. |
| `contextCompaction` | `compaction` renamed | Codex `{id}`; atto additionally `auto,tokensBefore,tokensAfter,text`. Preserve details in native API/sidecar, not unsupported fields/types. |
| `fileChange` | missing structured item | `{id,changes:[{path,kind,diff}],status}`. atto shell editing is just commandExecution: cannot reliably infer patches from arbitrary shell text. Git before/after diff is possible UI aid, not exact per-tool semantics; structured edit events would require instrumentation. |
| `mcpToolCall` | missing structured item | `server,tool,arguments,status,result,error,durationMs,appContext,mcpAppUi,...`; atto MCP-backed shell commands are not sufficient to reconstruct rich results/progress. |
| `dynamicToolCall`, `functionCallOutput` | missing | Client-owned tools, structured arguments/content and subsequent tool-output input are not atto's single bash tool interface. Requires new agent/tool integration, not just JSON translation. |
| `collabAgentToolCall`, `subAgentActivity` | conceptual subagents exist | Codex sender/receiver thread IDs, tool/status/agent-state maps and activity events versus atto subagent list/read and shell-mediated lifecycle. Need explicit observer/state projection, not regex parsing of shell command text. |
| `plan` | missing | Proposed plan text and plan notifications; atto can write plan prose but no plan item lifecycle. |
| `webSearch`, `imageView`, `imageGeneration`, `sleep` | missing dedicated items | atto can call shell/web tools, `atto view`, jobs/timers; these lack matching typed records. atto command images expose only name/dimensions, not Codex imageView path/resource identity. |
| `enteredReviewMode`, `exitedReviewMode` | missing | Require review runtime. |
| `hookPrompt` | conceptual hook messages only | Codex fragments, versus atto `hook` with `hookEvent,text,blocked`; cannot map as identical. |
| No Codex union equivalent | atto `event,goal,hook,notice,goalStatus,extText,branchSummary` | Keep native; either omit display-only content or deliberately render a labeled agentMessage/warning where semantics fit. Synthetic agentMessage fallback changes the UI meaning and should be documented. Do not inject arbitrary extra tagged variants. |

Codex UserInput supports `text {text,text_elements}`, `image {url|fileId,detail?}`, `localImage {path,detail?}`, `audio {url}`, `localAudio {path}`, `skill {name,path}`, and `mention {name,path}`. atto takes text plus base64 images. Text/images are implementable; validate local paths, data URLs and sizes; remote image fetching has SSRF/security implications and fileId requires an external file service. Mentions/skills need explicit context expansion/provenance. Audio and structured tool output aren't available. Reject unsupported typed content rather than dropping it.

### Notifications with an atto counterpart

| Codex notification | atto counterpart | Required translation |
|---|---|---|
| `thread/started` | missing native notification | Emit the full `{thread}` after start with correct ordering/subscriptions. |
| `thread/status/changed` | live `thread/updated`, or standalone state inferred | `{threadId,status:ThreadStatus}`; don't send flat atto thread. |
| `turn/started`, `turn/completed` | same names, shape differs | Codex `{threadId,turn:Turn}` vs atto `{threadId,turnId,startedAt}` / `{threadId,turnId,status,error?,usage,contextTokens}`. Convert timestamps and errors; finalize items before completion. |
| `item/started`, `item/completed` | same outer keys | Both `{threadId,turnId,item}`; translate tagged item union and IDs. This is the strongest structural overlap, not complete payload identity. |
| `item/agentMessage/delta` | generic `item/delta` | Dispatch by tracked item type; preserve `{threadId,turnId,itemId,delta}`. |
| `item/reasoning/summaryTextDelta`, `/summaryPartAdded`, `/textDelta` | generic `item/delta` | Track `summaryIndex`/`contentIndex` and summary-part creation; choose correct channel. |
| `item/commandExecution/outputDelta` | generic `item/delta` for output | Same base keys with command output semantics. atto writing command metadata is `item/updated`; it is **not** command output. Cache/finalize pending command updates, don't replay them as stdout. |
| `thread/tokenUsage/updated` | `thread/usage` renamed + shape differs | Codex `{threadId,turnId,tokenUsage:{total,last,modelContextWindow}}`; breakdown has `totalTokens,inputTokens,cachedInputTokens,cacheWriteInputTokens,outputTokens,reasoningOutputTokens`. atto `{usage,step,contextTokens}` includes cost. Map available counters; track/report unavailable reasoning split honestly; current context occupancy is not identical to lifetime total token usage. |
| `thread/compacted` (deprecated) | compaction item completion | Optional backward notification `{threadId,turnId}`; prefer contextCompaction item. |
| `hook/started`, `hook/completed` | atto `hook` | Rich HookRun lifecycle/IDs aren't present in native notice; limited mapping possible, full tracing missing. |
| `thread/goal/updated`, `/cleared` | live `goal/updated` | Goal object/status/budget conversion and thread-owned semantics, not an alias. |
| `thread/queue/changed` | live `turn/pending` | Codex queued submission IDs and typed inputs versus atto strings and queued/steer separation. |
| `thread/settings/updated`, `thread/name/updated` | live `thread/updated` | Split changed fields, create the exact Codex settings/name payload. |
| `error`, `warning`, `configWarning` | failed turn / reload notices / hooks | Send only when semantics fit; Codex `error` has `error:TurnError,willRetry,threadId?,turnId?`, not arbitrary atto title. |

Missing notifications include turn diff/plan, file patches, MCP progress/status/OAuth/events, PTY interactions, approvals resolved, account/login/rate-limit updates, catalog/app/plugin/import changes, filesystem watches, model reroute/verification/auth recovery, Guardian and moderation, realtime audio/transcript, native Windows sandbox state, and remote-control state. Appendix B lists each exact method and counterpart/status.

Native atto-only notifications are `item/delta`, `item/updated`, `hook`, `event`, `thread/reloaded`, `extension/notify`, `item/display`, `extension/ui`, `thread/usage`, `turn/pending`, `events/reset`, `thread/switched`, `thread/updated`, `goal/updated`, `prompt/open`, `prompt/closed`. Their extension/UI/SSE/live semantics must remain on the native endpoint, or receive a separately negotiated extension; broadcasting them blindly to Codex clients is not safe.

## 5. Server-to-client requests, approvals, and sandboxing

Codex server requests have **their own RPC IDs and await ordinary client result/error responses**, not notifications. atto's `Handle` only treats inbound messages as client requests/notifications; there is no pending server-request resolver, approval callback routing or timeout/cancellation machinery. atto live `prompt/open` + `prompt/answer` is a terminal picker mirror with first-answer-wins, **not** a tool-execution authorization boundary.

| Codex server request | Important fields / response | atto status |
|---|---|---|
| `item/commandExecution/requestApproval` | `threadId,turnId,itemId,startedAtMs,kind,approvalId?,environmentId,reason?,command?,cwd?,commandActions?,networkApprovalContext?,proposedExecpolicyAmendment?,proposedNetworkPolicyAmendments?`; response `{decision}` with `accept,acceptForSession,decline,cancel` or structured policy amendments | Missing. Must pause **before** executing, route callback, enforce deny/cancel; amendments affect future policy. |
| `item/fileChange/requestApproval` | `threadId,turnId,itemId,startedAtMs,reason?,grantRoot?`; `{decision}` | Missing. No structured patch executor. |
| `item/permissions/requestApproval` | `threadId,turnId,itemId,environmentId,cwd,reason,permissions,startedAtMs`; `{permissions,scope,strictAutoReview?}` | Missing permissions-grant runtime. |
| `item/tool/requestUserInput` | `threadId,turnId,itemId,questions,isBlocking,autoResolutionMs`; `{answers:{questionId:{answers:[string]}}}` | Missing structured model tool; live prompt is only a partial UI building block. |
| `mcpServer/elicitation/request` | `threadId,turnId?,serverName`, tagged form/openai-form/url mode, `_meta,message,requestedSchema` or `url,elicitationId`; response action/content | Missing protocol bridge, even if MCP/extension prompts exist internally. |
| `item/tool/call` | `threadId,turnId,callId,namespace,tool,arguments`; structured output content and success response | Missing client-side dynamic-tool execution. |
| `account/chatgptAuthTokens/refresh` | `reason,previousAccountId?`; client returns fresh access token/account/plan identity | Missing; don't mix this with atto's saved-provider login flow. |
| `attestation/generate` | Empty params `{}` → `{token:string}` (opaque client attestation); capability opt-in | Missing. No genuine attestation provider to emulate. |
| `currentTime/read` (experimental) | `{threadId}` → `{currentTimeAt}` (whole Unix seconds) | Missing; optional for initial chat. |
| `applyPatchApproval`, `execCommandApproval` (deprecated v1) | Legacy approval params/decisions | Missing; don't implement unless a selected client still needs v1. |

Codex approval policy at this snapshot is `"untrusted"`, `"on-request"`, `"never"`, or granular flags (`sandbox_approval,rules,skill_approval,request_permissions,mcp_elicitations`). Older releases may have other variants. `ApprovalsReviewer` distinguishes user/Guardian review. Sandbox mode uses `read-only`, `workspace-write`, `danger-full-access`; returned SandboxPolicy is a tagged `dangerFullAccess`, `readOnly {networkAccess}`, `workspaceWrite {writableRoots,networkAccess,excludeTmpdirEnvVar,excludeSlashTmp}`, or `externalSandbox {networkAccess}`. New named permission profiles/grants add filesystem/network policy and provenance.

atto executes shell commands through its agent/shell machinery. Hooks (`PreToolUse`, etc.) can block/modify calls; extension/MCP code approval files govern loading integrations. **Those are not Codex OS-level execution sandboxing or per-command approval.** `config.EnvAgent` explicitly says its environment guard is not a sandbox and the model can unset it. An adapter can implement an approval gate around bash execution, but cannot create trustworthy filesystem/network isolation by filtering command strings; shell subprocesses must actually be constrained. FileChange approvals cannot cover arbitrary writes in bash without an execution policy that constrains them.

Initial policy choices: accept only an explicitly selected `approvalPolicy:"never"` + full-access policy, or run under an independently enforced external sandbox whose policy is honestly reported. Reject restrictive/default-required safety modes with actionable errors. This may prevent official clients' default startup flow; that is a real compatibility blocker, not a reason to lie about enforcement. Never use auto-accept approval requests as purported safety parity.

## 6. What real clients need

### What could be verified from this repository

**Remote Codex TUI is the best first real target.** `app-server-client/src/remote.rs` implements WS/WSS and WS-over-UDS endpoints, sends initialize, extracts server metadata, sends initialized, dispatches typed server notifications/requests, resolves client requests, rejects unknown server requests, and handles connection loss. It does not implement HTTP/SSE. Remote TUI therefore fails before chatting against today's native atto HTTP endpoint.

In `tui/src/app_server_session.rs`, bootstrap fetches `account/read`, then `model/list` and **`configRequirements/read` via `tokio::try_join!` with errors propagated**; collaboration-mode catalog is fetched too. Comments explicitly discuss overlapping a `hooks/list` startup request. Model catalog can't simply be an empty list (default bootstrap treats missing usable models as error). Thus implementing initialize + thread/start + turn/start alone does **not** establish current TUI startup compatibility.

Verified TUI paths also use:

- New/resumed/forked thread: effective settings wrappers, UUID IDs, history-mode/pagination capability fallback, attachments capability probing; `thread/read`, `thread/list`/loaded list and history hydration for resume/pickers.
- Per turn: `turn/start`, `turn/steer {expectedTurnId}`, `turn/interrupt {turnId}`, model/effort/sandbox settings, client dynamic-tool transport where selected; typed items and completion/errors converted into TUI events.
- Background/settings/UI: `account/rateLimits/read`, `account/usage/read`, `skills/list`, `experimentalFeature/list`, `collaborationMode/list`, `config/read`/batch writes, account status and provider configuration.
- User-selected actions: archive/delete/unarchive/name, goals, compaction, shell command, review, memory and Guardian approval actions; remote filesystem methods (`app_server_session/fs.rs`), realtime and plugins on corresponding paths.

Not all these calls are unconditional or fatal. Some capability discovery catches unsupported methods or deserialization mismatches. A capability probe is **not** proof that any arbitrary malformed response works. Pin tests to the chosen client and preserve its explicit fallback/error conditions; inspect `history_tests.rs`, `rollout_history_tests.rs`, permission-projection tests and startup lifecycle tests.

`app-server-client` in-process facade is used by **TUI and exec**, but typed in-memory runtime startup isn't a portable remote backend hook. `exec/src/lib.rs` demonstrates initialize identity and thread/start or resume → turn/start and event handling; it does not prove every `codex exec` invocation can swap in an external atto binary. Embedded TUI/exec may reconcile late `SessionConfigured` **legacy** events; remote paths project v2 RPC results into their own core session representation. Do not indiscriminately emit the historical legacy event stream on the external endpoint.

### What could not be verified

The public checkout does **not** contain the production Codex VS Code extension or desktop application's complete client code. `.vscode` is repository editor configuration, not extension source. Protocol comments, session-source variants (`vscode`, CLI, appServer), integration tests and desktop-specific verification behavior show intended consumers, **not their exact current startup trace or backend redirection settings**.

Likely IDE/desktop startup needs initialize, auth/account state, effective config/requirements, model catalog, thread list/resume/read, and catalogs of enabled capabilities. Per-turn needs typed UserInput, effective settings, streaming item events, completion, approvals and user-input requests. IDE context tools may require `dynamicTools` + server `item/tool/call`; desktop local worktree/terminal/file UI may require fs/command APIs, and cloud/account/Apps UI cannot be supplied by atto. These are evidence-based expectations, **not verified call traces**. Official clients may require Codex-specific CLI flags/version output, spawn arguments, bundled executable paths, signed login or hosted service integration; protocol compatibility alone doesn't override those deployment constraints.

| Claim | Minimum success bar |
|---|---|
| Core chat works with a custom/third-party client | Handshake, real model catalog, start/resume/read/list, text/image input, items/deltas, interrupt/steer, correct terminal states and repeatable history IDs. Unsupported features explicitly rejected. |
| Current remote TUI works | Above + WS/UDS, UUID mapping, required bootstrap config/account/catalog methods, full effective-settings wrapper, history fallback/probes, compatible error/notification behavior; safety settings explicitly supported or rejected. Validate actual TUI integration. |
| Selected IDE build works | Verify backend redirection/spawn contract and its recorded startup/per-turn calls; support the required catalogs/config and dynamic-tool/approval paths or ensure they're not requested. Test the actual released extension. |
| Desktop/full feature parity | Safety enforcement, tool/file/terminal/worktree UX, reviews, MCP and Apps/resources, account/login/rate limits, realtime/native verification/remote/cloud services. Not a schema-only adapter milestone; some services are outside atto's control. |

A Rust/TS protocol library describes messages, not an automatic compatibility certification. Its consumers may assume required nullable fields, closed unions, UUIDs, ordering, durable histories, and safe policies.

## 7. Options and costs

Working days for **one experienced agent**, including implementation and deterministic tests but excluding access delays and long human security/release review. Ranges are cumulative per stated milestone, not promises. No code prototype was implemented in this research.

| Option | Effort | Main risks / assessment |
|---|---|---|
| **(a) Replace/reshape native protocol into a strict Codex superset, retaining atto extras** | ~20–40 days for core/chat + native-client migration; ~60–120 days for a substantial local feature set, excluding actual full hosted parity and new sandbox implementation | Same method names have conflicting shapes: `turn/start.input` string vs array, flat thread vs wrapper, generic vs typed deltas. A single answer containing all fields may appease permissive JS but isn't a strict Codex schema superset. Requires a native version/dialect split or migrating web/live clients/tests; unknown extra item variants remain problematic. Codex churn contaminates atto's primary API. “Strict superset” can mean **same Codex semantics plus separately negotiated atto methods**, not arbitrary extras in every Codex object. High regression risk; little advantage over an adapter. |
| **(b) Separate compatibility endpoint/adapter** | ~8–15 days core; ~15–30 total for one pinned TUI/IDE-oriented target with verified bootstraps; ~40–80 total for broader local history/config/file/search/MCP/review UX, **without** full sandbox/hosted parity | Best isolation. Reuse atto agent/session/transcript, but adapter owns connection state, IDs, persisted turn projection, schemas, catalog/config projections and subscriptions. Transport/method translation isn't enough for policy and dynamic tools. High-risk gaps can remain explicit unsupported capabilities. |
| **(c) Partial core-chat dialect only** | ~4–7 days text-only prototype with narrow stored-thread support and deterministic client; ~8–15 days credible core with history/images/reconnect/error tests | Useful for third-party clients written to the documented subset. Not automatically suitable for official clients; fatal startup calls and default sandbox expectations still block. Lowest maintenance scope but must advertise a **documented profile**, not general Codex compatibility. |

Additional work potentially needed regardless of option:

- Real cross-platform sandbox + approvals/permission broker: roughly **20–40 additional days for a focused supported-platform first implementation**, **40–80+** for robust multi-OS execution/network/filesystem policies and tests; security review still required. Reusing a trusted external sandbox can reduce initial scope but is not equivalent to implementing all Codex named profiles/Guardian policies.
- Genuine dynamic-tool/structured editing/MCP event integration: ~10–25 days depending on desired semantics and internal tool changes. These cannot all be delegated to the endpoint alone.
- Official IDE/desktop validation: budget at least several extra days **after** access and backend redirection are verified. If the desktop cannot target a custom local server, that client remains blocked regardless of protocol effort.
- Full Codex parity (all 173 methods, 86 notifications, hosted/account/platform integrations): **not a bounded translation project**. Local lookalike implementations are not equivalent to OpenAI service integration; don't quote a small finite schedule for this promise.

### Licensing and change rate

Codex root `LICENSE` is **Apache-2.0**. Vendoring its protocol schema/TS artifacts and deriving Go types or test fixtures is generally allowed, including commercially, provided applicable license/attribution/NOTICE obligations are retained and modifications are identified. Root `NOTICE` credits OpenAI and Ratatui-derived code; retain relevant notices and check artifact/dependency licenses rather than assuming the entire dependency tree has one license. Apache grants patent rights subject to its terms, **not trademark rights or OpenAI backend/account access**. Use wording such as “Codex app-server protocol adapter”; don't imply official endorsement. This is a practical licensing assessment, not legal advice.

Churn measured from the deeper source history (2,501 commits reachable, shallow boundary in August, safely before the measured interval):

```sh
git log --since=2026-09-07 --format='%h %ad %s' --date=short \
  -- codex-rs/app-server-protocol
```

**94 path-touching commits dated September 7–October 6 (30 calendar days); 23 dated September 30–October 6 (7 days); 9 on October 6 alone.** Not every path-touching commit changes wire compatibility; this is a churn indicator, not 94 breaking changes. Concrete recent examples:

- `cfc946f4`, Oct 6: persisted turn lineage across app-server (`rootTurnId`, related turn inputs).
- `b0a6b8d8`, Oct 6: resolved model/effort in sub-agent activity.
- `8b6bb1c7`, Oct 6: partial-answer message phase.
- `7ac954ea`, Oct 6: independent Fast/Ultra Fast policy fields.
- `c5d242fa`, Oct 2: command output consolidation into `aggregated_output`.
- `bee28e8a`, Oct 2: paginated command-output cap; `c73775f1`: session configuration event changes.
- `2635431e`, Oct 1: attachment owner lookup.
- `a5d56d81`, Sep 30: unknown Codex error variant tolerance.
- `90abcfac`, Sep 30: experimental thread prediction protocol.

This is a fast-moving protocol. Pin production support to named release builds (whose shapes may differ from this HEAD), vendor generated contracts with upstream commit recorded, maintain golden schema/request/event fixtures, and run automated schema diffs on upgrades. Budget recurring maintenance (roughly 1–3 days per supported-client upgrade, more for lifecycle/safety changes), rather than continually tracking HEAD automatically. Stable vs experimental filtering reduces scope but doesn't eliminate required-field/client-behavior drift.

## 8. Recommended phased plan and acceptance tests

**Choose (b), delivered initially with the narrow scope of (c).** Keep native atto serving and `/remote` intact. Prefer sharing internal service operations/transcript events over loopback HTTP→SSE proxying, while making the dialect boundary explicit. A proxy can prove shapes, but internal observation is needed for stable turn identity and authorization.

1. **Pin and define profile (1–2 days).** Select a Codex release/TUI binary and a custom typed reference client; record generated stable and necessary experimental contracts. Verify IDE backend override separately before promising IDE support. Define full-access/external-sandbox safety contract, unsupported input/method matrix, and required catalog defaults. Preserve atto-only data on native endpoint.
2. **Core stdio adapter (3–5 additional days).** Duplex envelope parser/response resolver, handshake, typed errors, UUID mapping, thread/turn projection, typed item/delta translation, start/steer/interrupt, model catalog and read/resume/list. Deterministic fake agent/provider and golden event traces; no real-model calls needed.
3. **Persistence, images, and transport hardening (4–8 additional days, overlap possible).** Durable IDs/turn groups and restart/branch handling; complete native-image conversion and unsupported typed-content rejection. WS first, WS-over-UDS next if needed; local-only default, auth/Origin/TLS guidance, bounded queues and subscription lifecycle. Never reuse atto all-interface `/remote` exposure as the default compatibility listener.
4. **Pinned TUI bootstrap and selected IDE profile (5–15 additional days).** Truthful account/config/requirements/skills/hooks/feature/collaboration projections; history mode and capability-probe fallback or proper pagination. Test actual remote TUI with mocked responses/agents. Obtain IDE startup/per-turn traces and implement only justified dependencies; gate dynamic tools until truly supported. Stop here with a useful documented release if broader features aren't required.
5. **Optional feature lanes.** Search/history management/review/diff UI, then structured edit/MCP/dynamic tools and direct fs/process sessions with explicit authorization. Develop sandbox/approvals separately before advertising safe policies. Hosted account/App/cloud/realtime/native attestation features remain unsupported unless genuine integrations exist.

Acceptance suite must check more than successful JSON unmarshaling:

- Initialize omission/defaults/duplicates, experimental rejection, exact notification opt-outs; integer/string IDs and out-of-order responses; client server-request replies distinguished from new requests.
- Golden thread/turn/item shapes against pinned schemas; no unknown native item tags; required null fields and seconds↔milliseconds conversion; external UUIDs stable across restart.
- Concurrent threads, turn-start response/event ordering, reasoning part indices, command metadata vs stdout, item completion before turn completion; interrupted/failed/completed terminal state; stale expectedTurnId/turnId rejection.
- Read/resume/list across unloaded threads, branch rollback/compaction/steers, pagination/fallback; don't reconstruct synthetic inputs as new user turns. Reconnect recovers state without SSE-only methods.
- Text/image input roundtrip and preserved client IDs; unsupported audio/file IDs/outputSchema/dynamic tools fail clearly rather than silently losing content.
- Requested read-only/workspace-write/approval policies fail **before execution** if not implemented; approvals reject/cancel before shell run if enabled; no falsely advertised isolation.
- WS/UDS handshake, bearer auth, Origin rejection, ping/pong, disconnect/reconnect, slow-client backpressure; stdout stays JSON-only on stdio.
- Pinned remote TUI bootstraps and runs a deterministic fake turn; selected IDE does the same once redirection and traces are obtained. Desktop is a separate verified target, not inferred from passing TUI tests.

**Decision:** compatibility is feasible and strategically useful, but the reusable asset is atto's agent lifecycle, not its existing wire shapes. An isolated, version-pinned, honest adapter offers most of the benefit without destabilizing native atto or pretending to implement Codex's safety and hosted product features.

## Appendix A. Complete Codex client-request inventory

Extracted from the snapshot's `client_request_definitions!` registry. **E** marks a method-level `#[experimental]`; unmarked methods can still have experimental fields. Params/response names resolve in `app-server-protocol/src/protocol` or the generated schema. Field samples are camelCase request-envelope fields (first six source fields), not an exhaustive property list; tagged/optional types are detailed in the main sections and upstream schema. **Missing** means no native atto RPC, not necessarily no internal related functionality. Legacy compatibility methods at the end are included.

| Codex method | Params (sample fields) → response | atto mapping |
|---|---|---|
| `initialize` | `InitializeParams` (clientInfo, capabilities) → `InitializeResponse` | Shape differs (§2) |
| `server/diagnostics` **E** | `ServerDiagnosticsParams` ({}) → `ServerDiagnosticsResponse` | Missing |
| `userVerification/status` **E** | `UserVerificationStatusParams` ({}) → `UserVerificationStatusResponse` | Missing |
| `userVerification/enroll` **E** | `UserVerificationEnrollParams` ({}) → `UserVerificationEnrollResponse` | Missing |
| `userVerification/delete` **E** | `UserVerificationDeleteParams` ({}) → `UserVerificationDeleteResponse` | Missing |
| `userVerification/verify` **E** | `UserVerificationVerifyParams` (challenge, title, description) → `UserVerificationVerifyResponse` | Missing |
| `userVerification/cancel` **E** | `UserVerificationCancelParams` (requestId) → `UserVerificationCancelResponse` | Missing |
| `thread/start` | `ThreadStartParams` (model, modelProvider, allowProviderModelFallback, serviceTier, cwd, runtimeWorkspaceRoots, …) → `ThreadStartResponse` | Shape differs (§3) |
| `thread/resume` | `ThreadResumeParams` (threadId, history, path, model, modelProvider, serviceTier, …) → `ThreadResumeResponse` | Shape differs (§3) |
| `thread/fork` | `ThreadForkParams` (threadId, lastTurnId, beforeTurnId, path, model, modelProvider, …) → `ThreadForkResponse` | Missing RPC; session branches exist |
| `thread/archive` | `ThreadArchiveParams` (threadId) → `ThreadArchiveResponse` | Missing |
| `thread/delete` | `ThreadDeleteParams` (threadId) → `ThreadDeleteResponse` | Missing |
| `thread/unsubscribe` | `ThreadUnsubscribeParams` (threadId) → `ThreadUnsubscribeResponse` | Missing |
| `thread/increment_elicitation` **E** | `ThreadIncrementElicitationParams` (threadId) → `ThreadIncrementElicitationResponse` | Missing |
| `thread/decrement_elicitation` **E** | `ThreadDecrementElicitationParams` (threadId) → `ThreadDecrementElicitationResponse` | Missing |
| `thread/name/set` | `ThreadSetNameParams` (threadId, name) → `ThreadSetNameResponse` | Missing |
| `thread/prediction/request` **E** | `ThreadPredictionRequestParams` (threadId, sourceTurnId) → `ThreadPredictionRequestResponse` | Missing |
| `thread/goal/set` | `ThreadGoalSetParams` (threadId, origin, objective, status, tokenBudget) → `ThreadGoalSetResponse` | Missing RPC; internal goal feature |
| `thread/goal/get` | `ThreadGoalGetParams` (threadId) → `ThreadGoalGetResponse` | Missing RPC; live goal field only |
| `thread/goal/clear` | `ThreadGoalClearParams` (threadId, origin) → `ThreadGoalClearResponse` | Missing RPC; internal goal feature |
| `thread/queue/add` **E** | `ThreadQueueAddParams` (threadId, input, clientUserMessageId) → `ThreadQueueAddResponse` | Missing typed queue RPC; live string queue exists |
| `thread/queue/list` **E** | `ThreadQueueListParams` (threadId, cursor, limit) → `ThreadQueueListResponse` | Missing typed queue RPC; live string queue exists |
| `thread/queue/update` **E** | `ThreadQueueUpdateParams` (threadId, queuedSubmissionId, input) → `ThreadQueueUpdateResponse` | Missing typed queue RPC; live string queue exists |
| `thread/queue/delete` **E** | `ThreadQueueDeleteParams` (threadId, queuedSubmissionId) → `ThreadQueueDeleteResponse` | Missing typed queue RPC; live string queue exists |
| `thread/queue/reorder` **E** | `ThreadQueueReorderParams` (threadId, queuedSubmissionIds) → `ThreadQueueReorderResponse` | Missing typed queue RPC; live string queue exists |
| `thread/queue/start` **E** | `ThreadQueueStartParams` (threadId, queuedSubmissionId) → `ThreadQueueStartResponse` | Missing typed queue RPC; live string queue exists |
| `thread/metadata/update` | `ThreadMetadataUpdateParams` (threadId, projectId, gitInfo, daybreakEnabled) → `ThreadMetadataUpdateResponse` | Missing |
| `thread/attachment/add` | `ThreadAttachmentAddParams` (threadId, attachmentType, identityKey, payload) → `ThreadAttachmentAddResponse` | Missing |
| `thread/attachment/list` | `ThreadAttachmentListParams` (threadId, cursor, limit) → `ThreadAttachmentListResponse` | Missing |
| `thread/attachmentOwner/list` | `ThreadAttachmentOwnerListParams` (attachmentType, identityKey, archived, cursor, limit) → `ThreadAttachmentOwnerListResponse` | Missing |
| `thread/attachment/remove` | `ThreadAttachmentRemoveParams` (threadId, attachmentType, identityKey) → `ThreadAttachmentRemoveResponse` | Missing |
| `thread/section/move` | `ThreadSectionMoveParams` (threadId, sectionId, beforeThreadId) → `ThreadSectionMoveResponse` | Missing |
| `thread/settings/update` **E** | `ThreadSettingsUpdateParams` (threadId, disabledPluginIds, cwd, approvalPolicy, approvalsReviewer, sandboxPolicy, …) → `ThreadSettingsUpdateResponse` | Partial setModel/setEffort counterpart |
| `thread/memoryMode/set` **E** | `ThreadMemoryModeSetParams` (threadId, mode) → `ThreadMemoryModeSetResponse` | Missing |
| `memory/status` **E** | `MemoryStatusParams` (minConsolidatedThreads) → `MemoryStatusResponse` | Missing |
| `memory/reset` **E** | `Option<()>` (none) → `MemoryResetResponse` | Missing |
| `rollout/compress` **E** | `Option<()>` (none) → `RolloutCompressResponse` | Missing |
| `thread/unarchive` | `ThreadUnarchiveParams` (threadId) → `ThreadUnarchiveResponse` | Missing |
| `thread/compact/start` | `ThreadCompactStartParams` (threadId) → `ThreadCompactStartResponse` | Renamed thread/compact + shape differs |
| `thread/shellCommand` | `ThreadShellCommandParams` (threadId, command, timeoutMs) → `ThreadShellCommandResponse` | Missing |
| `thread/approveGuardianDeniedAction` | `ThreadApproveGuardianDeniedActionParams` (threadId, event) → `ThreadApproveGuardianDeniedActionResponse` | Missing |
| `thread/backgroundTerminals/clean` **E** | `ThreadBackgroundTerminalsCleanParams` (threadId) → `ThreadBackgroundTerminalsCleanResponse` | Missing; jobs are not PTY cleanup |
| `thread/backgroundTerminals/list` **E** | `ThreadBackgroundTerminalsListParams` (threadId, cursor, limit) → `ThreadBackgroundTerminalsListResponse` | Partial job/list; terminal semantics differ |
| `thread/backgroundTerminals/terminate` **E** | `ThreadBackgroundTerminalsTerminateParams` (threadId, processId) → `ThreadBackgroundTerminalsTerminateResponse` | Partial job/stop; IDs/semantics differ |
| `thread/revert` | `ThreadRevertParams` (threadId, beforeTurnId) → `ThreadRevertResponse` | Different rollback semantics (§3) |
| `thread/list` | `ThreadListParams` (cursor, limit, sortKey, sortDirection, modelProviders, sourceKinds, …) → `ThreadListResponse` | Shape differs (§3) |
| `project/list` **E** | `ProjectListParams` (cursor, limit, sortKey, sortDirection) → `ProjectListResponse` | Missing |
| `project/read` **E** | `ProjectReadParams` (projectId) → `ProjectReadResponse` | Missing |
| `project/create` **E** | `ProjectCreateParams` (name, roots, metadata, idempotencyKey) → `ProjectCreateResponse` | Missing |
| `project/import` **E** | `ProjectImportParams` (name, roots, metadata, threads, idempotencyKey) → `ProjectImportResponse` | Missing |
| `project/update` **E** | `ProjectUpdateParams` (projectId, name, roots, metadata) → `ProjectUpdateResponse` | Missing |
| `project/move` **E** | `ProjectMoveParams` (projectId, beforeProjectId) → `ProjectMoveResponse` | Missing |
| `project/delete` **E** | `ProjectDeleteParams` (projectId) → `ProjectDeleteResponse` | Missing |
| `threadSection/list` | `ThreadSectionListParams` (cursor, limit) → `ThreadSectionListResponse` | Missing |
| `threadSection/create` | `ThreadSectionCreateParams` (name, appearance) → `ThreadSectionCreateResponse` | Missing |
| `threadSection/update` | `ThreadSectionUpdateParams` (sectionId, name, appearance) → `ThreadSectionUpdateResponse` | Missing |
| `threadSection/delete` | `ThreadSectionDeleteParams` (sectionId) → `ThreadSectionDeleteResponse` | Missing |
| `thread/search` **E** | `ThreadSearchParams` (cursor, limit, sortKey, sortDirection, sourceKinds, archived, …) → `ThreadSearchResponse` | Missing |
| `thread/searchOccurrences` **E** | `ThreadSearchOccurrencesParams` (threadId, searchTerm, cursor, limit) → `ThreadSearchOccurrencesResponse` | Missing |
| `thread/loaded/list` | `ThreadLoadedListParams` (cursor, limit) → `ThreadLoadedListResponse` | Missing |
| `thread/read` | `ThreadReadParams` (threadId, includeTurns) → `ThreadReadResponse` | Shape differs (§3) |
| `thread/turns/list` | `ThreadTurnsListParams` (threadId, cursor, limit, sortDirection, itemsView) → `ThreadTurnsListResponse` | Missing |
| `thread/items/list` | `ThreadItemsListParams` (threadId, turnId, cursor, limit, sortDirection) → `ThreadItemsListResponse` | Missing |
| `thread/inject_items` | `ThreadInjectItemsParams` (threadId, items) → `ThreadInjectItemsResponse` | Missing |
| `skills/list` | `SkillsListParams` (cwds, forceReload) → `SkillsListResponse` | Missing RPC; internal skills exist |
| `skills/extraRoots/set` | `SkillsExtraRootsSetParams` (extraRoots) → `SkillsExtraRootsSetResponse` | Missing RPC; internal skills exist |
| `hooks/list` | `HooksListParams` (cwds) → `HooksListResponse` | Missing RPC; internal hooks exist |
| `marketplace/add` | `MarketplaceAddParams` (source, refName, sparsePaths) → `MarketplaceAddResponse` | Missing |
| `marketplace/remove` | `MarketplaceRemoveParams` (marketplaceName) → `MarketplaceRemoveResponse` | Missing |
| `marketplace/upgrade` | `MarketplaceUpgradeParams` (marketplaceName) → `MarketplaceUpgradeResponse` | Missing |
| `plugin/list` | `PluginListParams` (cwds, marketplaceKinds, forceRefetch) → `PluginListResponse` | Missing |
| `plugin/search` **E** | `PluginSearchParams` (searchTerm, scope, cwds, cursor, limit) → `PluginSearchResponse` | Missing |
| `plugin/installed` | `PluginInstalledParams` (cwds, installSuggestionPluginNames) → `PluginInstalledResponse` | Missing |
| `plugin/reconcile` | `PluginReconcileParams` (reason) → `PluginReconcileResponse` | Missing |
| `plugin/read` | `PluginReadParams` (marketplacePath, remoteMarketplaceName, pluginName) → `PluginReadResponse` | Missing |
| `plugin/skill/read` | `PluginSkillReadParams` (remoteMarketplaceName, remotePluginId, skillName) → `PluginSkillReadResponse` | Missing |
| `plugin/share/save` | `PluginShareSaveParams` (pluginPath, remotePluginId, discoverability, shareTargets) → `PluginShareSaveResponse` | Missing |
| `plugin/share/updateTargets` | `PluginShareUpdateTargetsParams` (remotePluginId, discoverability, shareTargets) → `PluginShareUpdateTargetsResponse` | Missing |
| `plugin/share/list` | `PluginShareListParams` ({}) → `PluginShareListResponse` | Missing |
| `plugin/share/checkout` | `PluginShareCheckoutParams` (remotePluginId) → `PluginShareCheckoutResponse` | Missing |
| `plugin/share/delete` | `PluginShareDeleteParams` (remotePluginId) → `PluginShareDeleteResponse` | Missing |
| `app/read` | `AppsReadParams` (appIds, threadId, includeTools) → `AppsReadResponse` | Missing |
| `app/list` | `AppsListParams` (cursor, limit, threadId, forceRefetch) → `AppsListResponse` | Missing |
| `app/installed` | `AppsInstalledParams` (threadId, forceRefresh) → `AppsInstalledResponse` | Missing |
| `fs/readFile` | `FsReadFileParams` (path) → `FsReadFileResponse` | Missing client filesystem API |
| `fs/writeFile` | `FsWriteFileParams` (path, dataBase64) → `FsWriteFileResponse` | Missing client filesystem API |
| `fs/createDirectory` | `FsCreateDirectoryParams` (path, recursive) → `FsCreateDirectoryResponse` | Missing client filesystem API |
| `fs/getMetadata` | `FsGetMetadataParams` (path) → `FsGetMetadataResponse` | Missing client filesystem API |
| `fs/readDirectory` | `FsReadDirectoryParams` (path) → `FsReadDirectoryResponse` | Missing client filesystem API |
| `fs/remove` | `FsRemoveParams` (path, recursive, force) → `FsRemoveResponse` | Missing client filesystem API |
| `fs/copy` | `FsCopyParams` (sourcePath, destinationPath, recursive) → `FsCopyResponse` | Missing client filesystem API |
| `fs/watch` | `FsWatchParams` (watchId, path) → `FsWatchResponse` | Missing client filesystem API |
| `fs/unwatch` | `FsUnwatchParams` (watchId) → `FsUnwatchResponse` | Missing client filesystem API |
| `skills/config/write` | `SkillsConfigWriteParams` (path, name, enabled) → `SkillsConfigWriteResponse` | Missing RPC; internal skills exist |
| `plugin/install` | `PluginInstallParams` (marketplacePath, remoteMarketplaceName, installAttemptId, pluginName) → `PluginInstallResponse` | Missing |
| `plugin/uninstall` | `PluginUninstallParams` (pluginId) → `PluginUninstallResponse` | Missing |
| `turn/start` | `TurnStartParams` (threadId, disabledPluginIds, clientUserMessageId, input, turnTrigger, parentTurnId, …) → `TurnStartResponse` | Shape differs (§3) |
| `turn/settings/update` **E** | `TurnSettingsUpdateParams` (threadId, turnId, approvalsReviewer, model, effort, summary, …) → `TurnSettingsUpdateResponse` | No active-turn-only equivalent |
| `turn/steer` | `TurnSteerParams` (threadId, clientUserMessageId, input, responsesapiClientMetadata, additionalContext, expectedTurnId) → `TurnSteerResponse` | Shape differs (§3) |
| `turn/interrupt` | `TurnInterruptParams` (threadId, turnId) → `TurnInterruptResponse` | Extra target-turn requirement (§3) |
| `thread/realtime/start` **E** | `ThreadRealtimeStartParams` (threadId, clientManagedHandoffs, delegationAckFiller, flushTranscriptTailOnSessionEnd, codexResponsesAsItems, codexResponseItemPrefix, …) → `ThreadRealtimeStartResponse` | Missing |
| `thread/realtime/appendAudio` **E** | `ThreadRealtimeAppendAudioParams` (threadId, audio) → `ThreadRealtimeAppendAudioResponse` | Missing |
| `thread/realtime/appendText` **E** | `ThreadRealtimeAppendTextParams` (threadId, text, role) → `ThreadRealtimeAppendTextResponse` | Missing |
| `thread/realtime/appendSpeech` **E** | `ThreadRealtimeAppendSpeechParams` (threadId, text) → `ThreadRealtimeAppendSpeechResponse` | Missing |
| `thread/realtime/stop` **E** | `ThreadRealtimeStopParams` (threadId) → `ThreadRealtimeStopResponse` | Missing |
| `thread/timeline/list` **E** | `ThreadTimelineListParams` (threadId, cursor, limit) → `ThreadTimelineListResponse` | Missing |
| `thread/realtime/listVoices` **E** | `ThreadRealtimeListVoicesParams` ({}) → `ThreadRealtimeListVoicesResponse` | Missing |
| `review/start` | `ReviewStartParams` (threadId, target, delivery) → `ReviewStartResponse` | Missing review runtime |
| `model/list` | `ModelListParams` (cursor, limit, includeHidden) → `ModelListResponse` | Renamed models/list + shape differs |
| `account/gatewayOAuth/read` | `Option<()>` (none) → `GatewayOAuthReadResponse` | Missing account RPC/service |
| `account/gatewayOAuth/login` | `Option<()>` (none) → `GatewayOAuthLoginResponse` | Missing account RPC/service |
| `account/gatewayOAuth/cancel` | `Option<()>` (none) → `GatewayOAuthCancelResponse` | Missing account RPC/service |
| `modelProvider/capabilities/read` | `ModelProviderCapabilitiesReadParams` ({}) → `ModelProviderCapabilitiesReadResponse` | Missing |
| `experimentalFeature/list` | `ExperimentalFeatureListParams` (cursor, limit, threadId) → `ExperimentalFeatureListResponse` | Missing |
| `permissionProfile/list` | `PermissionProfileListParams` (cursor, limit, cwd) → `PermissionProfileListResponse` | Missing |
| `experimentalFeature/enablement/set` | `ExperimentalFeatureEnablementSetParams` (enablement) → `ExperimentalFeatureEnablementSetResponse` | Missing |
| `remoteControl/enable` **E** | `NullableRemoteControlEnableParams` (see tagged/optional type) → `RemoteControlEnableResponse` | Missing relay protocol; not atto /remote |
| `remoteControl/disable` **E** | `NullableRemoteControlDisableParams` (see tagged/optional type) → `RemoteControlDisableResponse` | Missing relay protocol; not atto /remote |
| `remoteControl/status/read` **E** | `Option<()>` (none) → `RemoteControlStatusReadResponse` | Missing relay protocol; not atto /remote |
| `remoteControl/pairing/start` **E** | `RemoteControlPairingStartParams` (manualCode) → `RemoteControlPairingStartResponse` | Missing relay protocol; not atto /remote |
| `remoteControl/pairing/status` **E** | `RemoteControlPairingStatusParams` (pairingCode, manualPairingCode) → `RemoteControlPairingStatusResponse` | Missing relay protocol; not atto /remote |
| `remoteControl/client/list` **E** | `RemoteControlClientsListParams` (environmentId, cursor, limit, order) → `RemoteControlClientsListResponse` | Missing relay protocol; not atto /remote |
| `remoteControl/client/revoke` **E** | `RemoteControlClientsRevokeParams` (environmentId, clientId) → `RemoteControlClientsRevokeResponse` | Missing relay protocol; not atto /remote |
| `collaborationMode/list` **E** | `CollaborationModeListParams` ({}) → `CollaborationModeListResponse` | Missing |
| `mock/experimentalMethod` **E** | `MockExperimentalMethodParams` (value) → `MockExperimentalMethodResponse` | Missing |
| `environment/add` **E** | `EnvironmentAddParams` (environmentId, execServerUrl, authBearerToken, connectTimeoutMs, skills) → `EnvironmentAddResponse` | Missing |
| `environment/info` **E** | `EnvironmentInfoParams` (environmentId) → `EnvironmentInfoResponse` | Missing |
| `environment/status` **E** | `EnvironmentStatusParams` (environmentId) → `EnvironmentStatusResponse` | Missing |
| `mcpServer/oauth/login` | `McpServerOauthLoginParams` (name, threadId, clientRegistration, scopes, timeoutSecs) → `McpServerOauthLoginResponse` | Missing RPC; internal MCP integration exists |
| `config/mcpServer/reload` | `Option<()>` (none) → `McpServerRefreshResponse` | Missing RPC; not native reload alias |
| `mcpServerStatus/list` | `ListMcpServerStatusParams` (cursor, limit, detail, threadId, serverName) → `ListMcpServerStatusResponse` | Missing RPC; internal MCP integration exists |
| `mcpServer/resource/read` | `McpResourceReadParams` (threadId, originCallId, server, uri, connectorId, target) → `McpResourceReadResponse` | Missing RPC; internal MCP integration exists |
| `mcpServer/event/stream/start` **E** | `McpServerEventStreamStartParams` (threadId, server, subscriptionId, name, arguments, meta) → `McpServerEventStreamStartResponse` | Missing RPC; internal MCP integration exists |
| `mcpServer/event/stream/stop` **E** | `McpServerEventStreamStopParams` (subscriptionId) → `McpServerEventStreamStopResponse` | Missing RPC; internal MCP integration exists |
| `mcpServer/tool/call` | `McpServerToolCallParams` (threadId, server, tool, arguments, meta) → `McpServerToolCallResponse` | Missing RPC; internal MCP integration exists |
| `windowsSandbox/setupStart` | `WindowsSandboxSetupStartParams` (mode, cwd) → `WindowsSandboxSetupStartResponse` | Missing |
| `windowsSandbox/readiness` | `Option<()>` (none) → `WindowsSandboxReadinessResponse` | Missing |
| `account/login/start` | `LoginAccountParams` (see tagged/optional type) → `LoginAccountResponse` | Missing account RPC/service |
| `account/bedrock/discover` **E** | `BedrockDiscoverParams` ({}) → `BedrockDiscoverResponse` | Missing account RPC/service |
| `account/bedrock/setup` **E** | `BedrockSetupParams` (see tagged/optional type) → `BedrockSetupResponse` | Missing account RPC/service |
| `account/bedrock/checkGovCloudRequirements` **E** | `BedrockCheckGovCloudRequirementsParams` ({}) → `BedrockCheckGovCloudRequirementsResponse` | Missing account RPC/service |
| `account/login/cancel` | `CancelLoginAccountParams` (loginId) → `CancelLoginAccountResponse` | Missing account RPC/service |
| `account/logout` | `Option<()>` (none) → `LogoutAccountResponse` | Missing account RPC/service |
| `account/rateLimits/read` | `NullableGetAccountRateLimitsParams` (see tagged/optional type) → `GetAccountRateLimitsResponse` | Missing account RPC/service |
| `account/rateLimitResetCredit/consume` | `ConsumeAccountRateLimitResetCreditParams` (idempotencyKey, creditId) → `ConsumeAccountRateLimitResetCreditResponse` | Missing account RPC/service |
| `account/usage/read` | `NullableGetAccountTokenUsageParams` (see tagged/optional type) → `GetAccountTokenUsageResponse` | Missing account RPC/service |
| `account/workspaceMessages/read` | `Option<()>` (none) → `GetWorkspaceMessagesResponse` | Missing account RPC/service |
| `account/sendAddCreditsNudgeEmail` | `SendAddCreditsNudgeEmailParams` (creditType) → `SendAddCreditsNudgeEmailResponse` | Missing account RPC/service |
| `feedback/upload` | `FeedbackUploadParams` (classification, reason, threadId, includeLogs, extraLogFiles, tags) → `FeedbackUploadResponse` | Missing |
| `command/exec` | `CommandExecParams` (command, processId, tty, streamStdin, streamStdoutStderr, outputBytesCap, …) → `CommandExecResponse` | Missing direct client exec API |
| `command/exec/write` | `CommandExecWriteParams` (processId, deltaBase64, closeStdin) → `CommandExecWriteResponse` | Missing direct client exec API |
| `command/exec/terminate` | `CommandExecTerminateParams` (processId) → `CommandExecTerminateResponse` | Missing direct client exec API |
| `command/exec/resize` | `CommandExecResizeParams` (processId, size) → `CommandExecResizeResponse` | Missing direct client exec API |
| `process/spawn` **E** | `ProcessSpawnParams` (command, processHandle, cwd, tty, streamStdin, streamStdoutStderr, …) → `ProcessSpawnResponse` | Missing PTY process API |
| `process/writeStdin` **E** | `ProcessWriteStdinParams` (processHandle, deltaBase64, closeStdin) → `ProcessWriteStdinResponse` | Missing PTY process API |
| `process/kill` **E** | `ProcessKillParams` (processHandle) → `ProcessKillResponse` | Missing PTY process API |
| `process/resizePty` **E** | `ProcessResizePtyParams` (processHandle, size) → `ProcessResizePtyResponse` | Missing PTY process API |
| `config/read` | `ConfigReadParams` (includeLayers, cwd) → `ConfigReadResponse` | Missing Codex config projection |
| `externalAgentConfig/detect` | `ExternalAgentConfigDetectParams` (includeHome, cwds, maxSessionAgeDays, maxSessions, source, migrationSource) → `ExternalAgentConfigDetectResponse` | Missing |
| `externalAgentConfig/import` | `ExternalAgentConfigImportParams` (migrationItems, source, providerId, migrationSource) → `ExternalAgentConfigImportResponse` | Missing |
| `externalAgentConfig/import/recordHistory` | `ExternalAgentConfigImportHistoryRecordParams` (providerId, itemTypeResults) → `ExternalAgentConfigImportHistoryRecordResponse` | Missing |
| `externalAgentConfig/import/readHistories` | `Option<()>` (none) → `ExternalAgentConfigImportHistoriesReadResponse` | Missing |
| `config/value/write` | `ConfigValueWriteParams` (keyPath, value, mergeStrategy, filePath, expectedVersion) → `ConfigWriteResponse` | Missing Codex config projection |
| `config/batchWrite` | `ConfigBatchWriteParams` (edits, filePath, expectedVersion, reloadUserConfig) → `ConfigWriteResponse` | Missing Codex config projection |
| `configRequirements/read` | `Option<()>` (none) → `ConfigRequirementsReadResponse` | Missing Codex config projection |
| `account/read` | `GetAccountParams` (refreshToken) → `GetAccountResponse` | Missing account RPC/service |
| `getConversationSummary` | `GetConversationSummaryParams` (see tagged/optional type) → `GetConversationSummaryResponse` | Missing |
| `gitDiffToRemote` | `GitDiffToRemoteParams` (cwd) → `GitDiffToRemoteResponse` | Missing |
| `getAuthStatus` | `GetAuthStatusParams` (includeToken, refreshToken) → `GetAuthStatusResponse` | Missing |
| `fuzzyFileSearch` | `FuzzyFileSearchParams` (query, roots, cancellationToken) → `FuzzyFileSearchResponse` | Missing; native local picker is not RPC |
| `fuzzyFileSearch/sessionStart` **E** | `FuzzyFileSearchSessionStartParams` (sessionId, roots) → `FuzzyFileSearchSessionStartResponse` | Missing; native local picker is not RPC |
| `fuzzyFileSearch/sessionUpdate` **E** | `FuzzyFileSearchSessionUpdateParams` (sessionId, query) → `FuzzyFileSearchSessionUpdateResponse` | Missing; native local picker is not RPC |
| `fuzzyFileSearch/sessionStop` **E** | `FuzzyFileSearchSessionStopParams` (sessionId) → `FuzzyFileSearchSessionStopResponse` | Missing; native local picker is not RPC |

### Native atto-only request inventory

| atto method | Codex relation |
|---|---|
| `models/list` | Renamed/reshaped model/list |
| `thread/setModel` | Partial thread/settings/update or sticky turn/start model; return differs |
| `thread/setEffort` | Partial thread/settings/update or sticky turn/start effort; return differs |
| `thread/compact` | Renamed/reshaped thread/compact/start |
| `thread/rollback` | Not the current paginated thread/revert contract |
| `turn/background` | No matching RPC (client PTY background execution is different) |
| `turn/unsteer` | No matching steer-retraction RPC; not typed queue/delete |
| `job/list` | Partial backgroundTerminals/list, but numeric jobs/monitors differ |
| `job/output` | No matching job-tail RPC |
| `job/stop` | Partial backgroundTerminals/terminate; different identity/runtime |
| `subagent/list` | No dedicated matching list; Codex projects subagents as threads/items |
| `subagent/read` | No dedicated matching read; Codex uses thread/read + collab items |
| `prompt/answer` | No matching picker-mirror RPC; server requests are duplex instead |

## Appendix B. Complete Codex server-notification inventory

Method-level experimental markers are shown as **E**. These are **server notifications**, not approval requests. Payload type is the concrete upstream source/schema reference; parentheses sample its first six fields (not exhaustive).

| Codex notification | Payload type | atto mapping |
|---|---|---|
| `error` | `ErrorNotification` (error, willRetry, threadId, turnId) | Partial failed-turn/reload errors; typed payload differs |
| `thread/started` | `ThreadStartedNotification` (thread) | Missing; adapter must synthesize |
| `thread/status/changed` | `ThreadStatusChangedNotification` (threadId, status) | Partial live thread/updated or inferred standalone state |
| `thread/archived` | `ThreadArchivedNotification` (threadId) | Missing |
| `thread/deleted` | `ThreadDeletedNotification` (threadId) | Missing |
| `thread/unarchived` | `ThreadUnarchivedNotification` (threadId) | Missing |
| `thread/closed` | `ThreadClosedNotification` (threadId) | Missing |
| `thread/reverted` | `ThreadRevertedNotification` (threadId) | Missing |
| `skills/changed` | `SkillsChangedNotification` ({}) | Missing |
| `thread/name/updated` | `ThreadNameUpdatedNotification` (threadId, threadName) | Partial live thread/updated; separate name payload |
| `thread/attachment/updated` | `ThreadAttachmentUpdatedNotification` (threadId, attachmentType, identityKey, attachmentId, operation) | Missing |
| `thread/goal/updated` | `ThreadGoalUpdatedNotification` (threadId, turnId, goal) | Partial live goal/updated; goal shape differs |
| `thread/prediction/updated` **E** | `ThreadPredictionUpdatedNotification` (threadId, sourceTurnId, result) | Missing |
| `thread/goal/cleared` | `ThreadGoalClearedNotification` (threadId) | Partial live goal/updated:null |
| `thread/queue/changed` **E** | `ThreadQueueChangedNotification` (threadId) | Partial turn/pending; typed queue IDs missing |
| `project/changed` **E** | `ProjectChangedNotification` (projectId, changeType) | Missing |
| `thread/project/updated` **E** | `ThreadProjectUpdatedNotification` (threadId, projectId) | Missing |
| `thread/environment/connected` **E** | `EnvironmentConnectionNotification` (threadId, environmentId) | Missing |
| `thread/environment/disconnected` **E** | `EnvironmentConnectionNotification` (threadId, environmentId) | Missing |
| `thread/settings/updated` **E** | `ThreadSettingsUpdatedNotification` (threadId, threadSettings) | Partial live thread/updated; settings shape differs |
| `thread/tokenUsage/updated` | `ThreadTokenUsageUpdatedNotification` (threadId, turnId, tokenUsage) | Renamed/reshaped thread/usage |
| `turn/started` | `TurnStartedNotification` (threadId, turn) | Shape differs: nested turn (§4) |
| `hook/started` | `HookStartedNotification` (threadId, turnId, run) | Missing HookRun start; native hook notice only |
| `turn/completed` | `TurnCompletedNotification` (threadId, turn) | Shape differs: nested turn/error (§4) |
| `hook/completed` | `HookCompletedNotification` (threadId, turnId, run) | Partial native hook notice; HookRun shape missing |
| `turn/diff/updated` | `TurnDiffUpdatedNotification` (threadId, turnId, diff) | Missing |
| `turn/plan/updated` | `TurnPlanUpdatedNotification` (threadId, turnId, explanation, plan) | Missing |
| `item/started` | `ItemStartedNotification` (item, threadId, turnId, startedAtMs) | Same outer keys; item/IDs differ |
| `item/autoApprovalReview/started` | `ItemGuardianApprovalReviewStartedNotification` (threadId, turnId, startedAtMs, reviewId, targetItemId, review, …) | Missing |
| `item/autoApprovalReview/completed` | `ItemGuardianApprovalReviewCompletedNotification` (threadId, turnId, startedAtMs, completedAtMs, reviewId, targetItemId, …) | Missing |
| `autoApprovalReview/strictReviewRequired` **E** | `StrictReviewRequiredNotification` (threadId, turnId, startedAtMs) | Missing |
| `item/completed` | `ItemCompletedNotification` (item, threadId, turnId, completedAtMs) | Same outer keys; item/IDs differ |
| `rawResponseItem/completed` | `RawResponseItemCompletedNotification` (threadId, turnId, item) | Missing |
| `rawResponse/completed` | `RawResponseCompletedNotification` (threadId, turnId, responseId, usage, usageMetadata) | Missing |
| `item/agentMessage/delta` | `AgentMessageDeltaNotification` (threadId, turnId, itemId, delta) | Renamed generic item/delta; dispatch by type |
| `item/plan/delta` | `PlanDeltaNotification` (threadId, turnId, itemId, delta) | Missing |
| `command/exec/outputDelta` | `CommandExecOutputDeltaNotification` (processId, stream, deltaBase64, capReached) | Missing |
| `process/outputDelta` **E** | `ProcessOutputDeltaNotification` (processHandle, stream, deltaBase64, capReached) | Missing |
| `process/exited` **E** | `ProcessExitedNotification` (processHandle, exitCode, stdout, stdoutCapReached, stderr, stderrCapReached) | Missing |
| `item/commandExecution/outputDelta` | `CommandExecutionOutputDeltaNotification` (threadId, turnId, itemId, delta) | Renamed generic item/delta for stdout only |
| `item/commandExecution/terminalInteraction` | `TerminalInteractionNotification` (threadId, turnId, itemId, processId, stdin) | Missing |
| `item/fileChange/outputDelta` | `FileChangeOutputDeltaNotification` (threadId, turnId, itemId, delta) | Missing |
| `item/fileChange/patchUpdated` | `FileChangePatchUpdatedNotification` (threadId, turnId, itemId, changes) | Missing |
| `serverRequest/resolved` | `ServerRequestResolvedNotification` (threadId, requestId) | Missing |
| `item/mcpToolCall/progress` | `McpToolCallProgressNotification` (threadId, turnId, itemId, message) | Missing |
| `mcpServer/oauthLogin/completed` | `McpServerOauthLoginCompletedNotification` (name, threadId, loginId, success, error) | Missing |
| `mcpServer/startupStatus/updated` | `McpServerStatusUpdatedNotification` (threadId, name, status, error, failureReason) | Missing |
| `mcpServer/event/stream/notification` **E** | `McpServerEventStreamNotification` (subscriptionId, notification) | Missing |
| `account/updated` | `AccountUpdatedNotification` (authMode, planType) | Missing |
| `account/gatewayOAuth/changed` | `GatewayOAuthChangedNotification` (authUrl, providerId, status, error) | Missing |
| `account/rateLimits/updated` | `AccountRateLimitsUpdatedNotification` (rateLimits) | Missing |
| `app/list/updated` | `AppListUpdatedNotification` (data) | Missing |
| `remoteControl/status/changed` | `RemoteControlStatusChangedNotification` (status, serverName, installationId, environmentId) | Missing |
| `externalAgentConfig/import/progress` | `ExternalAgentConfigImportProgressNotification` (importId, itemTypeResults) | Missing |
| `externalAgentConfig/import/completed` | `ExternalAgentConfigImportCompletedNotification` (importId, itemTypeResults) | Missing |
| `fs/changed` | `FsChangedNotification` (watchId, changedPaths) | Missing |
| `item/reasoning/summaryTextDelta` | `ReasoningSummaryTextDeltaNotification` (threadId, turnId, itemId, delta, summaryIndex) | Generic item/delta; summary indexing missing |
| `item/reasoning/summaryPartAdded` | `ReasoningSummaryPartAddedNotification` (threadId, turnId, itemId, summaryIndex) | Missing part lifecycle; adapter can synthesize |
| `item/reasoning/textDelta` | `ReasoningTextDeltaNotification` (threadId, turnId, itemId, delta, contentIndex) | Generic item/delta; content indexing missing |
| `thread/compacted` | `ContextCompactedNotification` (threadId, turnId) | Deprecated; derive from compaction item |
| `model/rerouted` | `ModelReroutedNotification` (threadId, turnId, fromModel, toModel, reason) | Missing |
| `model/verification` | `ModelVerificationNotification` (threadId, turnId, verifications) | Missing |
| `modelProvider/authRecoveryStarted` | `AuthRecoveryNotification` (threadId, turnId, provider, message) | Missing |
| `modelProvider/authRecoveryCompleted` | `AuthRecoveryNotification` (threadId, turnId, provider, message) | Missing |
| `turn/moderationMetadata` **E** | `TurnModerationMetadataNotification` (threadId, turnId, metadata) | Missing |
| `model/safetyBuffering/updated` | `ModelSafetyBufferingUpdatedNotification` (threadId, turnId, model, useCases, reasons, showBufferingUi, …) | Missing |
| `warning` | `WarningNotification` (threadId, message) | Partial native notices; warning shape differs |
| `guardianWarning` | `GuardianWarningNotification` (threadId, message) | Missing |
| `deprecationNotice` | `DeprecationNoticeNotification` (summary, details) | Missing |
| `configWarning` | `ConfigWarningNotification` (summary, details, path, range) | Partial reload/hook warning; shape differs |
| `fuzzyFileSearch/sessionUpdated` | `FuzzyFileSearchSessionUpdatedNotification` (sessionId, query, files) | Missing |
| `fuzzyFileSearch/sessionCompleted` | `FuzzyFileSearchSessionCompletedNotification` (sessionId) | Missing |
| `thread/realtime/started` **E** | `ThreadRealtimeStartedNotification` (threadId, realtimeSessionId, version) | Missing |
| `thread/realtime/itemAdded` **E** | `ThreadRealtimeItemAddedNotification` (threadId, item) | Missing |
| `thread/realtime/item/started` **E** | `ThreadRealtimeItemStartedNotification` (threadId, item) | Missing |
| `thread/realtime/item/transcript/delta` **E** | `ThreadRealtimeItemTranscriptDeltaNotification` (threadId, itemId, delta) | Missing |
| `thread/realtime/item/completed` **E** | `ThreadRealtimeItemCompletedNotification` (threadId, item) | Missing |
| `thread/realtime/transcript/delta` **E** | `ThreadRealtimeTranscriptDeltaNotification` (threadId, role, delta) | Missing |
| `thread/realtime/transcript/done` **E** | `ThreadRealtimeTranscriptDoneNotification` (threadId, role, text) | Missing |
| `thread/realtime/outputAudio/delta` **E** | `ThreadRealtimeOutputAudioDeltaNotification` (threadId, audio) | Missing |
| `thread/realtime/sdp` **E** | `ThreadRealtimeSdpNotification` (threadId, sdp) | Missing |
| `thread/realtime/error` **E** | `ThreadRealtimeErrorNotification` (threadId, message) | Missing |
| `thread/realtime/closed` **E** | `ThreadRealtimeClosedNotification` (threadId, reason) | Missing |
| `windows/worldWritableWarning` | `WindowsWorldWritableWarningNotification` (samplePaths, extraCount, failedScan) | Missing |
| `windowsSandbox/setupCompleted` | `WindowsSandboxSetupCompletedNotification` (mode, success, error) | Missing |
| `account/login/completed` | `AccountLoginCompletedNotification` (loginId, success, error, onboardingEntrypoint) | Missing |

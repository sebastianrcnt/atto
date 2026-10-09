# atto Swing desktop

A Java 21 desktop client for **atto's native app-server protocol**, not a Codex
wire adapter. The JDK is the only Java dependency: no Maven, Gradle, downloaded
libraries, embedded web view, or frontend execution engine.

## Build and run

```sh
clients/swing/build.sh
java -jar clients/swing/build/atto-swing.jar

# An explicit binary and workspace; default transport is JSON lines on stdio.
java -jar clients/swing/build/atto-swing.jar --atto /path/to/atto --cwd /work/project

# Join a saved session, or open more sessions using the sidebar.
java -jar clients/swing/build/atto-swing.jar --atto /path/to/atto 1234abcd

# Connect to an existing listener. Nothing is spawned in this mode.
atto app-server --listen ws://127.0.0.1:7878
java -jar clients/swing/build/atto-swing.jar --connect ws://127.0.0.1:7878

# atto serve requires its token; use its /ws endpoint.
java -jar clients/swing/build/atto-swing.jar --connect ws://host:7878/ws --token TOKEN

atto app-server --listen unix:///tmp/atto.sock
java -jar clients/swing/build/atto-swing.jar --connect unix:///tmp/atto.sock
```

`atto` is found on PATH unless `--atto PATH` is supplied. `--in-process` passes
that flag to a spawned app-server (useful without a daemon); it is not a Java
execution mode. `--cwd DIR` is the subprocess's initial working directory and
the initial new session's workspace. A new session dialog accepts a directory
**on the server**, which need not be a path accessible to the desktop.

No-argument launches remember the last endpoint and binary. An explicit
`--atto atto` selects stdio again. Tokens, URL queries, fragments and URL user
credentials are **never saved**; provide `--token` again for an authenticated
endpoint. Layout, theme and transcript/composer font size are saved only in
`~/.atto/swing.json`, never in atto's runtime settings. `"sound":true` in that
client file enables the optional unfocused-window notification sound.

On Windows, `build.sh` can run in Git Bash with JDK 21 on PATH. Unix listeners
are skipped by the Windows integration test. WebSocket (`ws://` and `wss://`)
and stdio use JDK facilities on every platform.

## Daily work

- Searchable session sidebar with names, cwd, timestamps and live/busy state;
  several independent session tabs. New, resume, rename, fork, archive, detach
  and explicit close are available from the Session menu or command palette.
- Transcript rows update in place, batching streams at 20fps. Initially only
  the latest 200 items have Swing components; **Load earlier messages** adds
  another page. User scrolling stops following; **Jump to bottom** resumes it.
- Assistant Markdown supports headings, lists, quotes, inline code/emphasis,
  links and fenced code with copy buttons. Reasoning is collapsed. Commands
  show command, status, duration, exit/background details and a clipped output
  tail, with expansion and a full available-output viewer. Notices, compactions,
  goals, branch summaries and extension text/display replacements are preserved.
  `/diff`'s extension text and diff code blocks have colored additions/deletions.
- Native user/tool images show PNG thumbnails (including server-side WebP
  conversion) and an image viewer with **Save original**. Attach files, drop
  files/images onto the composer, paste an image, or use the attachment action.
- Pending steer/queue items appear above the composer, with **Edit / take back**
  and **Remove**. Images recovered by the runtime belong to the originating
  client rather than being inserted into every editor.
- Runtime prompts are non-modal and first-answer-wins. Select, confirm, input
  and multi-select are supported; a peer's answer withdraws the dialog. Local
  pickers use balanced runtime gates so automatic queue/goal work waits.
- Filterable model/provider/context-window picker and effort picker; provider
  authentication status, masked API-key entry and browser OAuth with redirect
  paste. Credentials are stored by the **server**, not the desktop.
- Session tree with jump, labels, optional automatic/custom branch summaries,
  and fork; a saved transcript message also has a fork/label context menu.
- Goal objective/state/usage/notes, background-job list/output follow/stop,
  timers, observational agents, context/system breakdown, compaction/reload,
  request export, server heap/goroutine/memory/request-history diagnostics and
  usage totals. These all use native RPCs, not shell substitutes.
- Built-in status shows model/effort, context usage, cache percentage, in/out
  tokens, cost, activity/elapsed time, jobs/timers and cwd. Configured custom
  statusLine output plus extension statuses/widgets have additional rows.
  An unfocused window gets a title badge and OS attention on turn completion
  or a waiting prompt.

Every runtime slash command (including registered extension and skill commands)
is submitted as typed. Client-local commands open desktop controls: `/model`,
`/effort`, `/copy`, `/context`, `/goal`, `/tree`, `/fork`, `/resume`, `/sessions`,
`/jobs`, `/timers`, `/agents`, `/name`, `/archive`, `/clear`, `/new`, `/request`,
`/debug`, `/login`, `/logout`, `/detach`, `/close`, `/quit` and `/exit`.
`/goal set OBJECTIVE` is also accepted; `/goal OBJECTIVE` is the native spelling.
Slash completion uses the live command catalog, and `@file` completion uses
server workspace discovery, including remote workspaces. Ctrl+Space or the
**Complete** button opens completion for the current prefix.

Terminal-only `/tui` renderer modes and `/remote` listener/QR management do not
apply to Swing: they explain the desktop theme controls and listener setup
rather than pretending to execute those TUI-owned actions. Run app-server or
serve with a listener to attach other clients to the same daemon session.

## Keyboard

“Cmd/Ctrl” means Command on macOS and Control elsewhere. Every menu action and
slash command is also reachable through the searchable command palette.

| Shortcut | Action |
| --- | --- |
| Enter | Send; steer while a model turn is busy |
| Shift+Enter | Newline |
| Tab or Cmd/Ctrl+Q | Queue composer input |
| Cmd/Ctrl+Enter | Interrupt and send now |
| Shift+Left / Alt+Up | Take back the last pending steer, otherwise queue item |
| Esc | User interrupt; a hosted running command becomes a background job |
| Cmd/Ctrl+B | Background the current command |
| Cmd/Ctrl+K | Command palette |
| Ctrl+Space | Complete slash command / @file prefix |
| Cmd/Ctrl+N / O | New / resume session |
| Cmd/Ctrl+W | Detach selected tab |
| Cmd/Ctrl+Shift+W | Close session and stop its work (confirmation) |
| Cmd/Ctrl+[ / ] | Previous / next session tab |
| Cmd/Ctrl+M / E | Model / effort |
| Cmd/Ctrl+T / J / G / I | Tree / jobs / goal / context |
| Cmd/Ctrl+Shift+T / A | Timers / agents |
| Cmd/Ctrl+Shift+C | Copy last answer |
| Cmd/Ctrl+U / Shift+V | Attach / paste image |
| Cmd/Ctrl+L / End | Focus composer / jump to bottom |
| Cmd/Ctrl++ / - | Transcript/composer font size |
| Ctrl+Tab | Move focus out of the composer |

Hard cancel, reload, compact, diagnostics, authentication and remaining actions
are in the palette. Picker Enter selects; Escape closes; lists and trees support
normal Swing keyboard navigation.

## Lifetime, trust and reconnect

Closing a tab/window **detaches**, rather than sending thread/close. Daemon
workers keep executing without the desktop. **Close session** and `/close`
explicitly stop a session and its jobs; archive closes before moving the saved
file. With `--in-process`, ending the spawned **server process** necessarily
ends its runtimes; it cannot provide daemon-backed unattended lifetime.

The desktop defers new/resumed startup, displays loaded context, and asks before
releasing startup hooks/MCP work. Runtime extension/MCP approvals remain native
questions; disconnect never supplies an approval automatically. A session whose
legacy writer is elsewhere is read from disk and shown read-only, not taken over.

Disconnect fails pending requests, retries after two seconds, initializes again,
and rehydrates attached sessions from fresh snapshots. Notification cursors and
item IDs prevent double-appended text. **Accepted inputs are never automatically
resent**: a lost reply does not establish that the server rejected the input.
Inspect the transcript before resending a recovered draft. Reconnect and explicit
session resume work over all three transports.

Use bearer-authenticated TLS/private-network endpoints for remote access. Plain
WS has no transport encryption. Runtime requests, exported profiles and saved
outputs may contain private workspace/provider data; choose export locations
accordingly. Raw Markdown HTML and arbitrary remote images are not executed or
fetched. Native transcript image resources are separate from Markdown links.

## Screenshots to capture

The intended macOS review screenshots are:

1. **Main workspace:** live searchable sidebar, two session tabs, assistant
   heading/list/code block, collapsed reasoning and streamed command output;
   the composer with a queued message and full status/extension rows.
2. **Tree and goal:** session tree with branches/labels and a goal panel showing
   objective, status, tokens and notes.
3. **Approval and jobs:** a non-modal runtime question alongside a background
   job/output follow panel; show its withdrawal after a second client answers.
4. **Dark theme and images:** dark UI with user/tool image thumbnails, colored
   `/diff`, attached composer images and the command palette.

No synthetic screenshots are checked in; the real Swing UI should be captured
on the reviewer's desktop, rather than substituting a browser mock-up.

## Tests and architecture

```sh
clients/swing/build.sh test
# --selftest is for the deterministic providertest fake/m fixture only,
# not live provider credentials. The Go test below installs that fixture:
java -Djava.awt.headless=true -jar clients/swing/build/atto-swing.jar \
  --selftest --atto /path/to/atto --in-process --cwd /tmp/work

go test ./clients/swing -count=1
```

`Json`, `Transport`, `Protocol`, `ThreadState` and `Core` contain no Swing
references. Request futures/notification dispatch, writes, connection setup,
reconnect and resource I/O are off the EDT. Immutable copy-on-write projections
are batched before `Desktop`, `SessionPane`, `Transcript` and `Panels` apply them
on the EDT. Neither view code nor the reducer owns model/tool execution.

The tiny Java runner covers codec errors/precision/Unicode, cursor boundaries,
stream replacement, duplicates/out-of-order events, late provenance, snapshot
races, branch reset, detach during hydration, clipping, Markdown safety, request
ID matching, errors, disconnect/reconnect and fragmented UTF-8 JSON lines.
Go integration builds the jar and real atto, runs a scripted provider, and checks
streamed text, steer, queue/takeback and executed follow-up, hosted-shell
interrupt/job output (Unix), prompt answer, slash command, model setting,
user shell, fork/resume, detach/reattach, actual transport reconnect and saved
session contents. It covers stdio, Unix, WS and token-authenticated serve WS;
without java/javac/jar 21 it skips cleanly. The build uses temporary test output,
not tracked artifacts or external Java downloads.

Native additions are documented in [docs/protocol.md](../../docs/protocol.md):
workspace discovery, transcript image/output resources (including offline and
PNG previews), runtime authentication, archive, statusLine and debug profiles,
multi-select prompt answers, user entry provenance and resumable fork IDs.

### Deliberate limits

Markdown is a small safe renderer, not full CommonMark (tables/raw HTML are not
rendered as rich content). Very long text/output is clipped in the inline view;
the full viewer exposes only bytes still retained by the runtime or its output
file. Job output follows the protocol's 2,000-line cap. The observational agent
picker shows named agent states/reports; it does not become an execution owner or
move agents between runtimes. Native input/prompt/job persistence still has the
server's documented worker-crash limitations. OAuth itself requires the real
provider/browser; protocol tests use a deterministic mocked flow, not a paid
provider account. Real GUI layout/theme/OS-attention behavior needs the manual
macOS screenshot review; the automated tests are deliberately headless.

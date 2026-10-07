# atto2

atto2 is the next atto, developed on the `atto2` branch: every session
runs in a **session runtime** that any number of clients follow over
atto's protocol, instead of inside the terminal that started it. It is
installed beside atto, apart from it, until it replaces it.

atto2 has **no terminal UI**. The TUI (packages `app` and `tui`, the
daemon's PTY panes, `atto attach`, `connect`, `agents`, `resume` and
interactive `atto`) was removed; it is in git history up to 1a9dc7a. Its
user interface is a separate desktop client (Kotlin, Swing) that speaks
atto's protocol, to be built.

## Install

```sh
scripts/install-atto2.sh        # builds ~/.atto2/bin/atto and the atto2 wrapper
atto2 login                     # auth.json is not copied from ~/.atto
```

`atto2` sets `ATTO_DIR=~/.atto2`, so its settings, sessions, daemon and
workers never meet atto's. Rebuild with the same script; `go install`
would replace atto instead.

## Shape

| Name | What it is | Where |
|---|---|---|
| runtime | runs a session: the agent loop, queue/steer/interrupt, goals, extensions, hooks, MCP, jobs and timers, the session file | package `server` (`runtime.go`, `run.go`, `goal.go`, …) |
| worker | a process running one session's runtime; it alone writes the session | `atto _session-server`, started by the daemon |
| daemon | starts, lists and stops workers; nothing else | package `daemon`, `atto _daemon` |
| app-server | the protocol and its transports: stdio, HTTP, the worker socket | `server/protocol.go`, `atto app-server`, `atto serve`, `remote/start` |
| clients | the desktop client (Swing, to come), the web client, `atto -p` | separate repository, `server/web`, `cli` |

- Clients send requests and draw what the runtime's notifications say;
  every client of a session sees the same transcript, queue and state.
- Leaving a client detaches: the session goes on. An idle session with no
  client retires after `sessionRetention` (default 1m, as codex unloads
  threads after 60s); `thread/close` ends it now.
- A worker that dies is lost with the turn it was running; the session
  file is not. Starting it again is the client's job (the TUI did it).

The design, decisions and status of each step are in
[tui-as-client.md](tui-as-client.md).

## Direction

The terminal UI made every step harder (most of the bugs found using
atto2 were the TUI's), so atto2 dropped it and puts the runtime first: the
desktop client comes last, as a client of a finished protocol.

1. Subagents run as workers too (live views, crash recovery).
2. `atto -p`, `atto serve`, `atto app-server` and `remote/start` all go
   through workers: one way to run a session. Today only `atto -p
   -session` on a session a worker already runs does; nothing else
   starts workers since the TUI went.
3. The protocol covers what the TUI did locally or read from files (the
   list below); requests are deduplicated.
4. Queued input survives a worker crash.
5. The desktop client, on the protocol alone.

What the TUI did on its own that a client now needs from the protocol:

- **Opening a session in a worker.** The TUI asked the daemon (a Go
  frame protocol on a Unix socket) for a session's worker and connected
  to its socket. A desktop client needs that through atto's protocol: an
  `atto app-server` that bridges to the daemon's workers (start or find
  by session, restart a dead one), or the daemon speaking JSON-RPC.
- **Sign-in.** `/login` and `/logout` were the TUI's pickers (OAuth in a
  browser with a pasted redirect or device code, API keys). `atto login`
  and `atto auth set` still work from a shell; a client needs `auth/*`
  methods (providers and their status, start an OAuth flow, report its
  URL or device code, accept a pasted redirect, save a key, log out).
- **Sessions and the tree.** The resume picker, `/sessions` (rename,
  archive, preview), the agent center (running and saved sessions,
  subagent trees) and the `/tree` view read session files directly;
  the protocol has `thread/list` and `subagent/*` but no `thread/tree`,
  `sessions/changed` or worker registry listing.
- **Workspace.** `@` file mentions listed the project's files
  (`fsutil.ListFiles`, kept for this) and pasted image paths and the
  clipboard were read locally; a remote client needs `workspace/files`
  and image upload (`turn/start` images, or `image/read` for the store).
- **Client-side commands.** `/copy`, `/request` and `/debug` (the raw
  last requests, kept in the worker's memory: `ai.RecentRequests`), the
  custom `statusLine` command (removed with the setting), the startup
  update notice (`atto update -check`), and preferences the TUI read
  from settings.json (double-Esc action, branch-summary prompt, spinner
  words); initialize's `settings` is where clients get such preferences.

Until then atto2 is used through `atto2 -p`, the web client and
atto-desktop; atto stays the everyday terminal.

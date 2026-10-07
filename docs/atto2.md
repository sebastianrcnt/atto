# atto2

atto2 is the next atto, developed on the `atto2` branch: every session
runs in a **session runtime** that any number of clients follow over
atto's protocol, instead of inside the terminal that started it. It is
installed beside atto, apart from it, until it replaces it.

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
| daemon | starts and retires workers (and, for now, holds terminal panes) | package `daemon`, `atto _daemon` |
| app-server | the protocol and its transports: stdio, HTTP, the worker socket | `server/protocol.go`, `atto app-server`, `atto serve`, `/remote` |
| clients | the TUI, `atto -p`, the web client, atto-desktop | `app`, `cli`, `server/web` |

- Clients send requests and draw what the runtime's notifications say;
  every client of a session sees the same transcript, queue and state.
- Leaving a client detaches: the session goes on. An idle session with no
  client retires after `sessionRetention` (default 1m, as codex unloads
  threads after 60s); `/close` ends it now.
- A worker that dies is started again by the client that saw it go; the
  turn it was running is lost, the session file is not.

The design, decisions and status of each step are in
[tui-as-client.md](tui-as-client.md).

## Direction

The terminal UI made every step harder (most of the bugs found using
atto2 were the TUI's), so atto2 puts the runtime first and builds the TUI
last, as a thin client of a finished protocol:

1. Subagents run as workers too (live views, crash recovery).
2. `atto -p`, `atto serve` and `/remote` all go through workers: one way
   to run a session.
3. The protocol covers what clients still read from files: session lists,
   the tree, rename and archive, agents; requests are deduplicated.
4. Queued input survives a worker crash.
5. A new TUI on the protocol alone.

Until then atto2 is used through `atto2 -p`, the web client and
atto-desktop; atto stays the everyday terminal.

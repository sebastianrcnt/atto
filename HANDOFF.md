# Handoff — 2026-10-09 (night)

State when this was written: `main` = `a59521f` + this handoff + a CRLF test fix (pushed; CI run 37855272565 green on all platforms); the locally installed `atto` was built from it. Nothing is running;
agents `/root/split2`, `/root/split3` and `/root/split4` are closed and their branches deleted. Open issues: none from tonight.

## Engine / front-end split (done tonight)

The user's request: separate execution from front ends, so any client (TUI, web, others) attaches and detaches and atto can run
without its TUI, the way Codex's app-server does. Design and per-phase status: `docs/tui-as-client.md`. Protocol reference for
client authors: `docs/protocol.md`. Reference implementation: `archive/atto2` (`1a9dc7a`).

| Phase | Commits | What |
|---|---|---|
| 1 contract + runtime | `177199d`, `2a3af83`, `0086a01` | protocol revisions (`protocolVersions`, Codex-shaped `initialize`/`initialized`, `data.reason`); one event hub for every transport; `server.Client`/`Connect`; the session runtime (`server/runtime.go` and friends) owns execution: input, steer, queue, interrupts (#32 semantics), goals, prompts, shell, tree, inbox |
| 2 TUI as client | `c4a881f`, `e43c781`, `59b30f8` | App owns no agent/writer/hooks/extensions/MCP/goal/inbox; it renders runtime notifications. `server.Live` is gone; `/remote` is `server.Scope` over the same runtime; the frozen web client is unchanged |
| 3 workers | `d773cfc`, `2b138cb`, `7a9f540` | with the daemon each session runs in a worker (`atto _session-server`, unix socket); the TUI, panes, `atto connect`, app-server, serve, `/remote` and `-p` on a live session are its clients. Exit/Ctrl+D detaches; an unattended idle worker retires after 1 min; `/close` ends it; crash → reconnect. `ATTO_NO_DAEMON=1`/Windows: in-process as before |
| 4 transports + docs | `4b6a948`, `d4fcfae`, `a59521f` | `atto app-server --listen stdio:// \| unix:///path \| ws://IP:PORT` (hand-written RFC 6455, bearer token off loopback, Origin check, `--allow-origin`); `atto serve` has `/ws`; `docs/protocol.md` (tests keep it in step with the dispatcher); `examples/clients/stdio.py` and `index.html` |

Fixes made while verifying (direct commits): `shutdown` read thread state outside the UI lock (race); test races; `TestAgentRefusals`
flake; extension watchdog test flake; **an old daemon left running across an upgrade**: the new binary now runs in-process while
it runs (`daemon.Usable`) and still lists/attaches/kills/stops its panes (pane ops downgrade the protocol; execution ops never do);
SIGTERM shuts app-server/serve down cleanly.

Verified live (isolated `ATTO_DIR`, real model) for every phase: `-p`, `-p` with a 12 s command, app-server turns, TUI with and
without the daemon (`scratchpad/smoke.sh`), steer, Esc detaching a running command into a job, `!`, `/jobs`, `/context`, `/model`,
`/clear` + `/resume`, `/remote` RPC+SSE; detach mid-turn then `atto connect`; app-server `thread/resume` of the TUI's session with
the turn appearing in the TUI; 1-minute retirement; `/close`; `kill -9` of a worker → "Reconnected."; ws turn with and without token;
unix socket 0600 and removed on exit. CI is green after every phase.

## Things to know

- **The user's real daemon is still the old version** (protocol 2, several panes). Until it stops, new `atto` prints
  "running without the daemon" and runs in-process; `atto attach` still reaches the old panes. After closing those panes,
  `atto daemon stop -force` switches to workers.
- **Behaviour changes on purpose:** switching away from a busy session (/clear, /resume, /new) no longer cancels it; in daemon
  mode exit detaches instead of ending the session (use `/close`).
- **Known limits** (documented in tui-as-client.md): a crashed worker restores only saved session state (unsaved queued input
  and in-flight extension prompts are lost); the daemon's worker registry is in memory (workers orphaned by a daemon crash are not
  adopted; leases still prevent a second writer); no durable input journal; no full Codex-dialect adapter; WS reconnect is
  snapshot-based.
- **`atto agent` execution was deliberately not moved** onto workers (phase H). It is used constantly; move it only with care.
- A fresh `ATTO_DIR` picks the first available model until the catalog cache exists (old behaviour, not a regression).
- VMs (linuxvm, winvm) are down; Windows coverage is `GOOS=windows go vet` plus CI.

## Not done / open decisions

1. Phase H: `atto agent spawn/task/wait` through workers; durable accepted-input journaling and request dedupe.
2. `-m` / `enabled` restrictions for `atto agent`; request log "all" mode; macOS route-change pool reset (all still undecided).
3. Lazy transcript display loading for huge sessions (the display path still loads the whole active branch).

## How the work is done

- Implementation goes to `atto agent spawn NAME "<brief>" -worktree -m openai/gpt-6.1-sol -effort high`, run from a plain shell.
  Briefs live in `/tmp/atto-runs/` (`common2.txt`, `w12_common.txt`, `w12_phase*.txt`). gpt-6.1-sol tends to stop after part of a
  brief: end briefs with "you alone do all of it in this turn".
- Before merging: full checks, `-race` on touched packages, a build in the scratchpad, `smoke.sh BINARY` (SMOKE OK), then live
  checks of what changed.
- Sources of truth: the GitHub issues and the memory directory `~/.claude/projects/-Volumes-t5-atto/memory/`.

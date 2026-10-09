# Handoff — 2026-10-09 (evening)

State when this was written: `main` = `0036514` + this handoff, pushed; the locally installed `atto` (`go install -ldflags="-s -w"`,
26 MB) is built from it, and `win` runs the same build. One implementation is running: the agent model (below), in a Claude
Code Sonnet worktree. CI is nightly (03:00 KST): today's changes get their first Windows/Linux run tonight.

A picture of the whole structure (concepts, processes, memory, agents, commands, status): https://claude.ai/artifact/3KkHWQiWTpwHshzVh2oxMc

## Structure in one paragraph

Five concepts. A **session** is a JSONL file (the source of truth). A **worker** (`atto _session-server`) executes one session.
The **daemon** (`atto _daemon`) only starts, lists and retires workers; it holds no screens. **Clients** (TUI,
`app-server`, `-p`, scripts) attach to workers over JSON-RPC and only render. An **agent** is a session started by another
session; its identity is its session ID and its tree position is metadata. Without the daemon (Windows, `ATTO_NO_DAEMON=1`)
the worker runs inside the client process. Keep this separation strict: clients never read `~/.atto` or the daemon directly.
The Swing and web clients are gone (tags `archive/swing` and `archive/web-frozen`); protocol revision 3 is the only one
served, and `atto app-server --listen stdio:// | unix:// | ws://` is how other clients attach. `/remote` and `atto serve`
only print that the web UI is being rebuilt.

## Done today (2026-10-09)

| What | Commits | Notes |
|---|---|---|
| Engine/front-end split (overnight) | `177199d` … `a59521f` | runtime owns execution, workers, `app-server --listen stdio/unix/ws`; `docs/tui-as-client.md`, `docs/protocol.md` |
| Swing client, then removed | `7098497` … `ec9a016` | `clients/swing`, JDK 21 only; deleted the same day, tag `archive/swing` |
| Web and Swing removal | the commits after `9dd33fa` | `clients/swing`, `server/web`, the HTTP/SSE gateway, `server.Scope` and `/remote` plumbing, `subagent/*` aliases, protocol revisions 1 and 2 with full snapshots, `remote.port`; tags `archive/swing`, `archive/web-frozen`; `/remote` and `atto serve` print a one-line pointer to `app-server` |
| PTY panes removed | `c39c0f9`, `90e5da7`, `8eaba18` | daemon protocol 4, workers only; commands are `atto` and `atto resume [ID\|name]`; `attach`, `connect`, `/detach`, `-c`, `-resume` removed (pointer message); `atto daemon kill SESSION` |
| Archived sessions zstd | `f258d85`, `9879a0b`, `2013169` | `archived_sessions/*.jsonl.zst`; `atto sessions compress` migrated 24 → 8.4 MB here |
| Agent `@ID` addresses, no shared outside parent | `fb92f37` … `a8b4023` | being superseded by the agent model work |
| Command output to disk | `29df39a` … `0965dbd` | `outputs/<session>/*.log.zst`, head/tail 32 MB caps, 1 GB total, `atto output`; deleted with the session |
| Test temp-home leak | `f97b033` | `internal/testhome`; tests had left 12 GB of read-only Go module caches in TMPDIR (cleaned) |
| Backup / restore / clean / uninstall | `ffaca38`, `537c952` | `maint/`; backup excludes secrets unless `-with-secrets` |
| `desktop.go` split, POSIX sh prompt | `27c2a25`, `61a6017` | bash prompt text byte-identical |
| Slim build | `5b81abc`, `33c62e7`, `15b375d` | `-tags noext`, 16 MB vs 26 MB; `/diff` and `/autorename` ported to Go; releases ship `atto-slim_*`; `atto update -variant` |
| Memory | `94298a8` … `83c8be2` | protocol 3 tail snapshots + `thread/items` paging; unattached workers hold no display items; real 62 MB session: worker 653 → 45 MB, TUI 295 → 27 MB; `docs/session-memory.md` |
| Design docs | `6de440c`, `05aecea`, `5725da4`, `fb47b91`, `0036514` | `docs/agent-model.md` (decided), `docs/session-segments.md` (on hold) |

Every merge: full tests (both build tags), `-race`, Windows vet, `smoke.sh`, plus live checks of the change with a real model in
an isolated `ATTO_DIR`.

## In progress

**Agent model** (Sonnet worktree under `.claude/worktrees/`, brief `/tmp/atto-runs/w18_agentmodel_impl.txt`, spec
`docs/agent-model.md`): agent = session ID, metadata in the session header (parent, root, depth, `/root/…` path label, name, role,
`spawnedBy` {session, model, effort, turn, tool call id}); state `agent-state/<id>.json`; outside spawns are parentless roots;
new worktrees `atto/<id>`; **no on/off setting, no depth limit, no concurrency limit** (user: "완전 자유"); every model shell
command gets `ATTO_TOOL_CALL_ID`; migration = refuse while agent work runs → automatic backup → one-pass conversion → marker
last (on error: `atto restore`); agent turns run in session workers (phase H). Verify before merging: live spawn + attach a TUI
mid-turn, migration on a copy of the real `agent-state/`, send/interrupt/queued tasks unchanged.

## Next, in order

1. **`thread/list` extension** (brief `/tmp/atto-runs/w18b_overview.txt`): the command center and `atto resume` picker get
   agent metadata and needs-you flags from `thread/list`; remove `scanCenter`'s direct disk/daemon reads.
   No new overview method, no push notifications (decided).
2. **Command center tidy-up:** `a` archive/unarchive, `d` delete, finished agent trees folded by default, no `/root/` prefix.
3. **New web UI** on a new shared UI-element layer (planned, not started): it attaches through `atto app-server --listen ws://`
   and speaks revision 3 (`docs/protocol.md`); `/remote` and `atto serve` return when it exists.
4. **Windows daemon:** workers on Windows (AF_UNIX sockets work since Windows 10). Verify on `win`.
5. Small: the startup Config line lists `~\.atto\settings.json` twice on Windows; a `cli` test failed once under full load (not reproduced in 6 reruns).

## Decisions and things to know

- **Translate extension disabled** (`settings.json` `extensions.disabled`): it saved every translation twice and made sessions grow; no cache API needed.
- **Session segmentation on hold**; revisit only if several sessions get large (see the doc's status note).
- **Env vars are tracking, not security:** `ATTO_SESSION_ID`, `ATTO_AGENT`, `ATTO_TOOL_CALL_ID` can be changed by the model.
- **Machines:** this Mac (macOS arm64) is the main test host. `win` (coolguy-w13, AMD64) runs atto with the local model only
  (`llama-cpp/orca-local` at 192.168.0.235:8081, no API keys); deploy with `scripts/deploy.sh win` after Windows-relevant changes.
  linuxvm/winvm are down.
- A crashed worker restores only saved session state; the daemon's worker registry is in memory; no durable input journal; no full Codex-dialect adapter.
- A fresh `ATTO_DIR` picks the first available model until the catalog cache exists (copy `~/.atto/cache` for live tests).
- Live tests on copies of real sessions: pause their goal first; resuming a copy with an active goal starts goal work in the real project directory.

## How the work is done

- Implementation goes to `atto agent spawn NAME "<brief>" -worktree -m openai/gpt-6.1-sol -effort high` (from a plain shell) or
  to Claude Code Sonnet subagents in worktrees (the user rates them about equal). Briefs live in `/tmp/atto-runs/`. End briefs
  with "you alone do all of it in this turn"; gpt-6.1-sol still sometimes stops after the groundwork, so send it back with a
  list of what is left.
- Before merging: rebase on main, full checks (`gofmt`, vet with and without `-tags noext`, `GOOS=windows go vet`, `go test ./...`
  with `ATTO_SESSION_ID`/`ATTO_AGENT` unset, `-race` on touched packages), `scratchpad/smoke.sh BINARY`, live checks of what changed;
  then merge, `go install -ldflags="-s -w" ./cmd/atto`, push, close the agent and delete its branch.
- Sources of truth: the GitHub issues and the memory directory `~/.claude/projects/-Volumes-t5-atto/memory/`.

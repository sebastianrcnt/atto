# Handoff — 2026-10-10

State: `main` is released as **v0.1.0** (the first release since v0.0.3). The local `atto` (`go install -ldflags="-s -w"`) and `win`
(`scripts/deploy.sh win`) run it. CI is green on all three systems.

A picture of the whole structure (concepts, processes, memory, agents, commands): https://claude.ai/artifact/3KkHWQiWTpwHshzVh2oxMc

## Structure in one paragraph

Five concepts. A **session** is a JSONL file (the source of truth). A **worker** (`atto _session-server`) executes one session.
The **daemon** (`atto _daemon`) only starts, lists and retires workers; it holds no screens (protocol 4). **Clients** (TUI,
browser, `app-server`, `-p`, scripts) attach to workers over JSON-RPC revision 3 and only render. An **agent** is a session
started by another session or a shell; its identity is its session ID, its tree position is header metadata. Without the daemon
(`ATTO_NO_DAEMON=1`, `"daemon": false`) the worker runs inside the client. Windows has the same daemon (AF_UNIX + token file + DACL).
Keep this separation strict: clients never read `~/.atto` or the daemon directly.

## UI layer (docs/ui.md)

Package `ui` (pure Go): 13 elements, sites (pane, band, status, toast, transcript, dialog, message/toolCall/notice overrides),
`next()` middleware, `ui/open|render|close|event|capabilities`. The TUI and the browser draw the same trees; built-ins (/diff,
status line, goal, jobs, dialogs, queue, context card) are Go trees. Extensions: `atto.ui` in goja, JSX, `atto.store`; the old
string API is gone. Stages 1–4 are done (`docs/ui-stage-4-report.md`); **stage 5 (out-of-process UI providers) is not started**
and needs the user's go-ahead.

**Browser UI** (`server/web`): plain TypeScript DOM + Tailwind, built with Go only (`go generate ./server/web`, committed dist,
staleness test, TS tests through goja). `atto serve` = `app-server --listen ws://0.0.0.0:7879 --web`; `/remote` starts it in the
TUI. **No token** (user decision 2026-10-10: the LAN or Tailscale decide who reaches it); Origin check plus a Host check against
DNS rebinding (IP, localhost, machine name, NAME.local, `*.ts.net`, `--allow-origin` hosts). Wildcard binds print one link per
IPv4 address. WS-only app-server listeners keep the bearer token. Visual checks: `/tmp/atto-runs/w23/cdp.mjs` drives headless
Chrome over CDP with the system node (a local tool only; Node is not part of the build; don't use Chrome's virtual time).

## Next

1. Real-device check of the browser UI (phone touch, soft keyboard, LAN/Tailscale from another device): the user's.
2. Small: when a model's shell command is interrupted on Windows, the tree kill also ends descendants that `shell.Isolate`
   detached (e.g. a daemon first started by that command); exclude them if it ever matters.
3. Offered, not approved: UI stage 5; OAuth tokens outside `ATTO_DIR` (test copies rotating refresh tokens logged the real
   install out once); OSC 7501 progress reporting. Session segmentation stays on hold (`docs/session-segments.md`).
4. `~/.atto/extensions/translate.ts` (disabled) still uses the removed UI API; rewrite from `examples/extensions/` if wanted.

## Decisions and things to know

- **Translate extension disabled** in the user's settings (it doubled session growth).
- **Env vars are tracking, not security:** `ATTO_SESSION_ID`, `ATTO_AGENT`, `ATTO_TOOL_CALL_ID`.
- **Agents:** no on/off setting, no depth or concurrency limit; `spawnedBy` {session, model, effort, turn, toolCallId, origin, cwd}.
- **Machines:** this Mac (macOS arm64) is the main host. `win` (AMD64, Korean locale, Go 1.27.1) runs atto with the local model
  only (`llama-cpp/orca-local`: Strata on `linux`, 192.168.0.235:8081, Docker in `~/ml/strata/deploy/docker`, one request at a
  time, shared with the user). linuxvm/winvm are down.
- A crashed worker restores only saved session state; the daemon's worker registry is in memory.
- Live tests on copies of real sessions: pause their goal first. Never copy `auth.json` into a test `ATTO_DIR`.

## How the work is done

- Implementation goes to `atto agent spawn NAME "<brief>" -worktree -m openai/gpt-6.1-sol -effort high` or Claude Code Sonnet
  worktree agents. Briefs live in `/tmp/atto-runs/`; end them with "you alone do all of it in this turn".
- Before merging: rebase on main; `gofmt`; vet with and without `-tags noext` and `GOOS=windows`; `go test ./...` (both tags) with
  `ATTO_SESSION_ID`/`ATTO_AGENT`/`ATTO_TOOL_CALL_ID` unset; `-race` on touched packages; `scratchpad/smoke.sh BINARY`;
  **`scripts/wintest.sh win [args]`** for Windows-relevant changes (tests the committed HEAD on `win`; put `-run` patterns
  containing `|` in literal double quotes); live checks of what changed. Then merge, install, push, close the agent, delete its branch.
- CI runs nightly (03:00 KST) and by hand (`gh workflow run ci`). Releases: push a `v*` tag; the release workflow tests, builds
  full and slim binaries and publishes with generated notes, which are then replaced with hand-written ones.

# Handoff — 2026-10-09

State when this was written: `main` = `150ed3d` (pushed); the locally installed `atto` was built from it. Open issue: **#31**
(cmd.exe quoting). #32 is closed (CI run 37821095567 green on Linux, macOS, Windows). Nothing is running; agent `/root/yield` was
closed and its branch deleted. The refactor commits below have not been through CI yet; the nightly run will cover them.

## What was done today

| Area | Commits / issues |
|---|---|
| Shell yield (#32, Codex-style) | `653074e`: a hosted model command waits 10 s (the model may ask for up to 30 s), then becomes a job. Without a shell host or session the old kill timeout applies (60 s, max 30 min). `!` commands keep their long wait. `e10e149`: a **user interrupt** (Esc, Ctrl+Enter, Ctrl+C in a turn, exit-menu cancel, remote `turn/interrupt`, `atto agent interrupt`) detaches a running hosted command into a job instead of killing it. The cause is `agent.ErrUserInterrupt`, passed through `context.WithCancelCause`. Such a job exits "quietly" (`Job.QuietExit`): its event does not wake an idle session. Quit, shutdown, `-p`, hooks and direct runs still kill. `atto agent interrupt` now writes a per-turn request file (`agentstate/interrupt.go`) that the worker watches |
| Request log | `ab448b1`: `~/.atto/logs/requests.log` (JSONL, rotates at 2 MB to `.1`) records `retry`, `failed` and `recovered` model requests. Each line has the session, model, error, wait, elapsed time and connection (`ai.WithConnTrace`: local/remote address, reused, idle ms). Requests that succeed on the first try are not logged |
| Network resilience | `f88404a`: model requests use a shared transport (`ai/connections.go`) with HTTP/2 health checks (15 s, ping timeout 5 s) and a 30 s idle timeout. A response read that stays silent for 120 s fails with `ai.ErrStreamStalled`. After a connection-level error, the retry loop calls `ai.ResetConnections()` so the next attempt dials fresh. Prompted by repeated OpenAI errors when the Mac moved from Wi-Fi to Ethernet. The real network switch was not reproduced |
| #32 follow-up | `a79ffa2`: `atto agent interrupt` force-stops a worker that does not stop within 10 s (old kill path); `jobs.Host.DetachQuiet` replaces the variadic flag |
| Refactor | `80ef8c0`: `agent/agent.go` split (879 lines) into `stream.go` (retries, overflow recovery), `turn.go`, `compact.go`, `tools.go`; no behaviour change. `150ed3d`: `core.TurnRunner[T]` (`core/turn.go`) holds busy state, cancel cause (`Interrupt` = user stop), steers, queued follow-ups, `Settle` (TUI end-of-turn policy), inbox-event delivery rules and the print/worker boundary inbox; used by the TUI, server, `-p` and workers. Smoke-tested with a real model: a 13 s command yielded at 10 s and the model waited for the job |
| Issues | #31 filed (cmd.exe gets `\"`-escaped scripts; reproduced on winvm before it went down) |

## Things to know

- **The model-facing shell contract changed:** long commands become jobs after 10 to 30 s. The tool schema, `prompts/bash_tool.md`,
  `prompts/system.md`, README and the golden files were updated. Watch live runs for models that poll badly or misread the job message.
- **Codex comparison (from source, openai/codex `e9e6cf6`):**
  - unified exec yields after `yield_time_ms` (10 s default, 250 ms to 30 s);
  - an interrupt does not kill background terminals;
  - `/ps` lists them and `/stop` stops them;
  - the model polls with `write_stdin`.

  atto's equivalents are `/jobs` and `/stop`, plus exit events instead of polling.
- **VMs:** linuxvm and winvm are both down, and the user wants them skipped. Windows coverage is `GOOS=windows go vet` plus CI
  (nightly at 03:00 KST, or `gh workflow run ci.yml`).
- Earlier notes still hold (see the 2026-10-08 handoff in git history):
  - the state directory migration;
  - the "subagent" compatibility names, which must stay while the frozen web client is in use;
  - hook approvals by content hash;
  - Windows job objects;
  - atto2 is archived.

## Not done / open decisions

1. **#31 cmd.exe quoting:** proposed fix: set `SysProcAttr.CmdLine` raw for `Cmd`, and route `config/resolve_config_value.go`'s
   `cmd /C` through the shell package. That path affects `!command` config values even on machines with PowerShell.
2. **`-m` / `enabled` restrictions for `atto agent`:** still undecided. See the 2026-10-08 handoff.
3. **Request log scope:** it logs only problems. An "all requests" mode (e.g. `debug.requestLog: "all"`) was offered but not asked for.
4. **Network:** a third option is to watch macOS route changes and reset the pool at once. It was deferred until `requests.log`
   shows whether health checks and the reset are enough.
5. **Front-end differences the turn runner kept on purpose or by accident** (decide whether to unify):
   the daemon keeps uncommitted steers for its next turn instead of the TUI's `Settle` (possibly accidental); daemon steers do not
   wake `atto sleep` / `atto job wait` (TUI ones do); the daemon steers events into any busy work, the TUI holds them during
   compaction; the daemon's RPC interrupt uses the user-interrupt cause even for compaction; the daemon rejects queued follow-ups.
6. **Estimates are heuristics** (`estimateChars`). Compaction on a one-message conversation costs one extra request. Both unchanged.

## How the work is done

- Implementation goes to `atto agent spawn NAME "<brief>" -worktree -m openai/gpt-6.1-sol -effort high`, run from a plain shell.
  Follow-ups go to the same agent with `atto agent task NAME "..."`, one turn per step with a review in between. Briefs live in
  `/tmp/atto-runs/` (`common2.txt` holds the shared rules). They are not in the repo.
- Small, well-understood changes may be done directly when the user asks ("네가 해").
- Sources of truth: the GitHub issues and the memory directory `~/.claude/projects/-Volumes-t5-atto/memory/`.

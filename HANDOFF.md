# Handoff — 2026-10-09

State when this was written: `main` = `f88404a` (pushed). The locally installed `atto` (`~/go/bin/atto`) was built from it.
Open issues: **#31** (cmd.exe quoting) and **#32** (shell yield; implemented, close it once CI is green). Two things are still
running:

- **Manual CI run 37821095567** on `f88404a`. This is the first real Windows run of #32: job-object breakaway in `shell.Isolate`,
  `shell.TestIsolateOutlivesTree`, and interrupt-detach. Check it with `gh run view 37821095567`. If it is green, close #32.
- **Agent `/root/yield`, turn 4** (external parent `261077a5`, worktree `~/.atto/worktrees/261077a5/yield`, branch
  `atto/261077a5/yield`, brief `/tmp/atto-runs/w10_full.txt`). Planned commits:
  - **0** — #32 review fixes: `atto agent interrupt` force-kills after the 10 s cooperative wait; `Host.Detach`'s variadic
    `quietExit ...bool` becomes explicit.
  - **1** — split `agent/agent.go`: stream, retry and overflow recovery move to their own file. No behaviour change.
  - **2** — a `core` turn runner shared by the TUI, the server and `-p`. Covers start, cancel causes, steers and queue, and
    inbox events. The report must list any front-end behaviour differences for the user to decide; `-p` may be deferred.

  Collect it with `atto agent wait yield -json`. Then review, run `gofmt -l .`, `go vet ./...`, `GOOS=windows go vet ./...` and
  `go test ./...` (`-race` on core, app, server, agent), `git merge --ff-only atto/261077a5/yield` and push. When the work is
  finished, run `atto agent close yield` and `git branch -d`.

## What was done today

| Area | Commits / issues |
|---|---|
| Shell yield (#32, Codex-style) | `653074e`: a hosted model command waits 10 s (the model may ask for up to 30 s), then becomes a job. Without a shell host or session the old kill timeout applies (60 s, max 30 min). `!` commands keep their long wait. `e10e149`: a **user interrupt** (Esc, Ctrl+Enter, Ctrl+C in a turn, exit-menu cancel, remote `turn/interrupt`, `atto agent interrupt`) detaches a running hosted command into a job instead of killing it. The cause is `agent.ErrUserInterrupt`, passed through `context.WithCancelCause`. Such a job exits "quietly" (`Job.QuietExit`): its event does not wake an idle session. Quit, shutdown, `-p`, hooks and direct runs still kill. `atto agent interrupt` now writes a per-turn request file (`agentstate/interrupt.go`) that the worker watches |
| Request log | `ab448b1`: `~/.atto/logs/requests.log` (JSONL, rotates at 2 MB to `.1`) records `retry`, `failed` and `recovered` model requests. Each line has the session, model, error, wait, elapsed time and connection (`ai.WithConnTrace`: local/remote address, reused, idle ms). Requests that succeed on the first try are not logged |
| Network resilience | `f88404a`: model requests use a shared transport (`ai/connections.go`) with HTTP/2 health checks (15 s, ping timeout 5 s) and a 30 s idle timeout. A response read that stays silent for 120 s fails with `ai.ErrStreamStalled`. After a connection-level error, the retry loop calls `ai.ResetConnections()` so the next attempt dials fresh. Prompted by repeated OpenAI errors when the Mac moved from Wi-Fi to Ethernet. The real network switch was not reproduced |
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
5. **Estimates are heuristics** (`estimateChars`). Compaction on a one-message conversation costs one extra request. Both unchanged.

## How the work is done

- Implementation goes to `atto agent spawn NAME "<brief>" -worktree -m openai/gpt-6.1-sol -effort high`, run from a plain shell.
  Follow-ups go to the same agent with `atto agent task NAME "..."`, one turn per step with a review in between. Briefs live in
  `/tmp/atto-runs/` (`common2.txt` holds the shared rules). They are not in the repo.
- Small, well-understood changes may be done directly when the user asks ("네가 해").
- Sources of truth: the GitHub issues and the memory directory `~/.claude/projects/-Volumes-t5-atto/memory/`.

# Handoff — 2026-10-08

State at the end of the session: `main` = `99c9468`, equal to `origin/main`; **no open GitHub issues**;
the last manual CI run (37753324966) is green on Linux, macOS and Windows (amd64). The locally installed
`atto` (`~/go/bin/atto`) was built from main.

## What was done today (all on main)

| Area | Commits / issues |
|---|---|
| Compaction visibility | `f730748`: the compaction block says why it started early (price tier / `compaction.limits`), the status line shows `⇥<trigger>` |
| Context checks | `a5b75cb`: measured before every model request, the new message counts, notes cut off twice are not retried (`ai.ErrNotRetryable`). #20 (no usage → no reset), #22 (dense text estimate), #23 (trim before compacting past a cap) |
| Goals | #25 (two failed compactions block the goal), #24 (notice when `/context long` is on), #21 (`/context` says "No tier cap"; notice only for priced models missing from the catalog) |
| Recovery | #15 (early `length` stop or overflow → compact + retry once per turn), #17 (oversized trailing tool group kept after the notes) |
| Models | #16 (`llamaCppMetadata: true` provider setting: `/models`, `/props`, `n_ctx`, `enable_thinking`) |
| Memory | #2 (`debug.FreeOSMemory()` after 30 s idle, at most every 5 min; TUI and server) |
| Safety | #1 (project hooks need approval by content hash; one prompt for hooks/MCP/extensions; `atto trust`; headless entry points warn and leave unapproved items off) |
| Naming | #26 (package `agentstate`, state dir `~/.atto/agent-state/`, `agent/list` + `agent/read`; old names kept), #27 (README / `atto -h` / `atto agent -h`: `-p` vs `agent`) |
| TUI | #3 (emoji keycap width fixed; `x/ansi` evaluated and not adopted: `docs/tui-width-evaluation.md`) |
| CI | #28–#30 plus 12 Windows commits (events inbox lock, atomic rename retry, job-object breakaway, session lock `FILE_SHARE_DELETE`, test fixtures) |

## Things to know

- **State directory migration.** `~/.atto/subagents/` becomes `~/.atto/agent-state/` (with `subagents` left as a symlink) the first time a
  new atto touches it while no daemon, turn or slot lock is held. On this Mac it has **not** happened yet (a long-running `atto _daemon`
  holds locks); it will happen by itself. **Windows never migrates** (open guards block directory renames); it keeps `subagents/`.
  Roles still live in `~/.atto/agents/`. Code: `agentstate/layout*.go`.
- **Compatibility spots for the old word "subagent"** (all commented): settings key `"subagents"` (`"agents"` wins), env `ATTO_SUBAGENT`,
  old state dir, protocol `subagent/list|read` and their `subagents`/`subagent` result fields (the frozen web client reads them),
  Loaded JSON `subagents`, `subagent_presets`. Do not remove them while the web client is in use.
- **Project hook approvals** are keyed by settings-file path + hash of the hook configuration (not of scripts it calls), stored in
  `~/.atto/hook-approvals.json`. A git worktree has a different path, so its hooks need their own approval.
- **Windows job objects:** `shell.Detach` now asks for `CREATE_BREAKAWAY_FROM_JOB` only when the enclosing job allows it; the tree job has
  `JOB_OBJECT_LIMIT_BREAKAWAY_OK`. Cleanup on atto exit is weaker for processes that explicitly detach.
- **Frozen / archived:** the web client (`server/web`) is frozen (no changes). atto2 is archived: tags `archive/atto2`, `archive/atto2-proto`
  (branches still on origin); restore with `git worktree add <dir> archive/atto2`. `/Volumes/t5/attoos` was left untouched.
- **Release tags** (`v*`) trigger the release workflow; none were created today.

## Not done / open decisions

1. **linuxvm** (`ssh linuxvm`, 192.168.64.7) was unreachable all day, so nothing from today is installed there. Install recipe is in the memory
   `atto-dev-conventions` (cross-build `GOOS=linux GOARCH=arm64`, scp to `/usr/local/bin/atto.new`, `mv`).
2. **`-m` / `enabled` restrictions for `atto agent`** (discussed, not implemented, no issue filed): `-m/-effort` are refused whenever
   `ATTO_SESSION_ID`/`ATTO_AGENT`/`ATTO_SUBAGENT` is set (so a tool run from inside an atto shell is treated as the model), and
   `agent spawn|task|send` need `agents.enabled` even for external callers. Proposal: exempt external callers from the `enabled` gate,
   add `agents.allowModelOverride`, maybe an explicit `ATTO_CALLER=external`. Decide, then file an issue.
3. **cmd.exe quoting defect** (found by a CI agent, out of scope then): with `cmd.exe` forced as the shell, argument quoting is wrong; no issue yet.
4. **Windows ARM64 vs CI amd64:** `winvm` verification is ARM64/PowerShell 5.1; CI is amd64/PowerShell 7. Both are green now.
5. **Estimates are heuristics:** `estimateChars` counts 3 bytes/token for dense text, 4 otherwise; a response with zero usage no longer resets
   the running estimate. Real tokenizer error is not measured anywhere (idea from #22: log estimate vs reported usage in `/debug`).
6. **Compaction retry on a one-message conversation:** #15 changed the guard from `len(messages) > 1` to `> 0` so single-message turns can
   recover; it costs one extra request when nothing can be summarized.

## How the work was done (for the next session)

- Implementation is delegated to `atto agent spawn NAME "<brief>" -worktree -m openai/gpt-6.1-sol -effort high` from a plain shell
  (a lightweight parent session is created automatically), at most **two agents at a time**; then `atto agent wait -json`, review the
  branch in `~/.atto/worktrees/<parent>/<NAME>`, `git rebase main`, run `gofmt -l .`, `go vet ./...`, `GOOS=windows go vet ./...`,
  `go test ./...` (and `-race` on touched packages), `git merge --ff-only atto/<parent>/<NAME>`, push, `atto agent close NAME`,
  `git branch -d`. Plain `atto -p` is a standalone session, not an agent in a tree; do not use it for delegation.
- Briefs live in `/tmp/atto-runs/*.txt` (`common2.txt` holds the shared rules); they are not in the repo.
- **winvm** (`ssh winvm`, Windows 11 ARM64, PowerShell 5.1) has Go 1.27.1 at `C:\go` (not on PATH: `set PATH=C:\go\bin;%PATH%`).
  Pack with `COPYFILE_DISABLE=1 tar --exclude=.git --exclude=node_modules -cf x.tar .`, scp, extract under
  `C:\Users\user\atto-ci-scratch\<name>\`, `go test`, delete the folder afterwards.
- **CI** runs nightly at 03:00 KST; run it by hand with `gh workflow run ci.yml` and watch with `gh run view <id>`.
- The sources of truth for decisions are the GitHub issues (all closed, with commit references) and the memory directory
  `~/.claude/projects/-Volumes-t5-atto/memory/` (delegation rule, test scope, atto2 archive).

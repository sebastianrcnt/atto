# Agents are sessions; trees are metadata

**Proposal, 2026-10-09 — design only.** Adopt Codex's identity model, not its
storage implementation or wire protocol. An agent's identity is its session ID;
its parent, role and address are attributes, never storage keys. Remove synthetic
outside parents. Preserve atto's durable turns, jobs, events and same-tree model
messaging. This proposal defines storage/query contracts, not app/server cache or
paging internals; the concurrent memory/paging work can consume these contracts.

## 1. Current implementation and desired records

Today's sessions are parent-owned records: `agentstate/state.go` stores
`<parent>/<name>.json` with turn/interrupt files; `tree.go` uses `_up` for ancestry
and `_closed` for removed IDs. `layout.go` merges `agent-state/` and legacy
`subagents/`, preserving old lock locations and sometimes a symlink.
`cli/agent_external.go` creates a fresh `session.NewExternal` per outside spawn
and supports legacy shared parents. Project lookup follows that parent's cwd;
zero-message parents accumulate in session lists/archive. `cli/agent.go` starts
`_agent-turn -session PARENT NAME N` as PARENT's job and delivers FINAL_ANSWER
there. Slots are per-parent; shutdown uses tree locks. `app/agents*.go` combines
saved sessions/state; `server/background.go` walks children. Header `AgentOf` and
`External` drive filtering.

Codex's checked model (2026-10-08) is ThreadId with its own rollout's
`SessionSource::SubAgent(ThreadSpawn { parent_thread_id, depth, agent_path,
agent_nickname, agent_role })`. Its live manager resolves labels; its message
board enforces tree boundaries. Outside callers use thread IDs, not dummy parents.

Proposed agent session header (the session header is an `Entry` today):

```json
{"agent": {
  "version": 1, "parentSessionId": null, "rootSessionId": "a1b2c3d4",
  "depth": 0, "path": "/root", "name": "tests", "role": "general",
  "spawnCwd": "/src/project/pkg", "project": "/src/project",
  "origin": "external"
}}
```

Children inherit root, set depth=parent+1, and join parent path with their name.
Normal sessions have implicit root=self, depth=0, path=`/root`, without `agent`.
`ParentSession` (transcript fork ancestry) remains unrelated. Presence of `agent`,
**not nonempty parent**, marks an agent session. Role is the preset label;
snapshot instructions/model/effort stay in runtime state.

Use `~/.atto/agent-state/<session-id>.json`, `<id>.turn.json`,
`<id>.turn.json.interrupt`, and `<id>.turn.lock`. State contains version, ID,
creation/lifecycle (`open`, `closing`, `closed`), runtime settings, worktree refs,
turn counter and an explicit `{ownerSessionId, jobId}` reference. Include a small
metadata/lifecycle summary as the first JSON field for bounded inventory reads;
the header owns immutable identity/tree
metadata, and projection mismatches require repair, not silent reparenting.
Reserved coordination files live under `.coord/`, outside the record namespace.
No parent directories, `_up`, or separate `_closed` records in the final layout.
Closed state remains, with last turn and archive location; it is not a live agent.
Creation journals ID allocation, header/worktree creation and state publication
under the tree lock; only a published record is addressable. Recovery completes
or rolls back unfinished spawns before admitting new mutations.

**Outside spawn creates a parentless agent**, root=self, depth=0, path=`/root`.
Its name is a project lookup label, not a path segment. A project path would
imply a shared tree and messaging rights that do not exist. Record canonical
project using today's ProjectRoot/symlink/Windows-case rules before entering a
worktree, and original cwd separately from execution cwd. Descendants inherit it.
Explicit outside `-session ID` still creates a child of that real selected
session; no `External` header or dummy session is created.

## 2. IDs, labels and inventory

`@<full-id>` is the durable address; retain unique prefixes of at least six
characters, exact-match priority, and candidate lists on ambiguity. Closed IDs
participate in prefix ambiguity and return “closed” plus transcript instructions,
never a reused name. IDs can name the ordinary tree root for
messaging; commands requiring a managed agent reject ordinary sessions.

Inside atto, names resolve as children of the caller, relative paths descend,
`..` names the parent (error at a root), and `/root/...` starts at its tree root.
ID lookup and every send/task operation enforce equal root IDs, even with
`-session` overrides; the override is a lookup context, not an escape hatch.
An outside caller can address any managed agent by ID. Without explicit context,
bare names select **open, parentless, externally spawned roots in this project**;
`tests/lint` then descends into that selected root. Duplicate roots named `tests`
produce candidates with full `@IDs`, status and age. Retain outside `/root/tests`
as a compatibility spelling of that project selector, clearly documented as
such; bare `/root` and `..` need `-session`, since outside has no selected tree.

Keep names unique among **open children of one parent**, including idle/completed
agents, not merely currently running turns. Close frees the label; paths are
labels that may be reused, never durable identity. Outside roots may duplicate names, as today. Serialize sibling reservation
with the tree lock; defer renaming/reparenting.

Inventory contract: `Get(id)`, `Children(parentId)`, `Tree(rootId)`, and
`ExternalRoots(project, includeClosed)`. Start with one directory inventory of
`agent-state/*.json`, excluding `.turn.json`, and decode only each compact first
summary field, not task/instruction bodies or transcripts. Build ID, parent,
root and project maps once per
snapshot, not a disk scan per tree edge. Cache by file signature; generation hints and directory reconciliation
invalidate membership changes. Hints are not authoritative: mutations revalidate
disk under locks, periodic rescans catch missed notifications/crashes, and
projections can be rebuilt from headers. Benchmark before adding disk indexes. Closed records can
be lazily hydrated for history, but must remain in the ID ambiguity inventory.

The center displays real roots and children, including closed/history nodes.
A project “started from a shell” group is a **virtual display group**, without a
session ID, inbox or ancestry effects. Missing ordinary parents are placeholders
keyed by their original IDs, not invitations to attach orphans to another root.

## 3. Parent-dependent behavior

- **Worktrees:** new agents use `~/.atto/worktrees/<id>` and branch `atto/<id>`.
  Allocate the session ID before planning git work. Names and nesting no longer
  cause branch collisions after close/name reuse. Keep existing worktree paths,
  branches, Repo/Base/Cwd unchanged on migration; always operate from recorded
  refs. Rollback removes only newly created resources. Close preserves branches
  and refuses dirty worktrees unless `-force`.
- **Limits:** validate parent/root/depth/path consistency with cycle detection
  and bounded ancestry walks; missing/corrupt ancestry fails mutations closed.
  `agents.maxDepth` bounds edges from a real root: allow child iff parent depth
  is below the limit. An outside root starts at 0 and can spawn depth-1 children
  at the default 1 (an intentional change from its former fake-parent depth 1).
  Concurrent child turns acquire `.coord/parents/<parent-id>/slots/*`; tree locks
  use `.coord/trees/<root-id>/`. This is coordination, not parent-owned records.
  Preserve per-parent concurrency, not an implicit project cap. Parentless turns
  have their per-agent turn lock and no child-pool slot: charging their own turn
  against their children's pool would deadlock a root waiting for a child at
  maxConcurrent=1. Independent outside roots still have no aggregate cap.
- **Jobs:** retain parent-owned turn jobs for children and their existing
  `atto job list` labels. A parentless agent's turn job belongs to its **own
  session**, identified by the explicit job reference; outside spawn/report
  prints its owner and job ID. `_agent-turn` receives agent ID + turn number,
  not parent/name. Distinguish the controlling agent-turn job from that session's
  shell jobs: routine end-of-turn cleanup must not kill its own supervisor;
  explicit agent interrupt/close acts on the controlling reference. Ordinary
  jobs remain unchanged. Task senders never become parents.
- **Inbox:** child completion pushes FINAL_ANSWER only to its recorded parent,
  even if an outside caller started the follow-up. A root has no completion
  recipient: persist its terminal turn and let callers `wait`/`report`, with no
  self-inbox event. Preserve envelope kinds and the 8,000-character delivery
  cap. Add sender/recipient IDs and turn identity alongside path labels for
  deduplication and unambiguous logs; labels alone cannot identify reused paths.
  Suppress normal job-exit wakeups for root turn jobs too. Child turns ending
  do not stop their descendants; pending waking tasks still get successor turns.
- **Close/archive:** `rm` remains a `close` alias. Named close preflights the
  whole subtree, rejects active turns, and closes deepest first; `close -done`
  selects direct children (outside: project roots) whose entire subtree is
  inactive. `-force` allows dirty-worktree removal, not implicit interruption.
  Persist `closing` before teardown under the tree lock, reject new work below
  it, then resume removal/archive after crashes and commit `closed` last. Keep
  last state/turn and stable IDs. Archiving a transcript alone is not closing;
  explicit root shutdown blocks new work and stops active descendants as today.
  Reopening an ordinary session can reopen its tree gate; a closed agent ID is
  not revived by unarchiving its transcript.
- **Session lists:** hide sessions with `agent` metadata, including parentless
  roots; keep historical `AgentOf` fallback. Stop writing `External`. Default
  resume/continue must never pick managed agents or legacy dummy parents.

## 4. Migration (decided 2026-10-09: simple, backup-based)

The user decided against dual layouts, journals and drain protocols. atto is a
personal tool on a few machines; `atto backup`/`atto restore` exist. Migration:

1. `atto agent migrate` (also run automatically by the first new-format
   operation, after asking on a terminal) refuses while any agent turn, job
   supervisor of an agent turn, or session worker of an agent session is
   running, naming them.
2. It runs `atto backup` (default location under ATTO_DIR/backups/, secrets
   excluded) and prints the path.
3. It converts everything in one pass: legacy parent directories, `_up`,
   `_closed`, shared and per-spawn external parents (each direct child of an
   external parent becomes its own outside root), headers rewritten with the
   `agent` metadata, records written as `agent-state/<id>.json`. Existing
   worktrees and branches keep their paths and names. Fake external parent
   sessions with no messages are deleted; any with real messages stay as
   ordinary sessions. A format marker is written last.
4. On any error it stops, leaves the marker unwritten and tells the user to run
   `atto restore <backup>`; no automatic partial rollback. Re-running after a
   restore is safe.

Old binaries are not supported after the marker: they refuse to run agent
commands when the marker is present (add that check to the release before
the migration release is not possible retroactively; document "upgrade every
machine; old binaries must not run agents on migrated data").

## 5. User, model and protocol surface

Update `prompts/agent.md` with own ID, root/path and nullable parent. A parentless
agent is “started from a shell”; its self-contained answer is saved for polling,
not automatically sent, and `send ..` is unavailable. Worktree instructions say
“spawning checkout's HEAD”, not nonexistent parent's HEAD. `agent_parent.md`
keeps automatic child answers. Both explain labels versus durable IDs, same-tree
boundaries and depth; prompts do not expose runtime file locations.

CLI spawn always prints full `@ID`, name/path, project and worktree/job refs;
outside output says “poll with wait/report”, never “external parent created” or
“answer reaches you”. Lists distinguish named outside roots without implying a
project-wide tree. Update README/help layout, worktree, concurrency, outside
polling and depth examples, plus migration diagnostics. Keep aliases, `-session`,
`-json` existing fields and add metadata/job-owner fields without replacing IDs.

Protocol changes are additive: keep `agent/list` direct children and
`agent/tree` scoped to the selected real root; include the root in `agents` when
it is itself a managed agent. Keep existing `threadId`, `name`, `path`, `preset`,
`parentThreadId` fields; use empty/omitted parent for roots and add root ID/depth,
origin/project and closed lifecycle. `agent/read` accepts an optional agent ID
in addition to existing `name` addresses, reusing the same authorization rules.
Retain `subagent/*` aliases and duplicate result keys for the frozen web client;
never fabricate a parent to satisfy it. Selected-thread child panels still
work; virtual project groups are client presentation, not protocol threads.
Coordinate DTO/docs/contract tests with the paging work, not its implementation.

## 6. Risks, alternatives and reviewable delivery

Risks: split-brain old writers, divergent projections, name races, self-job
shutdown, changed depth and archive-scale scanning. Validate trees, gate cutover,
journal recovery and measure inventory costs.

**Keep parent layout, only drop fake parents:** smaller, but needs a pseudo-parent
namespace, retains `_up`, and couples worktrees to reusable names; transitional
only. **Single database:** fast transactional lookup/reservations, but adds
recovery/locking complexity and catalog dependence. If benchmarks warrant SQLite,
make it a rebuildable metadata catalog, not the source of identity.

Implementation sequence, each independently reviewed:

1. Metadata/header helpers and validation; retain old filtering fallback. Test
   real roots, agent roots, forks, missing parents, cycles and projection repair.
2. ID repository/query facade and compatibility inventory. Test sibling name
   races, outside duplicates/projects/symlinks, exact/prefix/closed IDs, tree
   boundaries and linear inventory behavior with many archived agents.
3. New spawn/turn format, ID worktrees and explicit job owners behind a format
   gate. Test failures/rollback, queues, maxDepth, maxConcurrent=1 root+child,
   interrupt while queued/running, no root self-event/self-kill, successor tasks,
   and parent-only answers with outside follow-ups.
4. Journaled migration tool plus old-layout adapter. Run old-binary/new-binary
   integration fixtures, concurrent old spawning during drain, shared external
   parents, separate/symlinked legacy directories, tombstones, archived headers,
   Windows locks, and crash injection at every prepare/commit/cleanup boundary.
   Assert repeat runs/rollback preserve transcripts and git refs byte-for-byte
   where unchanged, and refuse unsafe cutover.
5. Switch CLI/prompts and UI/protocol adapters; contract-test frozen aliases,
   root rendering, read-only archived transcripts, lists/continue filtering,
   dirty subtree close, and crash recovery. Benchmark before adding disk indexes.
6. Enable by default after migration soak; remove legacy writers/aliases only
   in a later compatibility release. Deliver only documentation in this change.

## Decisions (2026-10-09)

The user accepted recommended answers 1-4 and 6 below and replaced 5 with the
backup-based migration in section 4.

## Open questions for the user (recommended answers)

1. **Outside roots use `/root` or a project path?** `/root`; retain their name as
   the project selector, with a virtual project display group, not a shared tree.
2. **Permit duplicate sibling names?** No among open children; yes among outside
   roots. Keep today's predictable inside addressing and outside ambiguity lists.
3. **Worktree naming?** `atto/<id>` and `worktrees/<id>` for new agents; never
   rename existing resources. Stable ID beats readable but reusable labels.
4. **Outside depth/concurrency?** Depth 0, so default permits one child level;
   preserve per-parent limits and no new project-wide root cap. Document the
   change; add an explicit project budget separately if wanted.
5. **How much old-binary compatibility?** Finish old work in its original layout,
   then require a verified drain/upgrade before cutover. Reject a destructive
   automatic migration while legacy writers might still run.
6. **Turn job ownership and index complexity?** Keep child jobs parent-owned,
   root jobs self-owned with explicit refs; start with cached file inventory,
   not a database. Revisit both only with measured operational pain.

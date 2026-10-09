# Segmented live sessions (proposal; no implementation)

**Status (2026-10-09): on hold.** The memory/paging work already made large sessions cheap to open and attach (a 62 MB session: worker 653 → 45 MB, TUI 295 → 27 MB). What segmentation would still add is disk space and the cost of scanning the whole file when opening. Revisit when several live sessions are large, opening one becomes slow, or disk use matters; then start with a manual `atto sessions split <id>` and `atto sessions join <id>` (no automatic cuts).

## Recommendation and scope

Keep the session's public `.jsonl` path, ID and writer lease; put cold history in
immutable, independently compressed zstd segments beside it. Keep the complete
current model context and a small storage checkpoint in the live file. Nothing
is discarded: the logical session is still one ordered log whose entries form
one tree.

This complements `/tmp/atto-runs/w15_memory.txt`, which owns streaming parsing,
bounded worker/display retention and protocol-v3/client paging. Reuse its entry
index and page replay with location-aware storage; do not add another transcript
cache or protocol. This reduces hot I/O/storage, not current-context memory.

## 1. Disk format and identity

For `20261006-120000-abc12345.jsonl`:

```text
20261006-120000-abc12345.jsonl       # checkpoint + hot entries; same public path
20261006-120000-abc12345.lock        # unchanged LockPath; never replace this file
20261006-120000-abc12345.jsonl.d/    # private directory (0700; files 0600)
    catalog-000001.json            # immutable generation manifest
    seg-000001.jsonl.zst           # ordinary entries, chronological order
    seg-000001.idx.zst             # compact structural/location metadata
    ...
```

Numbers are monotonic, minimum six digits, never reused. Manifest paths must
be confined basenames, not symlinks. `List`, `Find`, `CompressArchives` and image
pruning must skip `.jsonl.d`: `IsSessionFile` otherwise discovers segments as
sessions.

The live file has a **physical** `session_segmented`, version-2 header, retaining
all original header fields, followed by a `segment_index` storage record:

```json
{"type":"segment_index","generation":1,"catalog":"catalog-000001.json","coldThrough":28029,"publishedThrough":29151,"leaf":"...","nextOrdinal":29152,"state":{}}
```

`state` holds latest session-wide model, effort, context-mode, name and goal
snapshots, accumulated usage, last usage/model, active last role and bounded
listing previews. Apply state/usage updates only beyond `publishedThrough`;
restoring hot context still replays all its entries, without double-counting.
Checkpoints are not logical entries. The catalog lists segment ordinal ranges,
lengths, counts, checksums and index locations. Indices contain ordinal, ID,
parent, type, time, role/text-presence, decoded offset/length, sparse target/from/
tool-call metadata and bounded tree previews. Catalog growth stays **outside**
the live file. Decode cold indices for structural queries, not ordinary attach.

Every ordinary stored entry gets a stable, 1-based **logical ordinal**, excluding
header and storage records. IDs and parent IDs never change. On first migration,
materialize legacy `#n` IDs/parents using the original full-log numbering
(`tree.go:link`); never relink each segment separately. Keep message bytes/values
and compaction replacements unchanged. Offsets are `(segment, decoded offset)`
or `(live generation, offset)`, not universal byte positions.

Use one resolver for header, metadata, ID/ordinal lookup, paths and full-log
streams. Cold-ID queries build the slim ID→location/parent map from index shards
(or reuse the memory work's map), without decoding unrelated payloads. `Load` remains a compatibility
materializer; hot paths must not call it. `ReadContext` reads the checkpoint and
hot entries without opening cold payloads; `ReadActive` and transcript pages
resolve cold ancestors on demand. Make `Open` a logical flattened stream for
full-history consumers/export; give persistence internals a separate physical
opener. Refactor the current `summary_cache.go` assumptions that `Open` returns
`*sessionReader`/one seekable file. Invalidate offsets by generation, not just
size/mtime or a newline: rewrites can have equal sizes. List whole-store size.

## 2. Cuts, branches and lazy history

**Default:** after a successfully persisted compaction, schedule a cut once
eligible uncompressed history reaches **8 MiB**. Also check on resume/idle when
there is an eligible compaction. Do not force a model compaction for storage.
Split cold entries into at-most-**4 MiB decoded** segments on line boundaries;
a larger individual entry is a documented jumbo segment, streamed rather than
buffered wholesale. Use the archive's `SpeedDefault`, one encoder worker.
No deletion or merging.

Let `C` be the latest compaction on the **current leaf's parent path**, not the
last compaction anywhere in the file. The first cut moves the chronological
prefix with ordinal `< C.ordinal`, including **all off-branch entries** in that
prefix. Keep `C` itself (its complete `Replacement`, not only `Notes`) and every
entry on the active path after it hot. Ordinary entries appended after the cut
remain live, even off branch, until a later cut. Session-wide state preceding
`C` may move only with its exact checkpoint preserved. Display-only records,
labels, earlier compactions and abandoned branch summaries are cold-eligible;
none are dropped. Later cuts segment only newly cold canonical entries and
release no-longer-needed hot pins.

Normally the hot file is current-context bytes + less than 8 MiB eligible
history + checkpoints. **Not a hard quota:** no compaction, huge replacements or
uncompacted old branches require a large live context. Compressed history and
metadata still grow; only redundant hot history is bounded.

* **Cross-cut parents:** preserve IDs verbatim. The resolver follows parents
  across index shards; it never treats a missing live parent as a new root.
  `TargetID` (labels/displays), `FromID` and tool-call relationships resolve the
  same way. Missing required segments are errors, not silently shorter trees.
* **`/tree`:** build structure from slim metadata, with lazy preview/text loads.
  A user-message selection moves to its parent and recovers text/images for
  editing. Load the selected path's own last-compaction suffix (whole path if none).
  **Before publishing the branch move**, atomically rewrite the live file with
  hot `pin` envelopes `{type:"pin", ordinal:N, entry:{...}}` for required entries
  residing in segments, plus the new ordinary branch/branch-summary entry.
  Pins are byte-equivalent cached copies, not new entries or new ordinals; they
  never change the leaf. Cold originals remain immutable. Lookup prefers pins;
  full-log iteration emits each ordinal once.
  Checkpoint and branch marker commit together, keeping restored context hot
  after restart.
* **Branch summaries:** compute abandoned paths from metadata, resolving the
  content needed by `HasBranchContent`/`branchStart` lazily. Summarization still
  uses the agent's current context; record the resulting summary on the chosen
  parent and retain `FromID`.
* **Fork:** stream the selected root-to-leaf path across segments; keep today's
  fresh IDs, label carrying, display target remapping and `ParentSession` path.
  Copy content, not segment dependencies: parent deletion must not break forks.
* **`history grep/show`:** ordinals keep today's `#n` stable across cuts,
  including non-searchable entries. Grep streams every branch's canonical log,
  preserving searchable fields, tool descriptions and `-active`; show resolves
  its ordinal/context window. Cross-segment tool descriptions need indexed call
  metadata. No search index: grep still scans history.
* **Pages/copy/agents:** storage relocation does not change entry/block/item
  identity or live event IDs. Cursors must use logical identity/ordinal and
  branch identity, not raw offsets; re-resolve against the current generation
  after a cut. The memory work still owns page replay dependencies
  (tool calls/later displays). Agent sessions have independent stores; `AgentOf`,
  orchestration state, transcripts and last-answer lookup use this resolver.

## 3. Durable publication and concurrency

The process holding the existing **OS writer lease** owns cuts; `Writer.mu`
alone is not that lease, and today's `Append` does not acquire one. Route every
mutation, including standalone rename/bookmark commands, through a lease-aware
store. Do not drop or rename the persistent `.lock` during publication.

1. Under that lease, select a complete-line prefix and snapshot its generation.
   Stage streaming compression/index construction while ordinary append-only
   tail writes continue; never cut a partial trailing line. Sync and close temp
   segments/indices, verify counts/checksums, rename to final immutable names,
   sync the store directory; publish and sync the immutable catalog likewise.
2. Between agent steps, serialize on the worker lane and `Writer.mu`. Revalidate
   the generation, active compaction and protected context; abandon stale staged
   work after a concurrent branch change. Flush/sync and close the writer handle.
   Stream a replacement live file containing the new header/checkpoint, required
   pins and **all** retained tail entries through the captured high-water ordinal.
   Sync and close it. Atomically replace the public `.jsonl`, sync its parent
   directory, reopen the append handle, restore leaf/ordinal state, then unlock.
   Never continue appending to the old inode after replacement.
3. Before replacement, the old file is authoritative; staging files are orphans.
   After replacement, only its catalog is authoritative. Recovery checks manifest
   integrity and referenced-file existence/lengths; verify payload checksums on
   access. Missing/corrupt required data fails closed with a repair diagnostic.
   Never concatenate discovered segments or replay pins as entries. GC staging
   orphans under the lease after a grace period.

Use the archive code's Windows delete-sharing opener, close-before-rename,
write-through replacement and retry policy; Unix needs file **and directory**
fsync. A failed cut before publication leaves appends on the old representation;
publication/reopen failures must stop writes until validated recovery.

Readers capture an opened live file, generation/catalog and complete-line
high-water mark. Old inodes remain valid snapshots; new opens see complete
generations. Keep immutable segments/indices and initially all catalogs, so cuts
do not invalidate grep/pages. Across requests, cursors re-resolve locations.
Archive/delete can remove the store: pin required open handles per bounded
request or report removal/retry, never silently incomplete history. Destructive
operations retain both leases and archive-shadowing rules.

## 4. Lifecycle and older binaries

* **Archive/compress:** stream the logical log to a single self-contained
  version-1 `.jsonl.zst`, omitting storage records/pin duplicates. Keep current
  destination-first fsync/rename semantics, then remove source file **and store
  directory**. A crash leaves a valid source or destination (possibly both),
  never a destination depending on removed segments. `CompressArchives` also
  flattens segmented plain archives.
* **Unarchive:** restore today's ordinary JSONL under both leases; optionally
  segment it after successful restore while holding the live lease. Do not assume
  a `.zst` copy of the hot file alone is a complete archive.
* **Delete:** extend `RemoveCopies` to remove matching stores in both roots and
  crash-left representations. Preserve current jobs-stop, inbox/goal removal,
  background-log handling and image-pruning behavior. Image reference scanning
  must traverse the *logical* complete log, including compaction replacements
  and cold branches; unknown/corrupt data forbids pruning. Jobs, inbox and goal
  files are keyed by unchanged session ID; image files are content-addressed,
  and `FullOutputPath` remains external. Segmentation does not collect them.
* **Export/copy:** default to flattened version-1 JSONL (plus separately bundled
  images/output assets for a genuinely portable export). Raw backup/copy must
  include `.jsonl` **and** `.jsonl.d`; document that copying the hot file alone
  is insufficient. `/copy` answer/selection resolves old payloads lazily.
* **Compatibility:** a header version bump **alone is unsafe**: current `Load`
  and `readHeader` check `type`, not `Version`; unknown storage records would
  otherwise produce a truncated tree and could permit writes. Use the distinct
  physical `session_segmented` discriminator so existing normal open paths reject
  it as “not an atto session,” while new readers normalize it to the logical
  session header. Add explicit maximum-version/feature checks to all new open
  and writer paths. Ship these guards first and segmentation opt-in initially;
  upgrade the daemon/clients together. Old binaries can read a flattened export
  or archive, not resume a segmented live file. This does not protect against
  an arbitrary raw appender or unchecked legacy `Writer.Resume` API caller;
  mixed-version writers are unsupported, not made safe by advisory locking.

## 5. Synthetic measurements (2026-10-09)

Generated only in `/tmp/atto-segments-design`, not from user data: seeded
`20261009`, 29,152 lines, 25 evenly spaced compactions, 7,000 assistant messages,
9,000 tool results, 8,000 display updates, 2,400 user messages and 2,726 residual
`ext_text` entries. Serialized category budgets were respectively 25, 19, 12,
1.4, 2 (compactions) and 5.6 MB; compactions include both notes and replacements.
Payloads mix repeated source/log/prose tokens and 22% unique hex-token choices,
not padding. This matches composition, **not real entropy** or branching.

Ran `python3 /tmp/atto-segments-design/measure.py`, then
`go run /tmp/atto-segments-design/measure.go` using the repo's
`klauspost/compress` v1.18.0, `SpeedDefault`, encoder/decoder concurrency 1 on
macOS arm64, Go 1.27.1. Units below are decimal MB.

| Measurement | Result |
|---|---:|
| Original JSONL | 65.000 MB |
| Hot file after latest cut (1,122 entries, including last compaction) | 2.472 MB |
| Cold payload, 15 segments, decoded cap 4 MiB | 62.529 MB |
| All cold payloads, Go zstd | 14.995 MB |
| Basic graph/offset indices, decoded / zstd CLI v1.5.7 level 3 | 3.440 / 0.378 MB |
| Catalog | 2.7 KB |
| Total segmented store (above codecs; excludes old catalogs/staging) | 17.848 MB |
| One old segment: 1,918 entries, decoded / Go compressed | 4.193 / 1.009 MB |
| One old segment: decode to discard / stream-decode + `session.Entry` parse | 8.94 / 29.05 ms |

Go timings are warm-cache medians of 11 runs after one warm-up, decoder creation
included, compilation excluded. Full segment parsing allocated 19.88 MB **in
total**, not retained heap or peak RSS; no worker/TUI memory claims follow from
this experiment. Synthetic indices omit sparse extras/previews; real indices
will cost more. Show can stop at its entry; pages may need neighbors. Hot-file
reduction is **96.2%**, total reduction **72.5%**. Measure real compression, cold
reads, publication latency and Windows behavior during implementation.

## 6. Alternatives, risks and implementation sequence

* **Whole-file rotation per compaction:** changes paths/locks/identity or needs
  cross-file tree stitching anyway.
* **One append-only zstd with seekable frames:** similar compression and fewer
  files, but frame-directory publication, torn appends, garbage recovery and
  portable reader compatibility are more complicated than immutable segments.
* **SQLite:** indexed queries/transactions, but migration, WAL/backup/locking
  changes and loss of primary JSONL inspectability are disproportionate.
* **Compress only archived sessions (today):** solves neither a three-day live
  file nor repeated historical scans. **Delete pre-compaction history:** breaks
  tree, search, fork and image ownership; rejected.

Risks: unsafe old-version opens, duplicate pins, missed cross-segment references,
rewrite races, many files/catalogs, transient migration/flattening disk space and
slower grep. Immutable chunks, stable ordinals, one resolver and thresholded cuts
contain these. Later catalog GC/index packing is optional.

Implementation, independently reviewable steps:

1. Ship physical-format/version guards and flattened export; audit all raw file
   users, listing walks, mutations and image scanning. Test old-binary rejection
   explicitly (including resume/rename), not just a new reader's version check.
2. Add the location-aware resolver atop the memory work's streaming index.
   Golden-test identical logical log, context, state/usage/list summaries,
   history ordinals, labels, previews and block IDs for legacy and segmented
   fixtures; keep `Load` only for compatibility/tests.
3. Add transactional cutting and recovery. Inject failures at every write,
   encoder-close, fsync, rename and reopen; test orphan recovery, disk-full,
   truncated tails, missing/corrupt segments, append/branch during staging and
   multiple readers. Exercise Unix and Windows, including lease handoff.
4. Add cold navigation/hot pins, summaries, forks and paged replay tests across
   cuts/branches, with events arriving during pages. Verify model requests are
   byte-equivalent before/after cut, and after restart on an old branch.
5. Finish archive/unarchive/compress/delete/export integration and image-GC
   round trips; test interrupted transfers and copying without its store.
   Re-run the memory agent's open/attach/page/compaction/tree peak benchmarks,
   plus disk sizes, cold/warm segment loads and cut latency. Enable automatic
   cuts only after these pass; run full tests, race tests and cross-platform vet.

## Open questions (recommended answers)

1. **Automatic cuts or explicit command?** Initially opt-in; then automatic after
   compaction/resume with 8 MiB eligible / 4 MiB segment defaults. No extra model
   calls. Provide an explicit maintenance command for migration/verification.
2. **Must already-installed old binaries resume these live files?** Recommend
   **no**: fail closed, upgrade to resume, or flatten under a new binary's lease.
   A version number alone cannot enforce this against existing readers.
3. **Hard hot-file quota even on uncompacted/old branches?** Recommend **no**:
   retain exact model context and expose its size. A hard quota requires changing
   context semantics or permitting cold model reads, outside this proposal.
4. **Segmented archives too?** Recommend **no for v1**: flatten to today's single
   `.jsonl.zst` for compatibility and backups; revisit only if archive paging
   latency justifies a separate archive container.
5. **Garbage-collect or deduplicate historical content?** Recommend **neither**:
   retain all branches/displays; only staging-orphan cleanup initially. Compression
   bounds the hot working set, not the information retained forever.

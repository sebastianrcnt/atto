# Long-session memory regression (2026-10-09)

Measured on macOS/arm64, Go 1.27.1. No private session files are used.
`internal/sessionfixture.Write` generates about **68,746,500 bytes (65.56 MiB)**,
29,152 JSONL lines, 25 compactions and 13,016 persisted display items, with
reasoning, assistant text, user messages and tool results. Large old compaction
replacements exercise skipped raw strings as well as display replay.

## Methodology

Each worker operation runs in a fresh subprocess with the real session runtime,
agent, hooks and extension host. A separate client subprocess consumes replies;
its allocations are not charged to the worker. HeapInuse/HeapSys are sampled at
1 ms intervals during each operation; RSS is read with `ps`, and **max RSS is
process-lifetime `getrusage(RUSAGE_SELF)`**, not a heap estimate. RSS includes
fixture generation and runtime setup, making the peak conservative. Sampling
can miss sub-millisecond heap spikes; getrusage still captures the process peak.

(Historical: the full-snapshot baseline below measured the revision 2 path, which
has since been removed, and its test modes are gone; the numbers are kept for
comparison.) The baseline deliberately reconstructs the prior full-active-branch reader and
full display replay and, for attach, the old whole-value `json.Encoder` buffer,
with no soft memory budget. It is a reproducible synthetic comparison, not a
claim that this test is executing a historical binary. The new revision-2 wire
path streams encoding but still reconstructs full snapshots for compatibility.
Snapshot sizes below count the JSON snapshot DTO, excluding the RPC envelope.
Open measures opening/replay; attach measures a real connection snapshot; page
fetches 200 earlier items; compaction runs the real compact operation against a
scripted provider; tree reads the tree and switches to an earlier compaction.

All numbers below are **MiB**, except payload bytes.

| Worker operation | Snapshot/page bytes | Steady HeapInuse | Peak HeapInuse | Peak HeapSys | RSS after operation | Max RSS |
|---|---:|---:|---:|---:|---:|---:|
| baseline-open | 304,616 | 76.34 | 144.06 | 155.16 | 181.66 | 181.66 |
| baseline-attach | 58,540,241 | 151.43 | 268.83 | 395.22 | 399.00 | 399.00 |
| open | 304,591 | 5.72 | 20.11 | 27.28 | 48.03 | 48.03 |
| attach | 304,616 | 5.57 | 5.59 | 27.25 | 47.36 | 47.36 |
| page | 893,536 | 5.62 | 21.02 | 31.31 | 54.91 | 54.91 |
| compact | 6,438 | 5.96 | 7.62 | 23.25 | 48.89 | 48.89 |
| tree | 6,539 | 5.78 | 29.20 | 39.28 | 63.19 | 63.19 |
| tree-unlimited | 6,539 | 5.85 | 41.93 | 55.28 | 72.66 | 72.66 |

**Attach payload fell from 58,540,241 to approximately 304,616 bytes (~192×).**
All revised worker operations meet peak RSS <150 MiB and steady RSS <80 MiB.
The soft limit is not a cap on a model context: a large active context may
legitimately require more memory. Full legacy snapshots and deliberately huge
client-supplied limits retain their inherently larger allocations.

## TUI after attach

The app harness uses the production attach path without its feature-test-only
eager tree preload. It renders the transcript and then samples live heap after
`FreeOSMemory`; the worker runs in-process, so these figures include its heap.
The baseline requests revision 2 and uses the same renderer for all items.

| TUI | Loaded items | HeapAlloc | HeapInuse | HeapSys | RSS |
|---|---:|---:|---:|---:|---:|
| Full snapshot baseline | 13,018 | 223.15 | 245.58 | 375.16 | 394.00 |
| Revision 3 tail | 69 | 4.80 | 7.00 | 23.25 | 49.59 |

The 69 items are the last post-compaction persisted items plus runtime notices.
TUI heap and RSS after attach are both below 100 MiB. A preliminary measurement
using the feature harness's tree preload showed ~35 MiB heap; a retained heap
profile identified ~23 MiB in tree DTOs and an 8 MiB scanner buffer. The final
measurement does not issue `/tree` when no user requested it. Set
`ATTO_TUI_MEMORY_PROFILE=/path/heap.pb.gz` to write a retained-heap profile.

## Headless worker

A separate subprocess records 2,000 synthetic turns, each including user text,
a tool call/result and an answer, with a synthetic compaction every 50 turns.
Model restore and the actual thread transcript handlers are exercised; this is
not 2,000 provider HTTP requests. With no attached client, every event boundary
releases completed builder items and the worker retains no display list,
block-text list or ordering map. The latest bounded model context is restored.

| Turn | HeapInuse | RSS | Max RSS |
|---|---:|---:|---:|
| 100 | 8.79 | 42.61 | 42.61 |
| 1000 | 6.54 | 44.23 | 44.23 |
| 2000 | 7.73 | 44.41 | 44.41 |

Steady memory remains flat, rather than proportional to session length. The
test then attaches, verifies the correct answer from turn 2,000 appears in the
paged tail, detaches, and checks display retention returns to zero. A separate
scripted-provider test verifies the same detach/run/attach behavior through RPC.

## Peak removal and returning unused memory

- Common opens use `core.Open`/`ReadContext`; skipped pre-compaction payloads
  are never decoded into retained strings. Display replay streams into a bounded
  builder/page, with only lightweight topology/offset metadata for old entries.
- `thread/items`, entry/resource lookup, history grep, previews, fork and branch
  summary origins read disk. Branch summaries retain only a short description of
  their start; the model already owns the context to summarize.
- Archives stream zstd; no entire decompressed temporary file is created.
- Standard `encoding/json.Encoder` still buffers an entire value on this Go
  release. Connection writes use `encoding/json/v2.MarshalWrite` with v1 wire
  options instead. WS sends bounded 64 KiB continuation frames; small replies
  remain single frames. No full snapshot byte buffer is built.
- `debug.FreeOSMemory` follows common opens, pages, compaction and navigation.
  For example, the full baseline's measured heap fell from 144.06 to 76.34 MiB
  after the measurement's release; its 181.66 MiB process high-water RSS did not
  vanish. Release alone cannot cure peaks.
- The default **32 MiB soft budget** (honoring `GOMEMLIMIT`/embedded overrides)
  improves the tree peak: the unlimited-budget control uses 41.93 MiB peak heap
  and 72.66 MiB max RSS versus 29.20 MiB/63.19 MiB with the budget. Preliminary
  versions with a 64 MiB budget still approached/exceeded 80 MiB RSS for tree
  switching despite post-operation release. Streaming removes the principal
  peak; scavenging and the budget complement it.

No final revised target was missed, so there is no failing-operation peak
profile to explain. Numerical bounds are regression tests, skipped under race;
RSS/getrusage are measured on macOS/Linux (unsupported hosts report zero for
RSS/getrusage but still enforce the heap bounds).

## Reproduce and validate

```sh
go test ./server -run 'TestLongSessionProcessMemory|TestHeadlessSessionDisplayMemory' -v -count=1
go test ./app -run TestTUILongSessionAttachMemory -v -count=1
go test ./session -run TestReadContextLargeSessionMemory -v -count=1
```

Contract coverage includes per-version snapshot shapes, empty `hasMore:false`
metadata, paging across compaction/branch navigation, page merges racing live
events without cursor movement, headless read/reattach, TUI scroll-up loading,
reducer paging and real stdio/Unix/WS e2e (including fragmented large pages).
JSONL/zstd fork/search/entry tests cover full text outside the loaded tail.

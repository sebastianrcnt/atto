# Repository benchmark

Six English questions about the read-only atto checkout at
`/Volumes/t5/atto`. Questions, exact expected answers, source-evidence commands,
and per-question regexp checkers are in `questions.json`. The source checkout
was inspected with `git ls-files` and `git grep` before writing the questions.

Run on macOS with Go and Python 3:

```sh
./bench/run.sh
# Optional:
./bench/run.sh --dir /Volumes/t5/atto --steps 30 --out bench/results
```

The runner builds atto2 into a temporary executable, then runs the questions
**sequentially** (the local model has one slot). It uses the CLI's default
`orca-local` model at `http://192.168.0.235:8081/v1`, without an API key;
`ATTO2_BASE_URL`, `ATTO2_MODEL` and `ATTO2_API_KEY` can override these.
Each checker requires a successful agent exit and checks **only the
`sys.exit` report from metrics**, not model stdout or reasoning. All listed
patterns for a question must match, case-insensitively. These are small
substring/regexp checkers, not a semantic judge.

The runner retains per-question metrics, stdout and verbose Lua/syscall traces
in the output directory, plus `summary.json`. The summary is checked in;
redundant/raw per-question artifacts are gitignored. Steps count model calls;
prompt tokens are those reported by the server for the **last** call;
syscalls include `sys.exit`, failures and rejected calls, but not malformed
model tool calls that never reach Lua.

## Recorded run

Date: 2026-10-07. Source repository HEAD:
`0308a097020fc591e5a3d32185c9a6b6bdad9f4e`.
Model: `orca-local`; base URL: `http://192.168.0.235:8081/v1`.
One pass, no retries of entire questions, default 30-step limit.

| Question | Correct | Steps | Last prompt tokens | Syscalls |
|---|---:|---:|---:|---:|
| definition | yes | 7 | 1988 | 5 |
| bash-timeout | yes | 8 | 2548 | 3 |
| instruction-cap | yes | 5 | 4835 | 4 |
| cache-key | yes | 7 | 2250 | 3 |
| error-cap | yes | 7 | 2197 | 3 |
| import-count | yes | 15 | 9355 | 11 |

**6/6 correct; 49 model steps; 29 syscalls; 0 pure runs, 29 impure runs.**
The last question's exact answer is six directories: `agent`, `app`,
`config`, `extensions`, `goal`, `provider` (nine importing production files).

## Observations

- The model treated Lua mostly as a shell-command assembly and output layer.
  It did not use `text` or `json` in this run. It did use `table.concat` to
  construct a single shell loop over a previously created nine-file Lua
  array. This excerpt is verbatim from the import-count trace:

  ```lua
  local r = sys.bash{cmd='cd /Volumes/t5/atto && for f in '..table.concat(files, ' ')..'; do echo "=== $f"; awk "/^import/,/^\\)/" "$f" | grep -n "atto/ai" ; done'}
  print(r.stdout, r.stderr, r.code)
  ```

  The concatenation was pure computation; the enclosing run was impure
  because it invoked `sys.bash`. All executed runs included a syscall.
- It tried to write an intermediate file outside its private temp directory:

  ```lua
  local r = sys.bash{cmd=[[cd /Volumes/t5/atto && git ls-files '*.go' | grep -v '_test\.go$' > /tmp/all.txt 2>/dev/null || true; wc -l /tmp/all.txt]]}
  print(r.stdout, r.stderr, r.code)
  ```

  macOS denied the write: `/bin/bash: /tmp/all.txt: Operation not permitted`.
  The model then switched to pipelines rather than asking for more access.
- The model repeatedly attempted `sys.exit` as a model-level tool (and once
  confused other syscall/tool names). The runtime's factual unknown-tool
  error was sufficient for it to correct to the sole `lua` tool. That
  overhead and several stdout-only replies explain why steps exceed Lua
  runs. The stdout-only reply reminder did not terminate the life.
- Recursive grep found duplicate files inside `.claude/worktrees`; it
  distinguished these from the main checkout. For the tracked-file count
  it used `git ls-files` and filtered `_test.go` files as requested.
- Apple's `/usr/bin/git` launcher emitted `xcodebuild` cache/FSEvents
  diagnostics in the restrictive sandbox, but git operations still
  succeeded. These diagnostics stayed in syscall stderr.

## Limits

Six source-lookup questions are a smoke benchmark, not evidence that the
model uses the pure machine deeply. Checkers can accept extraneous prose
and are not resistant to adversarial reports. More computational questions
would test the pure helpers and batching better. The sandbox is macOS-only
and provides write isolation, not read confinement; the VM still needs
memory/resource quotas and context compaction for larger investigations.

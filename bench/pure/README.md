# Pure-machine benchmark

Ten English problems with all input in the prompt. Generate the fixed-seed
problem set and Go reference answers from the repository root:

```sh
go run ./bench/pure/generate
./bench/pure/run.sh
python3 -m unittest discover -s bench/pure -p 'test_*.py'
```

The runner alternates one agent life (`-grant now,exit`, 30 model steps)
and one no-tool chat completion per problem, strictly sequentially. Both
use the same model client and default endpoint/model. No problem gives
implementation hints. Checkers match the trimmed report exactly (the runner
also supports full-match regexps). Model stdout is not the agent result.

`results/` retains metrics, stdout, Lua traces, and a combined summary.
Completion tokens are summed over all requests; prompt tokens describe the
last request. Syscalls include exit and rejected calls. Wall time includes
the CLI process. Baseline pure/impure runs and syscalls are zero because it
has no machine, not because it did any pure computation.

Results and observations will be filled after the one-pass experiment.

# atto2

An experiment: atto rebuilt as an operating system for agents. atto (1)
is frozen; atto2 starts from nothing here.

This prototype is one agent with a read-only machine:

- **agent** (`agent/`): the loop. Ask the model, run the code it wrote,
  give it the output, until it answers.
- **machine** (`machine/`): the agent's computer, a Lua 5.1 VM
  (gopher-lua) of its own. Only base, string, table and math are open;
  no os, io, load or require. Its files are a directory, which is `/` to
  the agent (a chroot). Shell-style commands print (`ls`, `cat`, `head`,
  `tail`, `lines`, `grep`, `find`, `wc`, `stat`, `pwd`, `cd`); the `fs`
  table gives the same as values for programs.
- **cortex** (`cortex/`): what the agent has in mind, the context sent to
  the model each step. Compaction and images will live here.
- **model** (`model/`): an OpenAI chat completions client. The agent has
  one tool, `lua`.

```sh
go build -o atto2 ./cmd/atto2
./atto2 -dir ~/some/project "Where is the config file read, and what keys does it take?"
```

`-base-url` and `-model` (or `ATTO2_BASE_URL`, `ATTO2_MODEL`,
`ATTO2_API_KEY`) choose the model; the default is the local
`orca-local` server.

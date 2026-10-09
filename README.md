# atto

A small terminal coding harness. You bring your own API key or model server.

## Install

**macOS / Linux**

```sh
curl -fsSL https://raw.githubusercontent.com/sebastianrcnt/atto/main/install.sh | sh
```

**Windows** (PowerShell)

```powershell
irm https://raw.githubusercontent.com/sebastianrcnt/atto/main/install.ps1 | iex
```

**With Go** (1.27+)

```sh
go install github.com/sebastianrcnt/atto/cmd/atto@latest
```

The install scripts:

1. download the binary for your platform from the [latest release](https://github.com/sebastianrcnt/atto/releases/latest);
2. check it against the release's `checksums.txt`;
3. install it to `~/.local/bin` (macOS and Linux) or `%LOCALAPPDATA%\Programs\atto` (Windows).

They don't need root. Three environment variables change what gets installed:

| Variable | Effect |
| --- | --- |
| `ATTO_CHANNEL=edge` | installs the edge build (see below); `stable` is the default |
| `ATTO_VERSION=v0.1.0` | pins that exact release; wins over `ATTO_CHANNEL` |
| `ATTO_INSTALL_DIR=...` | installs somewhere else |

Running the script again is safe. It installs the newest build of the channel over the old one.

**Edge builds.** Once a night, `main` (when its tests pass) replaces the [`edge` prerelease](https://github.com/sebastianrcnt/atto/releases/tag/edge), named like `v0.0.3-dev.14+abc1234` (the next patch version, 14 commits after the last tag, at commit `abc1234`). It's unreleased code and may break. Install it with `curl -fsSL https://raw.githubusercontent.com/sebastianrcnt/atto/main/install.sh | ATTO_CHANNEL=edge sh` (`$env:ATTO_CHANNEL = "edge"` before running `install.ps1` in PowerShell), or build from source with `go install github.com/sebastianrcnt/atto/cmd/atto@main`.

### Update

```sh
atto update          # install the latest release
atto update -check   # only check
atto channel         # show which channel this binary follows
atto channel edge    # switch to edge builds
atto channel stable  # go back to tagged releases
```

The channel is part of the binary: release builds are `stable`, edge builds are `edge`, and `atto -version` shows which (`atto v0.0.3-dev.14+abc1234 (edge)`). `atto update` stays on the channel you're on. `atto channel <name>` installs the latest binary of that channel, which then follows it. Nothing is saved in settings. Going from edge back to stable installs the latest stable release even though its version number is lower, and says so: `Switched to stable: atto v0.0.3-dev.14 → v0.0.2`. Only `atto channel` does that. Builds from `go install` or a local `go build` have no channel (they report `dev`) and get no update notices.

Once a day atto asks the GitHub API whether a newer release exists on your channel, and mentions it when you start atto. It never updates itself on its own. To turn the check off, set `"updateCheck": false` in `~/.atto/settings.json`.

If you installed with `go install` or Homebrew, update that way instead.

## Set up a model

Use a hosted provider:

```sh
atto auth set opencode       # OpenCode Zen API key
atto auth set opencode-go    # OpenCode Go API key
atto login openai            # Sign in with ChatGPT (subscription)
atto models                  # list what's available
```

Or use your own OpenAI-compatible server (llama.cpp, vLLM, Ollama, …) by adding it to `~/.atto/models.json`:

```json
{
  "providers": {
    "local": {
      "baseUrl": "http://localhost:8080/v1",
      "models": [
        { "id": "my-model", "contextWindow": 131072, "maxTokens": 32768 }
      ]
    }
  }
}
```

A provider can also set:

- `apiKey`: a literal key, or `"$ENV_VAR"` to read it from the environment
- `headers`: extra request headers
- `extraBody`: extra fields for the request body
- `api`: `"openai-completions"` (the default) or `"openai-responses"`
- `llamaCppMetadata`: `true` to opt in to llama.cpp runtime metadata discovery (off by default)
- `subscription`: `true` for a flat-rate plan, so the status line marks its cost as an estimate

For llama.cpp only, set `"llamaCppMetadata": true` on the provider. When
models.json loads (including `/reload`), atto reads `/models` beside the `/v1`
API path and uses positive runtime `n_ctx` limits in place of defaults. An
explicit model `contextWindow` (including `modelOverrides`) always wins; omit
it to use the runtime limit. Only models reported as `loaded` are inspected via
`/props?model=<id>&autoload=false` for context and chat templates containing
`enable_thinking`. Discovered thinking controls use the existing chat-template
request support (defaulting to `off`/`on` levels); explicit reasoning and
thinking-format settings take priority.
Sleeping, unloaded and unknown-status models are never queried for props.
Resolved metadata stays in the loaded config. Probe results, including failures,
are also cached in memory for five minutes across config loads (including
`/reload` and extension side calls), not probed on each request. Requests have a
two-second timeout; failures silently keep the configured/default metadata and
appear in `/debug`'s `model-metadata.txt`.
Leave this setting off for generic OpenAI-compatible servers: atto makes no
metadata requests unless you opt in.

A model can set:

- `efforts`: the reasoning levels it supports
- `effortMap`: how atto's effort levels translate into what the server expects
- `input`: `["text", "image"]` if it accepts images (default: text only; catalog models know this already)
- `cost`: prices in dollars per million tokens (`input`, `output`, `cacheRead`, `cacheWrite`), shown as the session's cost in the status line. Catalog models take theirs from models.dev; on a subscription (ChatGPT login, OpenCode Go) the figure is what the same usage would cost over the API, shown as `≈$0.123`

## Use

```sh
atto                                  # interactive session
atto "fix the build"                  # interactive, starting with this message
atto -m local/my-model                # pick the model
atto resume                           # pick a session (Enter: most recent)
atto resume ID                        # resume by ID, unique prefix or name
atto -p "fix the failing test"        # one prompt, non-interactive
git diff | atto -p "review this"      # stdin is appended to the prompt
atto -p -image shot.png "why?"        # attach images (repeatable)
pngpaste - | atto -p "what is this?"  # an image on stdin is attached too
atto -p -output-format json "..."     # also: stream-json
atto -p -goal "make the tests pass"
```

### Sessions keep running: the daemon

With the daemon enabled, one **session worker** owns each conversation's execution.
The TUI is its client: closing the terminal, losing SSH, `/quit`, or Ctrl+D on an
empty editor detaches, even during a turn. Turns, goals, jobs, timers and unanswered
prompts keep running without a terminal. **`/close` stops the session and its work.**
An unattended worker with no work retires after one minute; its saved conversation
can still be resumed.

The daemon starts on demand, runs per user and is never installed as a service
(no launchd, systemd or scheduled task). It works the same on macOS, Linux and
Windows (see below for how a Windows terminal detaches). It supervises workers only: every TUI
runs in its calling terminal process with its own editor and terminal size.
Several clients may attach and send input to the same conversation.

```sh
atto                   # new conversation in this terminal
atto resume            # this directory's sessions; Enter continues the latest
atto resume SESSION    # ID, unique ID prefix, or case-insensitive session name
atto agents            # the agent command center, including live workers
atto daemon status     # sessions, names, directories, clients, state and version
atto daemon kill SESSION # close the worker and end its work (like /close)
atto daemon stop -force # stop workers, including their work
```

The resume picker starts no worker until a session is chosen. Live workers are
marked and listed first; the cursor selects the most recently used conversation.
Esc/Ctrl+C exits without starting anything. Ambiguous names or prefixes list the
matching session IDs.

`/clear`, `/new`, `/resume` and `/fork` switch only this TUI's conversation;
accepted work in the previous worker continues. `atto app-server` routes sessions to
workers too; pass `-in-process` to use single-process operation. `atto -p -session ID`
on a live worker routes its prompt there (text/JSON; per-run overrides and
stream-json are refused with a pointer to `atto resume`). Other print runs and
agent CLI turns retain their existing execution path. `_continue` remains an
internal helper for the in-process busy-exit menu.

Daemon control protocol 4 has no PTY screen relay or pane operations. Across an
upgrade, an older running daemon triggers in-process fallback with a hint to stop
it; `atto daemon status` and `stop [-force]` still work against it.

`"daemon": false` or `ATTO_NO_DAEMON=1` keeps the in-process runtime: exit closes
its sessions and stops their jobs, and the busy exit menu still offers “Run in
background.” Normal sockets live in `~/.atto/run` (too-long paths use a private
temporary directory); logs are in `~/.atto/logs/daemon.log`.

**On Windows** the daemon and its workers are ordinary processes on a hidden console
of their own (no window appears), started by the first `atto` that needs them, so
closing the terminal window, or the SSH session, ends only the TUI: Windows sends it
a console-close event, which detaches it like SIGHUP does elsewhere. (Where the
parent environment puts every process of a session in a job that kills it on
close, the daemon leaves that job if the job allows it; otherwise it ends with the
session.) The sockets are Unix-domain sockets (Windows 10 1803 or later). With no
peer-credential call for them, a connection is trusted by the access list of the
`run` directory (a protected DACL naming only your account, which atto sets when
the directory is yours or an administrator's and refuses otherwise) and by a random
per-socket token, kept in a file beside the socket under that DACL, that a client
sends first. `atto daemon stop -force` asks each worker to close its session through
its standard input, then ends any that do not answer within 5 seconds. A logoff or
system shutdown reaches a worker as a stop request too.

The TUI reconnects after a dropped worker socket, restarting a crashed worker from
its saved conversation when needed. A live worker retains running command clocks
and pending prompts across client reconnects. A worker crash is different: unsaved
input/queue state and arbitrary in-flight extension questions are not durable yet.
After a binary upgrade, incompatible worker/daemon revisions fail clearly; idle old
workers still retire normally, and `atto daemon stop -force` can stop an older daemon.

### Keys in the session

| Key | Action |
| --- | --- |
| `Enter` | send; while the agent works, steer it after its current step |
| `Tab` | queue a message for when the agent finishes |
| `Esc` | interrupt, or send pending steers now; a running hosted model command keeps running as a job (`/jobs`) |
| `Ctrl+Enter` | while the agent works, interrupt it and send the prompt (after pending steers) as a new turn at once; an active goal is not paused but waits for you after that turn. `Ctrl+G` does the same where the terminal can't tell `Ctrl+Enter` from `Enter` (atto asks for xterm modifyOtherKeys and the kitty keyboard protocol; Terminal.app, `screen`, the Windows console and tmux without `extended-keys on` don't send it) |
| `Esc` `Esc` | on an empty prompt: open the session tree to go back to an earlier message and edit it |
| `←` | on an empty prompt: the agent command center (also `/agents`), as codex's: every atto session, the daemon's running ones and the saved ones, grouped by project, with tabs (`Tab`/`Shift+Tab`) for All, Needs you (a question is open or a goal waits for you), Working, Ready and Inactive (saved), and the selected session's last message, project, branch and first prompt on the right. `→` or `Enter` goes to it: this TUI attaches to its worker (or starts one from the saved file), detaching from the previous worker; `n` starts a new session in the selected one's project; `/` searches; `←`, `Esc` or `Ctrl+C` comes back (`Ctrl+C` closes only the center, without interrupting a running turn or shell command). Agents appear as a tree under the session that started them (agents started from a shell are shown under an “agents started from a shell” heading per project, a display group with no session behind it). `Space` folds/unfolds a tree; tabs and search include agents and reveal matching rows with their ancestors. Agent rows show their path, role, model, status and worktree branch; details show the task, last answer/report and turn tokens/duration. A running agent opens read-only with `Ctrl+R` to refresh until it finishes |
| `Shift+Tab` | cycle reasoning effort |
| `Ctrl+T` | expand everything: thinking, command groups and every command's full output; again to fold it all back (or click one block) |
| `Ctrl+B` | move the running command to the background: it keeps running as a job (`/jobs`), the agent goes on and gets an `[atto event]` when it exits |
| `Ctrl+C` | copy the selection if there is one; otherwise interrupt, clear the input when idle, or quit when the input is empty |
| `Ctrl+V` / `Alt+V` | attach the image on the clipboard (use `Alt+V` where the terminal pastes text on `Ctrl+V`, as on Windows) |

Shell commands, as in pi: start the prompt with `!` to run a command yourself, in the working directory, with the shell and environment the agent's own commands use and its output cut (the full text is saved to a temp file). It shows in the conversation as a `! command` block, and the command and its output go to the model as a user message the next time it runs (`Ran` followed by the command and its output). `!!command` runs it the same way but keeps it from the model; the block says so. The input turns green while the text starts with `!`. `Esc` or `Ctrl+C` cancels the command; `Ctrl+B` moves it to a background job. User-entered commands keep their long foreground wait (up to 30 minutes), rather than the model tool's 10–30 seconds. It runs at once even while the agent works, but its result joins the conversation only when the turn ends, so it never lands between a tool call and its result; one command runs at a time (another is refused and stays in the input). `!` alone is an ordinary message. Commands are saved in the session and come back on resume, `/tree` and fork. No hooks run for them, and `atto -p` has no such prefix.

File mentions, as in pi: type `@` at the start of a word to pick a file or folder of the project (fuzzy; a `/` in the query matches the whole path; `.gitignore` is respected). `Tab` or `Enter` inserts `@path`, quoted when it has a space; a folder keeps completing inside it. Only the path is sent: the model reads the file itself.

Pasting the path of an image file, or dropping the file on the terminal, attaches it too. Images show as `[image 1: 1024x768 PNG]` in the input; delete the placeholder to drop the image. Images larger than 2048 pixels are scaled down. Clipboard images need `osascript` (macOS; `pngpaste` is used if installed), `wl-paste` or `xclip` (Linux), or PowerShell (Windows, WSL).

`atto -p` attaches images given with `-image` and an image piped to stdin (PNG, JPEG, GIF or WebP, recognized by its first bytes); the prompt argument is then the text. Over JSON-RPC (`atto app-server`), `turn/start` takes `images: [{mimeType, data}]` (base64 or a `data:` URL, at most 10 of 10 MB each). Either way the model must accept images.

Pastes over 1000 characters show as `[Pasted Content 1234 chars]` and are sent in full.

Commands the agent runs: while one runs, its block shows the command and the last lines of output (the line above the input then shows a made-up verb, as `Blorping…`, one per turn, and the turn's tokens so far, `↑ 8.1k  ↓ 1.2k tokens` (input the server had not cached; output: thinking, text and the commands being written); see `spinnerVerbs` below). Once it ends it folds to one line, `✓ description · time  $ command`; a failed one (non-zero exit, timeout, canceled) keeps its last two output lines. Click the line for the full command and the output's first and last lines, `… +N lines` for all of it. Commands run one after another, with only the model's thinking between them, group: the ones before the last fold into one dim line, how many and their descriptions, `▸ 5 commands · 12s  Read main.go, Search for TODOs, …`, with `· 1 failed` in red when some failed (those stay shown below it, as does the last command). Click it, or press `Ctrl+T`, to see every command and thought of the group on a shaded background. Set `"toolGroups": false` in `settings.json` to show each command on its own.

### Mouse and selection

In the fullscreen renderer atto handles the mouse itself: the wheel scrolls the conversation, and a click on a block's header or its `+ N lines` line expands or collapses it.

- Drag to select text anywhere in the conversation; it is copied when you let go. Dragging past the top or bottom scrolls.
- Double-click selects a word (a file path or a URL is one word), triple-click the line.
- With a selection showing, `Ctrl+C` copies it instead of interrupting and `Shift+arrows` extend it. `Esc`, `PageUp` and `PageDown` keep it; other keys and clicks clear it.
- Copying uses every way that applies: the system clipboard (`pbcopy`, `wl-copy`/`xclip`/`xsel` and the PRIMARY selection, PowerShell), the tmux paste buffer inside tmux, and OSC 52, which is what reaches your clipboard over SSH. A short note says which worked.

Inside tmux, atto only gets the mouse with `set -g mouse on` (it says so at startup when it is off). OSC 52 through tmux needs `set -g set-clipboard on` or `set -g allow-passthrough on`.

To use the terminal's own selection instead, hold the key that bypasses mouse reporting: `Shift` in most terminals (Windows Terminal, GNOME Terminal, Konsole, kitty, WezTerm, Alacritty), `Option` in iTerm2, `Fn` in Terminal.app. Over SSH or in tmux it is the key of the terminal you are sitting at. Or turn mouse handling off with `"mouse": false` in `settings.json` or `ATTO_NO_MOUSE=1`; scrolling then works with `PageUp`/`PageDown`.

### Slash commands

| Command | What it does |
| --- | --- |
| `/model` | switch model mid-session |
| `/effort` | set reasoning effort |
| `/tui [auto\|fullscreen\|inline]` | show or change the renderer; saved to `settings.json` and applied at once |
| `/compact` | compact the conversation now |
| `/copy` | copy the last answer; works over SSH in terminals with OSC 52 |
| `/context [system\|long\|normal]` | show context and cache use; allow long context or restore the tier cap |
| `/reload` | read AGENTS.md, skills, hooks, extensions, MCP servers, `settings.json` and `models.json` again, keeping the conversation |
| `/extensions [approve <name>]` | list extensions, or approve a project extension |
| `/diff [--staged] [path]` | show what changed in the working tree: a summary, then the diff (a native command, see `extensions/native_diff.go`) |
| `/resume` | switch sessions in the agent command center (All tab) |
| `/sessions` | the session picker: search, this directory or all, archive (`ctrl+x`), rename (`ctrl+r`) and preview saved sessions |
| `/tree` | go back to any point of the session; earlier branches are kept |
| `/fork` | start a new session from an earlier message |
| `/name` | name the session; the name shows at the right end of the input's top rule, as in Claude Code |
| `/autorename` | have the current model name the session from what it is about |
| `/archive` | archive the session and start a new one |
| `/clear` | start a new session |
| `/goal [<objective>\|clear\|edit\|pause\|resume]` | set or view the goal for a long-running task, as in codex: bare `/goal` (or `status`) shows it with the time and tokens used, `help` shows the usage, `edit` opens a prompt, a new objective asks before replacing an unfinished goal. The words help and status alone never become an objective. Clearing or pausing while a turn runs is told to the model. A message sent while the goal is waiting, paused, stalled or usage limited carries a short note saying so, so the model answers instead of resuming goal work; a message sent while a goal turn runs says the goal is still active. A turn that fails for any reason a retry might fix (anything but an interrupt, a usage limit, an authentication failure or a request the provider rejected) is retried after 10s, 30s, 1m, 2m, 5m and 10m before the goal stalls (each turn has already sent a failed request up to 5 more times itself); Esc, `/goal pause` and `/goal clear` end the wait. The status shows at the right of the status line ("Pursuing goal (14m)"), Esc pauses it, and opening a session with a paused or stalled goal asks whether to resume |
| `/agents` | the agent command center (as `←` on an empty prompt) |
| `/close` | stop this session and its work, then exit the TUI |
| `/remote` | prints that the web UI is being rebuilt and that `atto app-server --listen ws://HOST:PORT` serves the protocol meanwhile |
| `/jobs`, `/stop` | list or stop background jobs |
| `/timer`, `/timers` | wake the agent later, or list pending timers |
| `/quit` | exit atto |

## How it works

**One tool.** The model works through a single shell tool: bash on macOS and Linux, PowerShell on Windows. Hosted commands wait in the foreground for 10 seconds by default; the model can set `timeout` up to 30 seconds. A command still running then becomes a background job, with its id and output so far returned to the model. Long builds and tests keep running: the model can use `atto job wait <id> -timeout 10m`, read `atto job output <id>`, or wait for the exit event. `run_in_background` starts a job immediately; `Ctrl+B` still moves the running command to a job at once. Without a shell host or session, commands cannot detach and retain the kill timeout (60 seconds by default, up to 30 minutes). Interrupting a turn (Esc, Ctrl+Enter, a remote interrupt or `atto agent interrupt`) also moves a still-running hosted model command to a job, recording the job id and output tail for the next turn. An interrupt-detached job's exit event waits for the next turn if the session is idle; requested, timed-out and Ctrl+B jobs still wake the agent as before. `/jobs` lists them and `/stop` stops them. Closing a session (or exiting in-process mode) stops its jobs; detaching a worker client does not; ordinary cancellation, hooks, direct runs and standalone `atto -p` Ctrl+C do not detach commands. Everything else is a command it can run:

- `atto history grep` searches the session transcript, including turns that were compacted away.
- Command output is streamed to disk as it comes, never held in memory. The model gets the first and last ~10k tokens (`toolOutputTokenLimit`) and, when it had to cut, a note naming the full output: a zstd file `~/.atto/outputs/<session id>/<call id>.log.zst` that it reads with `atto output <path|call-id> [-head N|-tail N|-grep RE]` (`!` commands are cut the same way, and the app server's `item/output` reads their file). Output that fits is never written. A file keeps the first and last 32 MiB of a command's output, with a `[... N bytes (M lines) omitted ...]` line for the middle of a longer one; all files together stay under 1 GiB (the oldest are deleted first, about once a minute, never one a running command is writing), and nothing is written while the disk has less than 1 GiB free (the model is then told the output was not saved). A command that becomes a background job keeps its output in the job log (`atto job output`, at most 16 MiB) instead. Deleting a session does not yet remove its `outputs/<session id>` directory; `outputs.RemoveSession` does. Files `atto` wrote to the temporary directory in earlier versions (`atto-bash-*.log`) are plain text and are never removed automatically.
- `atto job start` runs a command in the background. With `-notify REGEXP` (and `-notify-limit N`, default 50) each matching output line wakes the agent while the job keeps running; matches within a second are batched.
- `atto monitor` and `atto timer` wake the agent when something happens. `atto timer every 30m [-count N] [-until HH:MM|duration] <message>` repeats (minimum 1m, no drift; missed intervals fire once).
- `atto goal [status]` shows the goal; `atto goal complete|blocked|pause "<why>"` reports on the goal (done; stalled on the same blocker for three goal turns; paused at the user's request). A turn that fails stalls the goal (after the retries above, for a transient error), and one that hits the provider's usage limit marks it usage limited; `/goal resume` continues either.
- `atto reload` reloads the session's AGENTS.md files, skills, hooks, extensions, MCP servers and settings after the agent edited them; the result comes back as an `[atto event]`.
- `atto view <image>...` lets the model see an image file it made, a screenshot or a rendered plot: the image is attached to the result of the command that ran it, the way your own images are (PNG, JPEG, GIF or WebP, scaled to fit 2048 pixels, at most 8 per command). It works only in a foreground command of the agent, including an agent's, not in a background job or your own shell. The command's block shows `▣ shot.png 1136×1038` per image. Chat completions servers get the images in a user message after the tool results, as pi does; the Responses APIs in the tool result itself. A model without image input gets a note instead and nothing is attached; images are saved with the session like yours, and compaction drops them.

**Nothing loads unseen.** When a session starts, resumes or forks, the conversation opens with a dim "Loaded" block: the AGENTS.md (or AGENTS.override.md, CLAUDE.md) files in the system prompt with their sizes (and whether the 32 KiB cap cut them), files that were found but skipped and why, the skills and where they came from, the hooks, the extensions, the settings and models files read, and the model and effort with where each came from (`-m`, the session, `settings.json`). Click its header or press `Ctrl+T` for the full list. `/reload` shows it again with what changed. The same report:

- `atto context` prints it for the current directory (`-json` for the data).
- `atto -p -v` prints the one-line-per-kind summary to stderr at the start, and stream-json's `init` event has it as `context`.
- The server's `thread/start` and `thread/resume` results include it as `context`.

**Prefix-cache friendly.**

- The history is append-only.
- The system prompt and the tool schema don't change during a session, unless `/reload` (or `atto reload`) finds that AGENTS.md files or skills changed; the next request then reads the new prompt in full, and the reload says so.
- Compaction keeps the latest user messages plus a summary, the way codex does it. Run `/context` to see the cache hit rate.

A model request that fails mid-turn is sent again, up to 5 times with a growing wait (the provider's Retry-After when it gives one): a dropped stream, a 5xx, a rate limit or an error atto does not know. What streamed before the failure is not kept. It is not retried after an interrupt, a usage limit, an authentication failure or a request the provider rejected; a request that no longer fits the context window is compacted once and sent again instead. Each retry, a request that fails for good and one that gets through after retries are logged in `~/.atto/logs/requests.log` (JSON lines, with the local and remote address of the connection used).

Auto-compaction starts at 90% of the context window, or earlier to leave room for the model's maximum output. It is checked before every request of a turn, and a new message counts toward it before it is added. If a model's prices increase above a context size, atto also caps the trigger at 90% of the first positive price-tier boundary. This works with catalog prices and `models.json` `cost.tiers`, for every provider. For example, a 272k tier compacts at 244.8k instead of entering the long-context surcharge band.

`/context` reports the trigger and its reason. When a tier or `compaction.limits` lowered the trigger, the compaction block says so (`Context auto-compacted · price tier above 272.0k`), and the status line shows the trigger after the context size (`120.0k/1.1M ⇥244.8k`). `/context long` ignores the price-tier cap for this session; `/context normal` restores it. The choice survives resume and applies across model switches, but `/clear` starts in normal mode. The status line shows `long` beside the context percentage and `×2` (rounded input-price multiplier) beside cost when the last request used a surcharge tier. Context percentages always refer to the full window.

For a persistent per-model override, add `compaction.limits` to `~/.atto/settings.json`:

```json
{"compaction": {"limits": {"openai/gpt-6-luna": 0, "local/my-model": 200000}}}
```

Keys are exact `provider/model` IDs. A positive cap compacts at 90% of that many tokens, never later than the window/output limit; `0` or a cap at least as large as the context window disables the tier cap, not auto-compaction. Missing keys use the model's prices. `/context long` bypasses these caps too; `/reload` applies settings changes.

**Sessions** are JSONL files under `~/.atto/sessions/`. Archiving stores them as zstd-compressed `.jsonl.zst` files under `~/.atto/archived_sessions/`, keeping the same date layout. Listings, previews, history search and archived agent transcripts read them transparently. Unarchiving restores the original JSONL bytes; resuming an archived session restores it first. Existing plain archives remain readable; they are never compressed automatically. As in pi, entries form a tree: going back with `/tree` starts a new branch in the same file and keeps the old one. When that leaves work behind, atto asks whether to summarize the branch being left (optionally with your own instructions); the current model writes the summary, `Esc` cancels it, and the model sees it on the new branch. `"branchSummary": {"skipPrompt": true}` in `settings.json` never asks. `atto history grep` searches every branch and marks entries on other branches; `-active` limits it to the current one.

Manage sessions from the shell, without the TUI:

```
atto resume [id|name]                resume a session (no argument: picker; IDs may be unique prefixes)
atto sessions [-all] [-archived] [-json] [-n N]   list this directory's sessions (-all: every directory)
atto sessions show <id>              details and the last user messages
atto sessions rename <id> <name>
atto sessions archive|unarchive <id>  compress into the archive, or restore JSONL
atto sessions compress              migrate all legacy plain archives; print count and bytes before/after
atto sessions delete [-y] <id>       permanent: also removes its jobs, inbox, goal and images no other session uses
```

`delete` asks first on a terminal and refuses without `-y` elsewhere. Inside an atto agent only `list` and `show` work, so a model can't destroy session history.

A session is open in one atto at a time: while a terminal has it, resuming it in another (or with `atto -p`, or from `atto app-server`) is refused with the pid that holds it, since two writers would undo each other's work and goal.

**Hooks** use the same format as Claude Code: `PreToolUse`, `PostToolUse`, `UserPromptSubmit`, `Stop`, `PreCompact`, `SessionStart`, `SessionEnd` and `Notification`.

- Put them in `~/.atto/settings.json` or in the working directory's `.atto/settings.json`. User hooks are your own and need no approval; project hooks stay off until approved (see **Project code approval** below).
- A hook that exits with code 2, or returns `{"decision": "block"}`, stops the action.
- `Stop` runs when the agent is done answering (in the TUI, `-p` and the servers). Blocking it sends the reason to the model as a user message and the turn continues. The input has `stop_hook_active`, true once a Stop hook has already kept this turn going, so a hook can let it finish. After 8 blocks in a row atto stops anyway. An interrupted turn (Esc) runs no Stop hook. With `/goal`, the Stop hook runs at the end of every turn, before the goal decides whether to continue.
- `SessionEnd` runs when a session ends, with `reason`: `exit` (quitting the TUI), `clear` (`/clear`), `resume` (switching to another session), or `other` (`-p` finishing, the server shutting down, a conversation archived). It cannot block and has a 5 second default timeout (set `timeout` on the hook to change it), so exiting stays quick. The matcher is tested against `reason`.
- `Notification` runs when atto wants your attention, with `message` and `notification_type`; the matcher is tested against the type. It cannot block. The TUI sends `idle_prompt` when a turn that took 15 seconds or more is done and atto waits for your input (not when a queued message or a goal turn follows), `background_event` when a job or timer event arrives while atto is idle, and `goal_blocked` when a goal becomes blocked. There are no per-command permission prompts, so no such notification. `-p` and the servers send none.
- Hook messages, such as the reason of a blocked Stop, appear in the transcript, and as `hook` events in `-p --output-format stream-json`.

**Project code approval.** A repository may bring executable hooks, MCP servers or extensions. At startup, and when `/reload` finds new or changed content, the TUI asks **once**, listing all pending items together: **Allow all**, **Review one by one**, or **Deny**. Allow all approves only the content shown, not future items or changes. Review offers Allow/Deny for each item. Decisions are remembered by content hash; unchanged denials stay off without asking again. `Esc` means “not now”: content stays off and is not asked about again until the next atto run (or until it changes).

Where nothing can ask (`atto -p`, `atto app-server`, background workers), unapproved content stays off and a warning names each item and its approval command. Approve ahead of time from your own terminal:

```
atto trust                            # list project items and their decisions
atto trust list -json
atto trust approve all                # approve current content, not future changes
atto trust approve hook <name>        # use the hook name printed by atto trust
atto trust approve mcp github
atto trust approve ext deploy
atto trust revoke mcp github          # forget a decision; needs approval again
atto trust revoke all
```

`atto trust` refuses when `ATTO_AGENT` or `ATTO_SESSION_ID` is set: the agent cannot grant itself trust. Existing `atto mcp approve <name>`, `atto extensions approve <name>` and `/extensions approve <name>` still work. Use `/reload` to apply approvals or revocations in a running session. Invalid or disabled items are listed but are not prompted for or approved by Allow all.

Approvals live beside your settings: `hook-approvals.json`, `mcp-approvals.json` and `extension-approvals.json` under `~/.atto` (or `ATTO_DIR`). Hook hashes cover the event, matcher, command or HTTP configuration, headers and timeout; they do **not** hash a script invoked by the command. MCP hashes cover the entry before environment expansion; extension hashes cover the bundled code, including imports. Hook approvals are scoped to their settings file and content, so reordering identical hooks does not need approval. User hooks, user extensions and user/local MCP servers need no project trust. AGENTS.md and skills are text, not executable content, and remain listed in the Loaded block without a prompt.

This is approval of repository-supplied code, not a sandbox or a per-command permission system. It does not prevent prompt injection in project instructions or stop the model from running commands.

**Extensions** are TypeScript/TSX or JavaScript/JSX files run by esbuild + goja
(no Node.js/React). They can watch or gate shell commands, redact model-visible
output, add prompt context and slash commands, or draw portable UI. Full builds
expose `atto.ui`: typed panes, bands, status slots, toasts, dialogs and transcript
overlays, composed through `render`, `resolve` and `next()`. Drawings never
change conversation/model data; callbacks remain session-owned in the worker.

A shared, persisted counter pane (`~/.atto/extensions/counter.tsx`):

```tsx
export default function (atto: Atto) {
  let count = 0;
  atto.on("session_start", async () => {
    count = (await atto.store.get<number>("count")) ?? 0;
    atto.ui.invalidate({ site: "pane", id: "counter" });
  });
  atto.ui.render({ site: "pane", id: "counter" }, e => {
    const { Box, Text, Button } = atto.ui.resolve(e);
    return <Box gap={1}>
      <Text text={`Count: ${count}`} />
      <Button key="more" label="Add one" hotkey="a" onPress={async () => {
        count++;
        await atto.store.set("count", count);
        atto.ui.invalidate({ site: "pane", id: "counter" });
      }} />
    </Box>;
  });
  atto.registerCommand("counter", { handler: () =>
    atto.ui.open({ site: "pane", id: "counter", title: "Counter", focus: true }) });
}
```

Wrapping native tool rows (plain `.ts` constructors work identically):

```ts
export default function (atto: Atto) {
  atto.ui.render({ site: "toolCall" }, async (e, next) => {
    const { Box, Text } = atto.ui.resolve(e);
    const original = await next(e);
    return Box({ children: [original, Text({
      text: "Reviewed by my extension", color: "muted",
    })].filter(x => x !== null) });
  });
}
```

- Files can be `name.ts/.tsx/.js/.jsx` or `name/index.*`, in
  `~/.atto/extensions/` or the project's `.atto/extensions/`. Project code needs
  approval (`/extensions approve <name>`); user code does not.
- `/reload` retires registrations/callbacks, cancels dialogs and loads changes.
  The Loaded block lists status, commands/events and errors with `file:line`.
- Render hooks are fast and read-only: no I/O or store writes. Async composition
  through `next()` is supported; errors/timeouts fall back to built-in drawing.
  `atto.store` is JSON-only and session-scoped (64 KiB/value, 1 MiB/extension),
  survives reload/resume/fork, and requires explicit invalidation after writes.
- `notify/select/confirm/input` remain notice/dialog helpers. Worker dialogs
  wait for clients; standalone `-p` keeps undefined/false defaults. Non-UI hooks,
  `atto.exec`, `atto.fs`, `atto.complete`, fetch, timers and MCP remain available.
- `atto extensions docs` prints [the guide](docs/extensions.md), `types` the full
  declarations, and `source diff` the native Go implementation. See
  [examples/extensions](examples/extensions) and [the UI contract](docs/ui.md).
  Slim (`noext`) builds retain all Go UI/built-ins but do not run extension code.

**Skills** are read from `~/.atto/skills` and the project's `.atto/skills` (the first of a name wins), and from nowhere else: directories other tools share, such as `.agents/skills` and `.claude/skills`, hold what was installed for those tools.

**Built-in skills.** atto ships skills of its own (Agent Skills, one `SKILL.md` each), listed in the system prompt like yours and shown as `builtin` in the Loaded block and `atto context`. Today there is one, `atto-extensions`: when you ask to customize atto, the model reads the extension API with `atto extensions docs` and `types`, writes the extension, runs `atto reload` and fixes load errors. Because the model reads skills as files, they are written to `~/.atto/cache/skills/<hash>/<name>/SKILL.md` (the hash changes when the text does). A skill of the same name in `~/.atto/skills` or the project's `.atto/skills` overrides a built-in one; `/skill:atto-extensions` runs it by hand; to turn one off: `{ "skills": { "disabled": ["atto-extensions"] } }` in `settings.json`. `atto extensions source diff` prints the Go source of the native `/diff` command.

**MCP.** atto has one tool, the shell, and keeps it that way: MCP servers are reached through `atto mcp` subcommands that the model runs in the shell, not through tools of their own. The tool schema and the system prompt stay the same whatever you configure (when at least one server exists the prompt gets one line naming them, in sorted order, so it changes only when your configuration does), and the prompt cache survives.

```
atto mcp add github -scope user -e GITHUB_TOKEN='${GITHUB_TOKEN}' -- npx -y @modelcontextprotocol/server-github
atto mcp add docs -url https://mcp.example.com/mcp -H 'Authorization: Bearer ${DOCS_TOKEN}'
atto mcp list                                 # servers, scope, status, tool count
atto mcp tools                                # every tool, one line each
atto mcp tools github create_issue            # one tool's full JSON schema
atto mcp call github create_issue '{"repo": "me/x", "title": "Bug"}'
echo '{"query": "atto"}' | atto mcp call docs search -    # arguments from stdin
```

- **Configuration** is Claude Code's `.mcp.json` format, `{"mcpServers": {"name": {"command", "args", "env"} | {"type": "http", "url", "headers"}}}`, in three places: `~/.atto/mcp.json` (user), `<project>/.mcp.json` (shared, checked in) and a private per-project file, `~/.atto/projects/<project name>-<hash>/mcp.json` (local, written by `atto mcp add -scope local`, outside the repository). The shared, checked-in file is `.mcp.json` and needs approval; a `<project>/.atto/mcp.json` is not read at all (the Loaded block says so and where to move it), because a repository must not be able to start commands unapproved. The later wins by name (local over project over user). Strings may use `${VAR}` and `${VAR:-default}`, which is how tokens stay out of the files. A project's `.mcp.json` works as it does in Claude Code; `~/.claude.json` is not read. Transports are stdio and streamable HTTP (`"type": "http"`; `"sse"` also works). Remote servers that need OAuth are not supported; use a header token.
- **Approval.** A server from a project's `.mcp.json` needs approval, together with project hooks and extensions in the combined project-code prompt above. Or run `atto mcp approve <name>` / `atto trust approve mcp <name>`. Approvals remain in `~/.atto/mcp-approvals.json` by file and server name with a hash of the entry; a changed entry needs approval again. Unapproved servers are listed but never started. `atto mcp approve` refuses when run by the agent (`ATTO_AGENT` is set). Servers in your own user and local files need no approval. Old approvals of an entire file are still honored for compatibility; new Allow all decisions approve only current hashes, and `atto trust revoke` can remove old whole-file approvals.
- **Servers live in the session.** They start on first use and stay until the session ends, so a stateful server is not restarted per call. `atto mcp call` and `tools` run by the agent talk to the running atto over a Unix domain socket (`~/.atto/mcp/<session id>.json` holds its path and a random token, mode 0600; it works on Windows 10+ too). Run from a normal terminal, a server is started for that one command and stopped after. `/reload` (or `atto reload`) re-reads the files, keeps servers whose entry did not change and restarts those that did.
- **Transparency.** The Loaded block and `atto context` list every server with its scope, transport, command or URL, and status (not started, running with N tools, failed with the reason, needs approval). The calls are ordinary shell commands, so they appear as normal tool blocks and a `PreToolUse` hook with matcher `Bash` can gate them (for example by looking for `atto mcp call github`). Extensions can use the same servers: `await atto.mcp.call(server, tool, args)` and `atto.mcp.tools(server?)`.

<!-- Compatibility: document the older settings spelling for existing installs. -->
**Agents** are atto sessions other agents start, as in codex's multi-agent mode: equally capable, with the same tools, each working in the background on what it is sent. The model starts and talks to them through its shell like everything else. They are always available: there is no setting that turns them on or off (the old `"agents": {"enabled": …}` key, and `maxDepth` and `maxConcurrent`, are still read without error and ignored). The system prompt always tells the model about them, to start them on its own judgement when they help (independent work in parallel, a separate worktree for a risky change) and not for a small task.

**Standalone or agent?** These are different kinds of session:

| Command | Session |
| --- | --- |
| `atto -p "..."` | Standalone, headless: no parent, no path, not in any agent tree. Saved unless `-no-save`. |
| `atto agent spawn NAME "<task>"` | An agent, which is its session: its session ID is its identity. Below the session that ran the command, with a path such as `/root/NAME`; from a plain shell it has no parent and is the root of a tree of its own. `wait` / `report` / `list`, and an optional git worktree (`-worktree`). |

`agent` works directly from a plain shell: no running atto session or `atto -p` root is needed, and no setting to switch on.

From a plain shell, a typical worktree run is:

```sh
atto agent spawn NAME "task" -worktree -m openai/gpt-6.1-sol
atto agent wait -json
# Review the branch shown in the report.
atto agent close NAME
```

`close` removes the worktree and keeps the branch for you to merge.

For an outside orchestrator, remember the **agent's own address** from the
started line, not just its name:

```sh
atto agent spawn panes "task" -worktree
# agent panes started (@eff39362, session eff39362, ..., project /src/p, job 1 of session eff39362).
agent_addr=@eff39362                 # save the address returned by this spawn
cd /some/other/directory
atto agent send "$agent_addr" "also check the tests"
atto agent wait "$agent_addr" -json -timeout 10m
atto agent report "$agent_addr" -json
atto agent close "$agent_addr"
```

An agent started from a plain shell has **no parent**: it is the root of a tree
of its own (`/root`), recorded with the project (git root, else the directory).
No session is made for the caller and nothing is sent to anyone when its turn
ends: poll it with `wait` and `report`. Two orchestrators can both spawn `panes`
in the same project; each agent has its own session ID, worktree and branch.
Remember the returned `@ID`: it reaches exactly that agent from anywhere, with
no `-session` needed. `-session ID` instead makes the new agent a child of that
session, and its answers then reach that session.

Without `-session`, bare names are **project-wide labels** of the open agents
started from a shell, not a shared namespace. `atto agent wait panes` works when
exactly one open agent started from a shell is named `panes` in the current
project. When several match, the error lists their addresses, statuses and ages,
for example `2 agents named panes: @eff39362 (running, 2m); @3fa9c2e1 (done, 1h); use @id`.
A name with no match explains how to list agents. Paths such as `panes/lint`
first resolve the `panes` label, then follow that agent's own tree, and `/root/panes`
is accepted as a spelling of the label (bare `/root` and `..` need `-session`,
since outside there is no selected tree). `atto agent list` shows those agents and
their trees in the current project (spelled `panes`, `panes/lint`, never as one
project-wide tree); `list -all` includes all projects. `wait` without addresses
waits for any of the project's agents started from a shell, and `close -done`
closes those whose whole tree is finished. The command center shows them under one
"agents started from a shell" heading per project: a display group with no session
behind it.

```
atto agent spawn NAME "<task>" [-role R] [-worktree]
                                    start one in the background; returns at once
atto agent task AGENT "<text>"      a new task: a turn now if it is idle, else after its current step
atto agent send AGENT "<text>"      a message that starts no turn: read after its current step,
                                    or with its next turn
atto agent wait [AGENT...] [-timeout 10m]
                                    block until one of them finishes a turn (exit 124 on timeout)
atto agent list [-all]              the agents you started, and theirs, with IDs and addresses
atto agent report AGENT             its last answer, status, duration, tokens (and ≈cost)
atto agent interrupt AGENT          stop its running turn
atto agent close AGENT... | close -done [-force]
                                    remove agents you are done with, and theirs; sessions archived
atto agent roles                    what -role picks from
```

- **Trees and addresses.** Each agent has a path from the root of its tree: the session that started the first ones is `/root`, its agent `tests` is `/root/tests`, and that one's `lint` is `/root/tests/lint`. `AGENT` is a name you gave, a path below you (`tests/lint`), `..` for the agent that started you, or a full path (`/root`). Names and paths are labels that can be reused after an agent is closed; the **session ID is the identity** and never is. You can also use `@<session id>`: the agent's own full session ID, or a unique prefix of at least 6 characters. Bare hex IDs are still names, not ID addresses. Prefix ambiguity lists the matching candidates, closed IDs included; an unknown ID suggests `atto agent list` (outside atto, `list -all`). Inside an atto session or model shell, `@ID` can only name agents in the caller's own tree (and the session that roots it); an agent in another tree is reported as not found. Outside atto, `@ID` can reach any agent regardless of directory, parent, or `-session`. `spawn` prints the `@ID` address, the project and the job in its started line, and `list` shows short `ID` and `ADDRESS` columns. A closed ID reports "closed" rather than following a reused name; archived transcripts remain available through the session/history commands and the agent command center.
- **Messages.** What one agent sends another arrives wrapped in `<atto_internal_context source="agent">` with a `Message Type` (`NEW_TASK`, `MESSAGE` or `FINAL_ANSWER`), `From` and `To`. When an agent's turn ends, its final answer reaches the session that started it by itself (`FINAL_ANSWER`, cut at 8000 characters; `report` has all of it), and wakes it as a job's exit does. `task` starts a turn; `send` doesn't: a message to an idle session waits in its inbox for its next turn (a running one takes it after its current step, but a turn that has finished is not kept going for it). `wait` returns early when you send a message, so you are never stuck behind it.
- **Nesting.** There is no depth limit and no concurrency limit: any agent may start agents of its own, at any depth, and every queued turn starts at once (`agents.maxDepth` and `agents.maxConcurrent` no longer exist; old values are ignored). A turn is queued only behind the same agent's previous turn, since one agent runs one turn at a time. Closing an agent closes the agents below it.
- **From a normal shell**, every command accepts `-session ID`. See above for what an agent started without it is.
- External callers can set the model and effort on `spawn` with `-m provider/model -effort LEVEL`; in atto's model shell (`ATTO_SESSION_ID` / `ATTO_AGENT` set) these flags are refused and models pick roles. `read` and `show` are aliases of `report`. `wait` and `report` accept `-json` for one object with `name`, `status`, `turn`, `duration` (seconds), `tokens` (`in`, `cached`, `out`), optional `cost` (estimated USD), `session`, `model`, `message` and optional `error`, `worktree` and `branch`, plus `path`, `parent`, `root`, `depth`, `role`, `project`, `origin`, `lifecycle`, `job`/`jobOwner` (the job running the turn) and `spawnedBy`.
- **Where turns run.** With the daemon, an agent's turns run in the worker of the agent's own session, like every other session: `atto agent` finds or starts that worker and asks it for the turn, and a TUI or app-server client can attach to a running agent live (`atto resume ID`, or Enter in the command center) instead of reading a locked transcript. The worker stays while a turn runs or waits and retires when idle like others; an idle agent is not woken by what lands in its inbox, only by `task`. Without the daemon (`ATTO_NO_DAEMON=1`, `"daemon": false`) each turn is a background job process, `atto _agent-turn`, as before, and the same when the session's writer lease is held elsewhere. A worker takes its environment from the process that started it, so an API key exported only in the shell of one `atto agent` call may not reach an already-running worker.
- An agent is its own session (in the spawning checkout's directory, or its own worktree with `-worktree`) that sees only what it is sent. Each turn runs headless as a job: of the session that started it for a child (`atto job list` shows `agent NAME`), of the agent's own session for one started from a shell; `report -json` and `list` show the job and its owner. An agent's own agents keep running when its turn ends. The session header carries an `agent` object (parent or null, root, depth, path label, name, role, spawn directory, project, origin, and `spawnedBy`); runtime state is `~/.atto/agent-state/<session ID>.json` (with `.turn.json`, `.turn.json.interrupt` and `.turn.lock` beside it), one flat file per agent, no per-parent directories. **spawnedBy** records who started the agent: the session (or null from a plain shell), that session's model and effort when it did (read from the session's own record, not its environment), its turn number, the `ATTO_TOOL_CALL_ID` of the command and the directory, with origin `model`, `outside` or `explicit-session`. `list` shows a compact `BY` column (`sol·high t3`), `report -json` the object, the command center the details. It is tracking, not proof: environment variables can be changed by the model. Agents' sessions are kept out of the default `atto resume` and `atto sessions` listings, including agents started from a shell, but appear in the agent command center under their parent, recursively. Working includes running/queued agent turns; idle or completed agents are Ready, failed/stopped or closed agents Inactive. Closed agents keep their record (the ID stays reserved, the name is free) and their archived transcripts in the center. The center opens a locked agent transcript read-only (banner and `Ctrl+R` refresh); once unlocked, refresh opens it normally.
- **Worktrees.** `spawn -worktree` gives the agent a git worktree of its own, so agents editing files in parallel don't clobber each other or your checkout. It is made from the spawning checkout's `HEAD` (committed work only) on a new branch `atto/<session ID>`, at `~/.atto/worktrees/<session ID>`: the ID is chosen before any git work and never reused, so closing an agent and reusing its name cannot collide. The worktree is outside the project, so nothing shows up in its `git status` or searches, and short enough for Windows paths. Agents started before keep the worktree paths and branch names they were made with (`worktrees/<parent>/<name>`, `atto/<parent>/<name>`); they are recorded with the agent and always used from there. The agent works at the same place in it as the spawning checkout and is told to commit there. It needs a git repository with a commit. `report`, `list` and `-json` show the worktree and branch. `close` runs `git worktree remove` and keeps the branch, printing it and its new commits for you to merge; while the worktree has uncommitted changes `close` refuses and lists them, unless `-force`. A spawn that dies halfway is rolled back by the next one (journaled in `agent-state/.coord/spawn`).
- **Roles** set an agent's model, effort and instructions (`-role`, default `general`, which uses the parent's model and effort, or `agents.model` / `agents.effort` from `settings.json`). Add roles as Markdown files in `~/.atto/agents/` or the project's `.atto/agents/` (the project wins on the same name, and either replaces the built-in `general`):

  ```markdown
  ---
  name: reviewer
  description: reviews a diff for bugs
  model: anthropic/claude-sonnet-4-5
  effort: high
  ---
  Review the change you are given. Report bugs with file:line, most serious first.
  ```

  The body is added to the agent's system prompt; the names and descriptions are listed for the sessions that may start agents. `atto context` shows them too.
- The old command names still work: `start` (with `NAME PRESET "<task>"` too), `next`, `steer`, `wait-any`, `stop`, `rm`, `presets`.

**Front end and back end are separate.** Clients attach to the session runtime over one JSON-RPC protocol, built around threads, turns and items (revision 3 is the only one served). `atto app-server` serves it over JSON-lines stdio by default, or `--listen unix:///tmp/atto.sock` (0600, removed on exit), or `--listen ws://127.0.0.1:7878` (one JSON-RPC message per text message). Socket disconnect is detach, not stop. The terminal UI is one such client. A new web UI is planned; until then `atto serve` and `/remote` only print that it is being rebuilt, and `atto app-server` is the way to attach other clients. The [client-author protocol reference](docs/protocol.md) covers every method, notification, handshake, cursor and prompt; a [small Python client](examples/clients/README.md) shows how to attach. `server/protocol.go` contains the Go DTOs.

WS listeners beyond loopback require a bearer token (printed by app-server on stderr and saved in `~/.atto/server-token`); send `Authorization: Bearer <token>` or `?token=`. Browser origins must be same-host, loopback, or explicitly added with repeatable `--allow-origin https://client.example`. There is no built-in TLS: prefer a private network or TLS proxy. All transports route sessions to daemon workers when available; `--in-process` keeps a standalone server runtime. `initialize` (listing protocol revision 3) then `initialized` starts the protocol handshake. Detaching leaves worker execution alive; `thread/close` ends it.

## Safety

atto has **no per-command permission prompts**. It asks before loading repository-supplied hooks, MCP servers and extensions (see Project code approval), but the model's commands still run with your user's permissions. The only command filters are hooks you configure.

For untrusted repositories or long unattended runs, run atto in a VM or container.

Commands that atto runs get `ATTO_AGENT=1` and `ATTO_SESSION_ID=<session>` in their environment. With `ATTO_AGENT` set, atto refuses to start another agent, change credentials, or replace itself. This guards against accidents. It is not a sandbox. Every command the model runs through its shell tool also gets `ATTO_TOOL_CALL_ID=<the tool call ID of that command>` (like Codex's `CODEX_TOOL_CALL_ID`), including commands under a shell host and the background jobs they start; commands you run with `!` get none. `atto agent spawn` records it with the session, model, effort and turn that started an agent (`spawnedBy`). These variables are tracking, not proof: a model can change its own environment.

## Configuration

Everything lives in `~/.atto`. Set `ATTO_DIR` to move it.

<!-- Compatibility: explain the old state layout for existing installs and running daemons. -->
**Agent data layout and migration.** Earlier versions kept agent state in one directory per parent session (`agent-state/<parent>/<name>.json`, with `_up`, `_closed` and, before that, `subagents/`), and made a lightweight "external parent" session for agents started from a shell. The current layout is one record per agent session ID (`agent-state/<id>.json`), a format marker `agent-state/.format`, and no fake parents. `atto agent migrate` converts everything in one pass: it refuses while any agent turn, the job of one, or a session worker of an agent session runs (naming them), takes a backup to `~/.atto/backups/` (`atto restore -force <file>` undoes it), converts parent directories, `_up`, `_closed`, the symlink and shared and per-spawn external parents (each direct child of an external parent becomes its own agent root; empty fake parents are deleted, any that have messages stay ordinary sessions; existing worktrees and branches keep their paths and names) and writes the marker last. On an error it stops before the marker and tells you to restore the backup; running it again is safe. The first `atto agent` command that finds the old layout asks on a terminal; elsewhere (a model's shell, a script) it refuses and names `atto agent migrate`. **Every machine that shares `~/.atto` must be upgraded** to a version with this layout, and old binaries' agent commands must not run on migrated data: they cannot check the marker. From this release on, atto refuses agent commands on data whose marker is newer than it understands. `agents/` remains roles, never state.

| Path | Contents |
| --- | --- |
| `settings.json` | default model and effort, renderer, `mouse`, `toolGroups` (`false`: no command groups), `spinnerVerbs` (the word the activity line shows while commands run, drawn once per turn: `en`, the default, made-up English verbs; `ko`, made-up Korean words, as `글벅거리는 중…`; `ko-literary`, Korean verbs; `off`, just `Working…`), `spinnerScanner` (`true`: a sweeping `▰▱` scanner before that word), status line, hooks, `updateCheck`, `doubleEscapeAction` (`tree`, `fork` or `none`), `branchSummary.skipPrompt`, `toolOutputTokenLimit` (how much of a command's output the model gets, default 10000 tokens; the middle is cut and the full output saved to a file, as in codex), `toolOutput` (`fileHeadMB`, `fileTailMB`: how much of the start and end of one command's output its file keeps, 32 each; `totalMB`: all saved output, 1024, the oldest files go first; `minFreeMB`: free disk space needed to save any, 1024; `0` or absent is the default and a negative `totalMB` or `minFreeMB` turns that limit off), `backgroundExit` (experimental: `false` turns off the exit menu that offers "Run in background" while a turn runs), `daemon` (`false`: run sessions in-process instead of in daemon workers), `extensions` (`disabled` names, handler `timeout` in seconds), `skills.disabled` (built-in skills to turn off), `agents` (`model`, `effort`: defaults for roles that name none; agents are always on and unlimited, so the old `enabled`, `maxDepth` and `maxConcurrent` are ignored) |
| `agents/` | agent roles (`<name>.md`) |
| `agent-state/` | one record per agent session ID (`<id>.json`, `.turn.json`, `.turn.json.interrupt`, `.turn.lock`), the format marker `.format` and `.coord/` locks (separate from roles) |
| `backups/` | backups atto takes itself, before `atto agent migrate`; never part of a backup |
| `hook-approvals.json` | project hook decisions, scoped to settings file and content hash |
| `mcp.json` | MCP servers (Claude Code's `.mcp.json` format); `mcp-approvals.json` holds approved project servers, `mcp/` the endpoints of running sessions |
| `extensions/` | your extensions; `extension-approvals.json` holds project extension decisions, `extensions.log` their logs |
| `models.json` | your providers and models |
| `auth.json` | keys and logins (mode 0600) |
| `sessions/` | saved sessions |
| `images/` | images sent in sessions, by content hash |

### Troubleshooting

If text looks garbled, doubled or leaves fragments behind (seen with wide characters such as Korean in Windows Terminal and other ConPTY hosts), atto can repaint every visible row on each frame instead of only the changed ones. This is on by default on Windows. Set `ATTO_FULL_REPAINT=1` to force it on, or `ATTO_FULL_REPAINT=0` to force it off, on any OS. While atto works, the line above the input animates at about 30 frames a second (only that line is rewritten); full repaint animates it every 250ms instead.

The activity line's colors blend in 24-bit color when the terminal says it can (`COLORTERM=truecolor` or `24bit`, Windows Terminal, iTerm2, WezTerm, VS Code, Ghostty) and are rounded to the 256-color palette otherwise; on the Linux console and other 16-color terminals it is plain ASCII. Its teal turns amber when the model has sent nothing for 15 seconds (while no command runs), and back when output arrives.

## Slim build

The slim binary (`atto-slim_<os>_<arch>`, built with `-tags noext`) omits the
JavaScript extension engine and TypeScript compiler. It is about 10 MB smaller
than the full stripped build. `/diff` and `/autorename` are native Go commands
in **both** builds; model providers, tools, hooks, MCP, skills and the UI remain.
User and project extension files are not run or offered for approval in slim;
the Loaded block and `atto extensions` report how many were ignored.

Install slim on macOS/Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/sebastianrcnt/atto/main/install.sh | ATTO_VARIANT=slim sh
```

On Windows, set `$env:ATTO_VARIANT = "slim"` before running `install.ps1`.
`ATTO_CHANNEL=edge` also works with either variant. Updates preserve the running
variant; switch explicitly with `atto update -variant slim` or
`atto update -variant full`. A source build is `go build -tags noext ./cmd/atto`.


## Development

```sh
go install ./cmd/atto          # this machine
scripts/deploy.sh win linux    # other machines over ssh, no GitHub involved
```

Enable the pre-commit checks (gofmt, vet, modernize, tidy) with `git config core.hooksPath .githooks`.

`scripts/deploy.sh` builds an edge binary of this checkout for each host's system and installs it where the install scripts would (`%LOCALAPPDATA%\Programs\atto` on Windows, `~/.local/bin` elsewhere), so `atto update` there keeps following edge. Its version ends in `.local`.

## License

[MIT](LICENSE)

## Moving, cleaning and removing atto

Atto keeps its data in `~/.atto` (`%USERPROFILE%\.atto` on Windows), or the
path in `ATTO_DIR`. These commands run from your own terminal; `restore`,
`clean` and `uninstall` refuse an atto model shell.

```sh
atto backup                         # ./atto-backup-HOST-YYYYMMDD-HHMM.tar.zst
atto backup -o /safe/atto.tar.zst    # output must be outside ATTO_DIR
atto backup -with-secrets           # WARNING: includes credentials
atto backup -include-cache          # also preserve rebuildable caches
atto restore /safe/atto.tar.zst      # restore into an empty ATTO_DIR
atto restore /safe/atto.tar.zst -into /new/data -force
atto restore -worktrees -into /new/data
atto clean -dry-run                 # category/count/byte table; no removal
atto clean                          # show table, then ask
atto clean -y -older 7d              # positive days, or Go durations like 168h
atto uninstall                      # ask; offer a backup by default
atto uninstall -y -keep-data -no-backup
```

### Backup and restore

A backup is one streaming zstd-compressed tar archive, with `manifest.json`
first. It includes session transcripts and both compressed and legacy archives,
images, saved command outputs, jobs and inbox/events/goals, agent state (including
legacy `subagents`, reverse indexes and closed-agent records), external-parent
mappings, extensions and their log, skills, prompts, themes, agent profiles,
settings and their `.bak`, models, approvals, device identity, translation
settings, and helper binaries—indeed **every regular file, directory and safe
relative symlink under `ATTO_DIR`, except**:

* `cache/` (unless `-include-cache`), `run/`, `debug/`, `logs/`;
* `*.lock`, `update-check.json`, and the prior restore's bookkeeping manifest;
* `worktrees/` checkouts; repository, branch, base commit and agent identity are
  recorded instead;
* `auth.json` and its backup variants, and `server-token`, unless `-with-secrets`.

Archives are created exclusively (never silently overwrite an existing backup)
and with mode `0600`; included credential files are also marked `0600`.
**Transcripts, outputs and custom files can themselves contain secrets**, even
without `-with-secrets`. Keep all backups private. Unknown files are included,
not guessed to be disposable. Runtime sockets and other special files are not
archived. A backup refuses active workers, sessions, turn/slot locks or jobs;
`-force` explicitly warns that the copy may be inconsistent. Each transcript
is copied under its existing session writer lease when possible. Stop atto
before backing up for the strongest consistency guarantee.

The manifest records the atto version, archive/session-header/agent-layout/daemon
format versions, creation time, OS/architecture, hostname, source directory,
entry count, source bytes, exclusions and worktrees. Restore refuses newer
formats, unsafe/duplicate paths, absolute or escaping symlinks, hard links,
special files and entries traversing symlinks. It verifies entry and byte counts
in an isolated staging directory before replacing anything. `-force` first
preserves an existing non-empty directory as
`DIR.before-restore-YYYYMMDD-HHMMSS.NNNNNNNNN`; an active destination is never
replaced. Regular file/directory permission bits are preserved, subject to OS
semantics; credentials are always `0600` on Unix. Windows does not provide Unix
permission bits and requires the user's normal private directory ACLs.

The existing lazy agent-layout migration runs on restore (Windows retains the
legacy layout when its migration requires a symlink). Archive compression is
**not automatic**: use `atto sessions compress` if desired. Restore saves
`restore-manifest.json`, so `atto restore -worktrees` can later recreate clean
checkouts from existing local branches and relocate their agent-state paths.
Missing repositories/branches are reported and skipped. Git repositories and
branch contents are **not in the backup**: clone/copy them separately, retain
`atto/*` branches, and ensure their recorded repository paths exist on the new
machine. Dirty/uncommitted worktree contents are not backed up; commit or save
those changes separately first. Other historical absolute paths in transcripts
and project approvals are not rewritten.

### Clean

Clean never removes normal sessions or settings. It prints a category/count/byte
table before asking (or before `-y` removal). Its only transcript exception is an
unused **zero-message external orchestration parent** and its corresponding
`external_parents` mappings; a parent with agents, messages or a writer lock is
kept. Clean removes:

* `outputs/` files older than `-older` (default `30d`) **only when their session
  no longer exists**, including in the archive;
* job directories of deleted sessions, only without active jobs/open files;
* orphan registered agent worktrees through `git worktree remove`, never forcing
  dirty checkouts; active records are kept, and closed recorded branches must be
  absent or merged;
* old `debug/` files and stale `run/` sockets without listeners;
* idle, owned temporary `atto-bash-*.log`, `atto-transcript-*`, `atto-view-*`,
  `atto-mcp-*.sock`, and private `atto-<uid>/` socket files;
* `atto-home*`, `atto-session-test*` test leftovers older than a
  day, including read-only module-cache contents.

Deletion is re-inventoried after confirmation. Locks and live job PIDs protect
items. Open files are checked with `lsof` on macOS, `lsof` or `/proc` on Linux,
and exclusive file handles on Windows; inability to prove a runtime artifact
idle keeps it. Temporary cleanup is deliberately conservative: another atto
process keeps temporary artifacts even when it uses another data directory.
Symlink directory aliases below a maintenance root are kept, never traversed
for deletion. Branches are never deleted by `clean`.

### Uninstall

Uninstall offers a credential-excluding backup by default, stops the daemon
with force, recorded supervisors and non-daemon session processes, and removes
agent checkouts through Git. Runtime shutdown precedes the backup so it is
consistent. Dirty checkouts are listed and retained unless explicitly confirmed;
`-y` **does not authorize discarding dirty changes or deleting branches**.
Atto branches are listed per discovered repository and require a separate
confirmation to delete (including unmerged commits). If a worktree is retained,
its checkout and agent metadata are kept even without `-keep-data`; other data
is removed. `-keep-data` retains the data directory but does not prevent runtime
shutdown or clean worktree removal. `-no-backup` skips the backup offer.

The running binary's actual path and recognized installer `.old`/`.new`
artifacts are removed when idle, whether installed in
`~/.local/bin`, `$GOPATH/bin`, a custom install directory, or
`%LOCALAPPDATA%\Programs\atto`. Windows schedules running-executable deletion
in a detached process after exit, and removes the installer's directory from
user PATH when this binary resides there. The Unix installer **does not edit
shell startup files** (it only prints PATH advice); uninstall therefore does not
guess at or rewrite user-owned shell configuration. Manually added PATH entries,
other binary copies, and external repository
branches/checkouts outside the atto data directory must be removed separately.
Any failed or unsafe removal is reported. Open a new terminal afterward.

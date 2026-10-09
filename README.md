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
(no launchd, systemd or scheduled task). It supervises workers only: every TUI
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
accepted work in the previous worker continues. `/remote` controls the same worker
from the frozen web client. `atto app-server` and `atto serve` route sessions to
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
background.” Windows always uses that mode. Normal sockets live in `~/.atto/run`
(too-long paths use a private temporary directory); logs are in
`~/.atto/logs/daemon.log`.

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
| `←` | on an empty prompt: the agent command center (also `/agents`), as codex's: every atto session, the daemon's running ones and the saved ones, grouped by project, with tabs (`Tab`/`Shift+Tab`) for All, Needs you (a question is open or a goal waits for you), Working, Ready and Inactive (saved), and the selected session's last message, project, branch and first prompt on the right. `→` or `Enter` goes to it: this TUI attaches to its worker (or starts one from the saved file), detaching from the previous worker; `n` starts a new session in the selected one's project; `/` searches; `←`, `Esc` or `Ctrl+C` comes back (`Ctrl+C` closes only the center, without interrupting a running turn or shell command). Agents appear as a tree under the session that started them (shell-started agents have an “agents started from a shell” parent). `Space` folds/unfolds a tree; tabs and search include agents and reveal matching rows with their ancestors. Agent rows show their path, role, model, status and worktree branch; details show the task, last answer/report and turn tokens/duration. A running agent opens read-only with `Ctrl+R` to refresh until it finishes |
| `Shift+Tab` | cycle reasoning effort |
| `Ctrl+T` | expand everything: thinking, command groups and every command's full output; again to fold it all back (or click one block) |
| `Ctrl+B` | move the running command to the background: it keeps running as a job (`/jobs`), the agent goes on and gets an `[atto event]` when it exits |
| `Ctrl+C` | copy the selection if there is one; otherwise interrupt, clear the input when idle, or quit when the input is empty |
| `Ctrl+V` / `Alt+V` | attach the image on the clipboard (use `Alt+V` where the terminal pastes text on `Ctrl+V`, as on Windows) |

Shell commands, as in pi: start the prompt with `!` to run a command yourself, in the working directory, with the shell and environment the agent's own commands use and its output cut (the full text is saved to a temp file). It shows in the conversation as a `! command` block, and the command and its output go to the model as a user message the next time it runs (`Ran` followed by the command and its output). `!!command` runs it the same way but keeps it from the model; the block says so. The input turns green while the text starts with `!`. `Esc` or `Ctrl+C` cancels the command; `Ctrl+B` moves it to a background job. User-entered commands keep their long foreground wait (up to 30 minutes), rather than the model tool's 10–30 seconds. It runs at once even while the agent works, but its result joins the conversation only when the turn ends, so it never lands between a tool call and its result; one command runs at a time (another is refused and stays in the input). `!` alone is an ordinary message. Commands are saved in the session and come back on resume, `/tree` and fork. No hooks run for them, and `atto -p` has no such prefix.

File mentions, as in pi: type `@` at the start of a word to pick a file or folder of the project (fuzzy; a `/` in the query matches the whole path; `.gitignore` is respected). `Tab` or `Enter` inserts `@path`, quoted when it has a space; a folder keeps completing inside it. Only the path is sent: the model reads the file itself.

Pasting the path of an image file, or dropping the file on the terminal, attaches it too. Images show as `[image 1: 1024x768 PNG]` in the input; delete the placeholder to drop the image. Images larger than 2048 pixels are scaled down. Clipboard images need `osascript` (macOS; `pngpaste` is used if installed), `wl-paste` or `xclip` (Linux), or PowerShell (Windows, WSL).

`atto -p` attaches images given with `-image` and an image piped to stdin (PNG, JPEG, GIF or WebP, recognized by its first bytes); the prompt argument is then the text. In the web client (`atto serve`, `/remote`), attach images with the image button, by pasting or by dropping them; over JSON-RPC, `turn/start` takes `images: [{mimeType, data}]` (base64 or a `data:` URL, at most 10 of 10 MB each). Either way the model must accept images.

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
| `/diff [--staged] [path]` | show what changed in the working tree: a summary, then the diff (a built-in extension, see `extensions/builtin/diff.ts`) |
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
| `/remote [on [port]\|off]` | control this session from a phone or browser: serves atto's web client on port 7879 (or `"remote": {"port": N}` in `settings.json`), prints its link and a QR code, and marks messages sent from there "from remote"; `off` closes every connection and revokes the link |
| `/jobs`, `/stop` | list or stop background jobs |
| `/timer`, `/timers` | wake the agent later, or list pending timers |
| `/quit` | exit atto |

## How it works

**One tool.** The model works through a single shell tool: bash on macOS and Linux, PowerShell on Windows. Hosted commands wait in the foreground for 10 seconds by default; the model can set `timeout` up to 30 seconds. A command still running then becomes a background job, with its id and output so far returned to the model. Long builds and tests keep running: the model can use `atto job wait <id> -timeout 10m`, read `atto job output <id>`, or wait for the exit event. `run_in_background` starts a job immediately; `Ctrl+B` still moves the running command to a job at once. Without a shell host or session, commands cannot detach and retain the kill timeout (60 seconds by default, up to 30 minutes). Interrupting a turn (Esc, Ctrl+Enter, a remote interrupt or `atto agent interrupt`) also moves a still-running hosted model command to a job, recording the job id and output tail for the next turn. An interrupt-detached job's exit event waits for the next turn if the session is idle; requested, timed-out and Ctrl+B jobs still wake the agent as before. `/jobs` lists them and `/stop` stops them. Closing a session (or exiting in-process mode) stops its jobs; detaching a worker client does not; ordinary cancellation, hooks, direct runs and standalone `atto -p` Ctrl+C do not detach commands. Everything else is a command it can run:

- `atto history grep` searches the session transcript, including turns that were compacted away.
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

A session is open in one atto at a time: while a terminal has it, resuming it in another (or with `atto -p`, or from `atto serve`) is refused with the pid that holds it, since two writers would undo each other's work and goal.

**Hooks** use the same format as Claude Code: `PreToolUse`, `PostToolUse`, `UserPromptSubmit`, `Stop`, `PreCompact`, `SessionStart`, `SessionEnd` and `Notification`.

- Put them in `~/.atto/settings.json` or in the working directory's `.atto/settings.json`. User hooks are your own and need no approval; project hooks stay off until approved (see **Project code approval** below).
- A hook that exits with code 2, or returns `{"decision": "block"}`, stops the action.
- `Stop` runs when the agent is done answering (in the TUI, `-p` and the servers). Blocking it sends the reason to the model as a user message and the turn continues. The input has `stop_hook_active`, true once a Stop hook has already kept this turn going, so a hook can let it finish. After 8 blocks in a row atto stops anyway. An interrupted turn (Esc) runs no Stop hook. With `/goal`, the Stop hook runs at the end of every turn, before the goal decides whether to continue.
- `SessionEnd` runs when a session ends, with `reason`: `exit` (quitting the TUI), `clear` (`/clear`), `resume` (switching to another session), or `other` (`-p` finishing, the server shutting down, a conversation archived). It cannot block and has a 5 second default timeout (set `timeout` on the hook to change it), so exiting stays quick. The matcher is tested against `reason`.
- `Notification` runs when atto wants your attention, with `message` and `notification_type`; the matcher is tested against the type. It cannot block. The TUI sends `idle_prompt` when a turn that took 15 seconds or more is done and atto waits for your input (not when a queued message or a goal turn follows), `background_event` when a job or timer event arrives while atto is idle, and `goal_blocked` when a goal becomes blocked. There are no per-command permission prompts, so no such notification. `-p` and the servers send none.
- Hook messages, such as the reason of a blocked Stop, appear in the transcript, and as `hook` events in `-p --output-format stream-json`.

**Project code approval.** A repository may bring executable hooks, MCP servers or extensions. At startup, and when `/reload` finds new or changed content, the TUI asks **once**, listing all pending items together: **Allow all**, **Review one by one**, or **Deny**. Allow all approves only the content shown, not future items or changes. Review offers Allow/Deny for each item. Decisions are remembered by content hash; unchanged denials stay off without asking again. `Esc` means “not now”: content stays off and is not asked about again until the next atto run (or until it changes).

Where nothing can ask (`atto -p`, `atto app-server`, `atto serve`, background workers), unapproved content stays off and a warning names each item and its approval command. Approve ahead of time from your own terminal:

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

**Extensions** are TypeScript or JavaScript files, often written by the agent itself, that atto runs in an embedded engine (no Node.js needed). They can block or rewrite the agent's commands, rewrite what the model sees of their output (to redact secrets, say), add to prompts, add slash commands, and show status items, widgets and dialogs in the TUI.

```ts
// ~/.atto/extensions/no-force-push.ts
export default function (atto) {
  atto.on("tool_call", (e) => /git push.*--force/.test(e.command) ? { block: true, reason: "no force-push" } : undefined);
  atto.registerCommand("todo", {
    description: "Count TODOs",
    handler: async (args, ctx) => ctx.ui.notify((await atto.exec("git grep -c TODO || true")).stdout || "none"),
  });
}
```

- Put them in `~/.atto/extensions/` (`name.ts`, or `name/index.ts` with files it imports) or the project's `.atto/extensions/`. Project extensions run only after you approve them (`/extensions approve <name>` or `atto extensions approve <name>`); a change needs approval again.
- `/reload` (or `atto reload`) loads changes; the Loaded block shows each extension's status, commands and events, and errors with `file:line`.
- Events: `session_start`, `session_end`, `turn_start`, `turn_end`, `user_prompt`, `tool_call`, `tool_result`. They run inside the hooks: `PreToolUse` hooks, then `tool_call`, the command, `tool_result`, then `PostToolUse` hooks.
- Also `atto.exec`, `atto.fs`, `fetch`, timers, `atto.sendMessage`; dialogs and widgets are TUI-only (in `-p` and the server, dialogs get default answers).
- `atto extensions docs` prints the guide ([docs/extensions.md](docs/extensions.md)), `atto extensions types` the type declarations, `atto extensions source diff` the source of a built-in one, `atto extensions` the list. [examples/extensions](examples/extensions) has examples to copy, such as one that shows reasoning translated by a small local model. A handler that hangs is skipped after 5 seconds and a runaway script is stopped and its extension disabled; atto goes on.

**Skills** are read from `~/.atto/skills` and the project's `.atto/skills` (the first of a name wins), and from nowhere else: directories other tools share, such as `.agents/skills` and `.claude/skills`, hold what was installed for those tools.

**Built-in skills.** atto ships skills of its own (Agent Skills, one `SKILL.md` each), listed in the system prompt like yours and shown as `builtin` in the Loaded block and `atto context`. Today there is one, `atto-extensions`: when you ask to customize atto, the model reads the extension API with `atto extensions docs` and `types`, writes the extension, runs `atto reload` and fixes load errors. Because the model reads skills as files, they are written to `~/.atto/cache/skills/<hash>/<name>/SKILL.md` (the hash changes when the text does). A skill of the same name in `~/.atto/skills` or the project's `.atto/skills` overrides a built-in one; `/skill:atto-extensions` runs it by hand; to turn one off: `{ "skills": { "disabled": ["atto-extensions"] } }` in `settings.json`. `atto extensions source diff` prints the source of the built-in `/diff` extension, the example the skill points to.

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
**Agents** (off by default) are atto sessions other agents start, as in codex's multi-agent mode: equally capable, with the same tools, each working in the background on what it is sent. The model starts and talks to them through its shell like everything else. Turn them on with `"agents": {"enabled": true}` in `settings.json` (the older `"subagents"` key still works and is migrated on write); the system prompt then tells the model about them and to start them only when you ask.

**Standalone or agent?** These are different kinds of session:

| Command | Session |
| --- | --- |
| `atto -p "..."` | Standalone, headless: no parent, no path, not in any agent tree. Saved unless `-no-save`. |
| `atto agent spawn NAME "<task>"` | Under a parent, with a path such as `/root/NAME`, `wait` / `report` / `list`, and an optional git worktree (`-worktree`). |

`agent` works directly from a plain shell: no running atto session or `atto -p` root is needed. A model-run root is only needed when a model should orchestrate the agents. With agents off, `agent spawn`, `task` and `send` fail with "agents are off".

From a plain shell, a typical worktree run is:

```sh
atto agent spawn NAME "task" -worktree -m openai/gpt-6.1-sol
atto agent wait -json
# Review the branch shown in the report.
atto agent close NAME
```

`close` removes the worktree and keeps the branch for you to merge.

For an outside orchestrator, remember the **agent's own address** from the
started line, not just its name or its parent's ID:

```sh
atto agent spawn panes-123 "task" -worktree
# agent /root/panes-123 started (@eff39362, session eff39362, ...).
agent_addr=@eff39362                 # save the address returned by this spawn
cd /some/other/directory
atto agent send "$agent_addr" "also check the tests"
atto agent wait "$agent_addr" -json -timeout 10m
atto agent report "$agent_addr" -json
atto agent close "$agent_addr"
```

No `-session PARENT` is needed for these `@ID` calls. Outside orchestrators in
the same project still share a parent, so choose a free name when spawning;
a saved `@ID` reaches exactly that agent even if its name is later reused.
`atto agent list -all` discovers agents under all external parents.

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

- **Trees and addresses.** Each agent has a path from the root of its tree: the session that started the first ones is `/root`, its agent `tests` is `/root/tests`, and that one's `lint` is `/root/tests/lint`. `AGENT` is a name you gave, a path below you (`tests/lint`), `..` for the agent that started you, or a full path (`/root`). You can also use `@<session id>`: the agent's own full session ID, or a unique prefix of at least 6 characters. Bare hex IDs are still names, not ID addresses. Prefix ambiguity lists the matching candidates; an unknown ID suggests `atto agent list` (outside atto, `list -all`). Inside an atto session or model shell, `@ID` can only name agents in the caller's own tree; an agent in another tree is reported as not found. Outside atto, `@ID` can reach any agent regardless of directory, parent, or `-session`. Name/path addressing is unchanged. `spawn` prints the `@ID` address in its started line, and `list` shows short `ID` and `ADDRESS` columns. `list -all` is for outside callers only and lists agents under every external parent. A closed/removed ID reports "closed" rather than following a reused name; archived transcripts remain available through the session/history commands and agent command center.
- **Messages.** What one agent sends another arrives wrapped in `<atto_internal_context source="agent">` with a `Message Type` (`NEW_TASK`, `MESSAGE` or `FINAL_ANSWER`), `From` and `To`. When an agent's turn ends, its final answer reaches the session that started it by itself (`FINAL_ANSWER`, cut at 8000 characters; `report` has all of it), and wakes it as a job's exit does. `task` starts a turn; `send` doesn't: a message to an idle session waits in its inbox for its next turn (a running one takes it after its current step, but a turn that has finished is not kept going for it). `wait` returns early when you send a message, so you are never stuck behind it.
- **Nesting.** `agents.maxDepth` (default 1, as codex) is how deep trees may grow: at 1 only your session starts agents; at 2 they may start their own, and so on. An agent that may start agents is told how; one that may not is told to do the work itself. `agents.maxConcurrent` (default 3) caps the turns each session's agents run at once; the rest wait in a queue (`list` shows them `queued`). Closing an agent closes the agents below it.
- **From a normal shell**, every command accepts `-session ID`. For spawning and name/path addressing without it (and without `ATTO_SESSION_ID`), atto creates a lightweight parent without calling a model, prints its ID, and reuses it for the project (git root, else cwd). It is named `atto agent (external)` in session lists, is not picked by continue, and is archived and forgotten when `close` removes its last agent.
- External callers can set the model and effort on `spawn` with `-m provider/model -effort LEVEL`; in atto's model shell (`ATTO_SESSION_ID` / `ATTO_AGENT` set) these flags are refused and models pick roles. `read` and `show` are aliases of `report`. `wait` and `report` accept `-json` for one object with `name`, `status`, `turn`, `duration` (seconds), `tokens` (`in`, `cached`, `out`), optional `cost` (estimated USD), `session`, `model`, `message` and optional `error`, `worktree` and `branch`.
- An agent is its own session (in the parent's directory, or its own worktree with `-worktree`) that sees only what it is sent. Each turn runs headless as a job of the session that started it (`atto job list` shows `agent NAME`); an agent's own agents keep running when its turn ends. Agents' sessions are kept out of the default `atto resume` and `atto sessions` listings, but appear in the agent command center under their parent, recursively. Working includes running/queued agent turns; idle or completed agents are Ready, failed/stopped or closed agents Inactive. Closed agents keep their archived transcripts in the center. The center opens a locked agent transcript read-only (banner and `Ctrl+R` refresh); once unlocked, refresh opens it normally.
- **Worktrees.** `spawn -worktree` gives the agent a git worktree of its own, so agents editing files in parallel don't clobber each other or your checkout. It is made from the parent's `HEAD` (committed work only) on a new branch `atto/<parent session>/<name>` (spawn refuses if that branch exists), at `~/.atto/worktrees/<parent session>/<name>`: outside the project, so nothing shows up in its `git status` or searches, and short enough for Windows paths. The agent works at the same place in it as the parent and is told to commit there. It needs a git repository with a commit. `report`, `list` and `-json` show the worktree and branch. `close` runs `git worktree remove` and keeps the branch, printing it and its new commits for you to merge; while the worktree has uncommitted changes `close` refuses and lists them, unless `-force`.
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

**Front end and back end are separate.** Both servers speak the same JSON-RPC protocol, built around threads, turns and items:

- `atto serve` serves it over HTTP + SSE, also exposes WebSocket at `/ws` with the same token, and includes a web client, so you can use atto from a phone. Listening beyond this machine (`-listen 0.0.0.0:7878`), it prints the link with a QR code to scan.
- `atto app-server` serves it over JSON-lines stdio by default, or `--listen unix:///tmp/atto.sock` (0600, removed on exit), or `--listen ws://127.0.0.1:7878` (one JSON-RPC message per text message). Socket disconnect is detach, not stop.
- `/remote` in the TUI serves the session you are in, with the same protocol and web client: the browser shows the conversation as it streams, and what you send from it goes in as if typed (a turn, or a steer while one runs); Stop, Background, model and effort work too. `/clear` and `/resume` take the browser along. Each `/remote on` makes a new token, so `/remote off` revokes the link; quitting atto stops it. There is no TLS: use it on a network you trust (or Tailscale).

The web client shows what the terminal does: commands the model ran one after another fold into one line (`toolGroups` in `settings.json` applies), the status line sits under the input (model, context, cache hit rate, ↑/↓ tokens, cost) with the activity line above it while a turn runs (its time and tokens, orange when the model has been quiet for a while), and steers or queued messages not taken yet are listed above the input, where they can be edited or dropped. The ⋯ menu starts a new conversation, compacts, undoes the last turn (its message returns to the input), switches model and effort, and opens the session's background jobs (their output, and Stop) and agents (their reports and, read only, their transcripts). The [client-author protocol reference](docs/protocol.md) covers every method, notification, handshake, cursor and prompt; [small Python and browser clients](examples/clients/README.md) show how to attach. `server/protocol.go` contains the Go DTOs.

WS listeners beyond loopback require a bearer token (printed by app-server on stderr and saved in `~/.atto/server-token`); send `Authorization: Bearer <token>` or `?token=`. Browser origins must be same-host, loopback, or explicitly added with repeatable `--allow-origin https://client.example`. There is no built-in TLS: prefer a private network or TLS proxy. All transports route sessions to daemon workers when available; `--in-process` keeps a standalone server runtime. `initialize` then `initialized` starts the protocol handshake. Detaching leaves worker execution alive; `thread/close` ends it.

## Safety

atto has **no per-command permission prompts**. It asks before loading repository-supplied hooks, MCP servers and extensions (see Project code approval), but the model's commands still run with your user's permissions. The only command filters are hooks you configure.

For untrusted repositories or long unattended runs, run atto in a VM or container.

Commands that atto runs get `ATTO_AGENT=1` in their environment. With it set, atto refuses to start another agent, change credentials, or replace itself. This guards against accidents. It is not a sandbox.

## Configuration

Everything lives in `~/.atto`. Set `ATTO_DIR` to move it.

<!-- Compatibility: explain the old state layout for existing installs and running daemons. -->
Agent state formerly lived in `subagents/`. Atto moves it to `agent-state/` under a lock when the old daemon and turns are idle, leaving an old-path alias for older processes. If both directories exist, it reads the new one first and falls back to old-only agent ids. Without symlink privileges it keeps using the old layout safely; `agents/` remains roles, never state.

| Path | Contents |
| --- | --- |
| `settings.json` | default model and effort, renderer, `mouse`, `toolGroups` (`false`: no command groups), `spinnerVerbs` (the word the activity line shows while commands run, drawn once per turn: `en`, the default, made-up English verbs; `ko`, made-up Korean words, as `글벅거리는 중…`; `ko-literary`, Korean verbs; `off`, just `Working…`), `spinnerScanner` (`true`: a sweeping `▰▱` scanner before that word), status line, hooks, `updateCheck`, `doubleEscapeAction` (`tree`, `fork` or `none`), `branchSummary.skipPrompt`, `toolOutputTokenLimit` (how much of a command's output the model gets, default 10000 tokens; the middle is cut and the full output saved to a file, as in codex), `backgroundExit` (experimental: `false` turns off the exit menu that offers "Run in background" while a turn runs), `remote.port` (`/remote`'s port, default 7879), `daemon` (`false`: run sessions in-process instead of in daemon workers), `extensions` (`disabled` names, handler `timeout` in seconds), `skills.disabled` (built-in skills to turn off), `agents` (`enabled`, `maxDepth`, `maxConcurrent`, `model`, `effort`) |
| `agents/` | agent roles (`<name>.md`) |
| `agent-state/` | state, turns and coordination files for agents each session started (separate from roles) |
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

## Development

```sh
go install ./cmd/atto          # this machine
scripts/deploy.sh win linux    # other machines over ssh, no GitHub involved
```

Enable the pre-commit checks (gofmt, vet, modernize, tidy, web dist) with `git config core.hooksPath .githooks`.

The web client (`server/web`) is Preact and TypeScript styled with Tailwind; its built bundle in `server/web/dist` is committed, so `go build` needs nothing else. After changing `server/web/src`, run `go generate ./server/web` (it downloads the pinned Tailwind standalone CLI and Preact once, checking their SHA-256, and bundles with esbuild; no Node) and commit `dist/`; a test fails while `dist/` is stale.

`scripts/deploy.sh` builds an edge binary of this checkout for each host's system and installs it where the install scripts would (`%LOCALAPPDATA%\Programs\atto` on Windows, `~/.local/bin` elsewhere), so `atto update` there keeps following edge. Its version ends in `.local`.

## License

[MIT](LICENSE)

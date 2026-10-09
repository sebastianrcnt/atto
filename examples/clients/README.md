# Tiny native-protocol clients

These examples use only standard-library / browser APIs. They speak atto's
[native protocol](../../docs/protocol.md), not Codex's wire dialect. They run
commands with your permissions: use a trusted project and network.

## Python: stdio

Requires Python 3 and an installed atto with a configured model:

```sh
python3 examples/clients/stdio.py 'Explain this repository briefly'
python3 examples/clients/stdio.py --atto ./atto --model provider/model --in-process 'Hello'
```

The script starts `atto app-server --listen stdio://`, initializes an interactive
client, starts a thread, streams assistant text and answers select/input prompts
from stdin. Enter a 1-based choice, or an empty choice to cancel. EOF cancels a
prompt; it does not approve it. Ctrl+C sends a user interrupt. Closing the pipe
at the end detaches; daemon workers are not explicitly closed. The example
reports an event gap rather than attempting automated recovery.

## Browser: WebSocket

Serve this directory over HTTP (not `file://`, whose Origin is `null`):

```sh
python3 -m http.server 8000 --directory examples/clients
atto app-server --listen ws://127.0.0.1:7878
# Or use the existing HTTP/SSE server's socket:
atto serve --listen 127.0.0.1:7878
```

Open <http://localhost:8000/>. For app-server use `ws://127.0.0.1:7878/` with no
token on loopback. For serve use `ws://127.0.0.1:7878/ws`, and copy the token from
`~/.atto/server-token` (or `$ATTO_DIR/server-token`) into the token field. Browser
WebSocket cannot set Authorization headers, so the example uses `?token=`; it
never stores the token. Avoid logging URLs containing it.

Click Connect, then Start / resume. An empty thread ID starts a thread; paste an
existing ID to resume it. Send starts an idle turn; Steer adds input to a running
turn; Interrupt cancels and returns pending steers; Detach closes only the
socket. Items update live. It hydrates snapshots by event cursor and reloads on
`events/reset` / branch changes. It intentionally omits production UI, image
upload, reconnect/backoff, queue controls and complete extension displays.

For a browser hosted elsewhere, add its exact origin:

```sh
atto app-server --listen ws://0.0.0.0:7878 --allow-origin https://client.example
```

A non-loopback listener requires the bearer token printed on stderr (also saved
in the token file), and warns about lack of TLS. Put it behind a TLS proxy or
use a trusted private network. `--allow-origin` is repeatable; it is not an auth
bypass. Same-host and HTTP(S) loopback origins are allowed by default.

Both examples use daemon worker routing by default when available. On Windows,
with `ATTO_NO_DAEMON=1`, or with `--in-process`, runtime lifetime is the server
process's lifetime rather than a detached worker's.

Both examples intentionally negotiate revision 2, preserving their full-snapshot
reducers. New clients can negotiate revision 3 for cheap tail snapshots and use
`thread/items` with `before` to load older messages; see the protocol reference.

# Tiny native-protocol client

This example uses only the Python standard library. It speaks atto's
[native protocol](../../docs/protocol.md) (revision 3), not Codex's wire
dialect. It runs commands with your permissions: use a trusted project.

## Python: stdio

Requires Python 3 and an installed atto with a configured model:

```sh
python3 examples/clients/stdio.py 'Explain this repository briefly'
python3 examples/clients/stdio.py --atto ./atto --model provider/model --in-process 'Hello'
```

The script starts `atto app-server --listen stdio://`, initializes an interactive
client (`protocolVersions: [3]`), starts a thread, streams assistant text and
answers select/input prompts from stdin. Enter a 1-based choice, or an empty
choice to cancel. EOF cancels a prompt; it does not approve it. Ctrl+C sends a
user interrupt. Closing the pipe at the end detaches; daemon workers are not
explicitly closed. The example reports an event gap (`events/reset`) rather than
attempting automated recovery.

It uses daemon worker routing by default when available. On Windows, with
`ATTO_NO_DAEMON=1`, or with `--in-process`, runtime lifetime is the server
process's lifetime rather than a detached worker's.

## Other transports

`atto app-server --listen unix:///path.sock` speaks the same JSON lines over a
Unix socket, and `--listen ws://IP:PORT` one JSON-RPC message per WebSocket text
message (a non-loopback listener requires the bearer token printed on stderr,
`?token=` or `Authorization: Bearer`; `--allow-origin` adds a browser origin).
The [protocol reference](../../docs/protocol.md) covers the handshake, snapshots
(`thread/read` returns the newest page; `thread/items` with `before` loads older
ones), events and prompts. A new web UI is planned; there is no browser example
meanwhile.

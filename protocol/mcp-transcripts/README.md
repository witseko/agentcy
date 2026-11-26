# MCP wire transcripts of the Python shim

These files record what the Python shim (`src/agentcy/mcp/server.py`, mcp SDK 2.3.0) sends and receives on the wire. The Go port uses them as the reference for its MCP layer.

## Files

| File | Content |
| --- | --- |
| `stdio.2025-11-25.json` | stdio session, `initialize` handshake era |
| `stdio.2026-07-28.json` | stdio session, `server/discover` era |
| `http.2025-11-25.json` | streamable HTTP session behind the bearer gate, handshake era |
| `http.2026-07-28.json` | streamable HTTP session behind the bearer gate, `server/discover` era |
| `http.2025-03-26-initialize.json` | the raw `2025-03-26` initialize POST from `tests/e2e/test_http_bridge.py:26-35` |
| `tools-list.2025-11-25.json`, `tools-list.2026-07-28.json` | the full `tools/list` result of each era, verbatim. The port embeds the tool names, descriptions and schemas from here |
| `capture.py` | the script that recorded all of the above |
| `tee_stdio.py` | the stdio proxy that `capture.py` puts between client and shim |

Each session file has `transport`, `era`, `client` and a list of `steps`. A step has a name and the messages in wire order. Each message has a `direction` (`client_to_server` or `server_to_client`) and the JSON-RPC `message`. HTTP entries also have an `http` object (method, path, status, and the MCP-relevant headers) or an `sse` object (the SSE event fields) when the message arrived as a server-sent event.

Steps per session file:

- `connect`: `initialize` plus `notifications/initialized` (2025-11-25), or `server/discover` (2026-07-28).
- `tools/list`.
- `tools/call create_conversation`: one ordinary tool call.
- `skills/list` and `skills/get` (MCP Skills extension SEP-2640).
- 2025-11-25 only, `tasks/get (not bound in this era)`: the shim answers `-32601`.
- 2026-07-28 only, `tools/call launch_participant (task-shaped result)`, `tasks/get`, `tasks/cancel` (MCP Tasks extension SEP-2663). The launch uses the fake plugin `tests/fixtures/fake_run_plugin.py`, registered the same way as in `tests/e2e/test_mcp_tasks_stdio.py`.
- `tools/call close_conversation (elicitation)`: the close confirmation form, answered with `accept` and `confirm: true`.
- HTTP only, `disconnect`: the session `DELETE` (2025-11-25 only; the 2026-07-28 era has no session).

## How they were captured

```sh
uv sync --locked
uv run --locked python protocol/mcp-transcripts/capture.py
```

The script:

1. Creates a fresh temporary `AGENTCY_HOME` under `/tmp` (short, because of the Unix socket path limit), registers the fake fresh-run plugin in its `plugins.json`, and starts a private core with `python -I -m agentcy.mcp.core`. It never uses `~/.agentcy`.
2. For stdio, starts the installed `agentcy --workspace <home>/workspace` behind `tee_stdio.py`, which copies every JSON-RPC line in both directions to a log.
3. For HTTP, starts `agentcy --workspace ... --http <port>` on 127.0.0.1 with a test bearer token, and logs requests and responses through a logging `httpx2` transport inside the client.
4. Connects with the unmodified mcp 2.3.0 `Client`, the same client as `tests/e2e`: `mode="legacy"` for 2025-11-25 and `mode="auto"` for 2026-07-28, with `extensions=[advertise("io.modelcontextprotocol/tasks")]` and an elicitation callback that accepts.
5. Stops the shim and the core and deletes the temporary home.

## Placeholders

Values that are credentials or unique per run are replaced, everywhere they occur (also inside JSON text content):

| Placeholder | Original value |
| --- | --- |
| `<TOKEN_n>` | conversation member token |
| `<JOIN_CODE_n>` | invitation join code |
| `<TASK_ID_n>` | MCP task ID (the only credential for `tasks/get`) |
| `<SESSION_ID_n>` | `mcp-session-id` header |
| `<REQUEST_STATE_n>` | the opaque, signed `requestState` of a 2026-07-28 input-required result |
| `<BEARER_TOKEN>` | the HTTP access token |
| `<AGENTCY_HOME>` | the temporary data directory |
| `<ICON_PNG_BASE64>` | the base64 bytes of `src/agentcy/mcp/icon.png` in `serverInfo.icons` |

Conversation IDs, timestamps, digests and the agentcy version are kept as recorded.

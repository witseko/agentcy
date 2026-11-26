# Spike S1: the official Go MCP SDK against the agentcy shim surface

## Verdict

**Use the SDK:** `github.com/modelcontextprotocol/go-sdk` **v1.8.0** (the latest stable release, published 2026-11-17). It passed every criterion on stdio and HTTP. It needed no fork and no reflection, only exported API.

| # | Criterion | stdio | HTTP |
| --- | --- | --- | --- |
| 1 | `initialize` in era 2025-11-25 | PASS | PASS |
| 1 | Connect in era 2026-07-28 (`server/discover`) | PASS | PASS (needs the era router, see below) |
| 2 | Raw `tasks/get` from `tests/e2e/test_mcp_tasks_stdio.py:35-38` gets a valid reply | PASS | PASS |
| 3 | `skills/list` gets a valid reply | PASS (both eras) | PASS (both eras) |
| 4 | One form elicitation completes, era 2025-11-25 | PASS | PASS |
| 4 | One form elicitation completes, era 2026-07-28 | PASS | PASS |
| 5 | The `2025-03-26` initialize from `tests/e2e/test_http_bridge.py:26-35` | n/a | PASS |
| - | No fork, no reflection hacks | PASS | PASS |

Additional checks, all PASS: the shim's 17 tools served verbatim from `protocol/mcp-transcripts/tools-list.2026-07-28.json` (each entry equal to the Python shim's entry), an ordinary `tools/call`, `skills/get`, a task-shaped `tools/call` result (`resultType: "task"`), `tasks/cancel`, `tasks/get` answering `-32601` in the 2025-11-25 era like the Python shim, an elicitation decline that becomes a tool error, and the bearer gate answering 401 `Unauthorized`.

v1.9.0-pre.1 was tested first and also passed every check (38 of 38). The module now pins v1.8.0.

## Evidence

Build and run (from the repo root):

```sh
BIN=$SCRATCH/spike-mcp-180
(cd spikes/mcp && go vet . && CGO_ENABLED=0 go build -trimpath -o "$BIN" .)
uv run --locked python spikes/mcp/driver.py --server "$BIN" --scratch "$SCRATCH/mcpwire180"
```

Output (exit code 0; long lines cut at about 200 characters):

```text
PASS  initialize stdio 2025-11-25: protocol_version=2025-11-25
PASS  tools/list verbatim stdio 2025-11-25: 17 tools, each equal to the Python shim's recorded entry: True; same order as Python: False
PASS  tools/call stdio 2025-11-25: content={"tool": "post_message"}
PASS  skills/list stdio 2025-11-25: {"skills": [{"uri": "skill://lead-conversation/SKILL.md", "frontmatter": {"description": "Spike skill.", "name": "lead-conversation"}, "resources": [...]}]}
PASS  skills/get stdio 2025-11-25: {"skill": {"uri": "skill://lead-conversation/SKILL.md", ...}}
PASS  tasks/get unbound in legacy era stdio 2025-11-25: error code=-32601 message='method not found: "tasks/get"' (Python shim: -32601 'Method not found')
PASS  form elicitation accept stdio 2025-11-25: elicitations=['Close this conversation?'] is_error=False text='{\n  "closed": true\n}'
PASS  form elicitation decline stdio 2025-11-25: elicitations=1 is_error=True text='The user declined this action, so nothing changed. Do not retry it unless the user asks again.'
PASS  initialize stdio 2026-07-28: protocol_version=2026-07-28
PASS  tools/list verbatim stdio 2026-07-28: 17 tools, each equal to the Python shim's recorded entry: True; same order as Python: False
PASS  tools/call stdio 2026-07-28: content={"tool": "post_message"}
PASS  skills/list stdio 2026-07-28: {"_meta": {"io.modelcontextprotocol/serverInfo": {...}}, "skills": [...], "ttlMs": 300000, "cacheScope": "public", "resultType": "complete"}
PASS  skills/get stdio 2026-07-28: {"_meta": {...}, "skill": {...}, "ttlMs": 300000, "cacheScope": "public", "resultType": "complete"}
PASS  task-shaped tools/call result stdio 2026-07-28: {"_meta": {...}, "resultType": "task", "taskId": "spike-task-0001", "createdAt": "2026-11-26T11:14:37Z", ..., "ttlMs": null, "pollIntervalMs": 5000, "status": "working", "statusMessage": "The fresh run for Fresh is queued."}
PASS  raw tasks/get stdio 2026-07-28: {"_meta": {...}, "resultType": "complete", "taskId": "spike-task-0001", ..., "status": "working", "statusMessage": "The fresh run for Fresh is queued."}
PASS  tasks/cancel stdio 2026-07-28: {"_meta": {...}, "resultType": "complete"}
PASS  form elicitation accept stdio 2026-07-28: elicitations=['Close this conversation?'] is_error=False text='{\n  "closed": true\n}'
PASS  form elicitation decline stdio 2026-07-28: elicitations=1 is_error=True text='The user declined this action, ...'
PASS  initialize http 2025-11-25: protocol_version=2025-11-25
PASS  tools/list verbatim http 2025-11-25: 17 tools, each equal to the Python shim's recorded entry: True; same order as Python: False
PASS  tools/call http 2025-11-25: content={"tool": "post_message"}
PASS  skills/list http 2025-11-25: {"skills": [...]}
PASS  skills/get http 2025-11-25: {"skill": {...}}
PASS  tasks/get unbound in legacy era http 2025-11-25: error code=-32601 message='method not found: "tasks/get"' (Python shim: -32601 'Method not found')
PASS  form elicitation accept http 2025-11-25: elicitations=['Close this conversation?'] is_error=False text='{\n  "closed": true\n}'
PASS  form elicitation decline http 2025-11-25: elicitations=1 is_error=True text='The user declined this action, ...'
PASS  initialize http 2026-07-28: protocol_version=2026-07-28
PASS  tools/list verbatim http 2026-07-28: 17 tools, each equal to the Python shim's recorded entry: True; same order as Python: False
PASS  tools/call http 2026-07-28: content={"tool": "post_message"}
PASS  skills/list http 2026-07-28: {"_meta": {...}, "skills": [...], "ttlMs": 300000, "cacheScope": "public", "resultType": "complete"}
PASS  skills/get http 2026-07-28: {"_meta": {...}, "skill": {...}, "ttlMs": 300000, "cacheScope": "public", "resultType": "complete"}
PASS  task-shaped tools/call result http 2026-07-28: {"_meta": {...}, "resultType": "task", "taskId": "spike-task-0001", ...}
PASS  raw tasks/get http 2026-07-28: {"_meta": {...}, "resultType": "complete", "taskId": "spike-task-0001", ..., "status": "working", ...}
PASS  tasks/cancel http 2026-07-28: {"_meta": {...}, "resultType": "complete"}
PASS  form elicitation accept http 2026-07-28: elicitations=['Close this conversation?'] is_error=False text='{\n  "closed": true\n}'
PASS  form elicitation decline http 2026-07-28: elicitations=1 is_error=True text='The user declined this action, ...'
PASS  initialize 2025-03-26 raw POST http: status=200 content-type=text/event-stream mcp-session-id present=True result.protocolVersion=2025-03-26
PASS  bearer gate rejects a missing token http: status=401 body='Unauthorized'

38 passed, 0 failed
```

The driver logs the Go server's stdio wire through `protocol/mcp-transcripts/tee_stdio.py`. The logs show the same mechanics as the Python transcripts. Era 2025-11-25 close (abridged):

```text
server_to_client {"id": 1, "method": "elicitation/create", "params": {"mode": "form", "message": "Close this conversation?", "requestedSchema": {...}}}
client_to_server {"id": 1, "result": {"action": "accept", "content": {"confirm": true}}}
```

Era 2026-07-28 close (abridged):

```text
server_to_client {"id": 9, "result": {"_meta": {...}, "content": null, "resultType": "input_required", "inputRequests": {"agentcy.mcp.confirmation:confirm_close": {"method": "elicitation/create", "params": {...}}}}}
client_to_server {"id": 10, "method": "tools/call", "params": {"inputResponses": {"agentcy.mcp.confirmation:confirm_close": {"action": "accept", "content": {"confirm": true}}}, "name": "close_conversation", "arguments": {"token": "t"}, "_meta": {...}}}
```

Indicative footprint (one stdio `initialize`, then 1 s idle, `/usr/bin/time -l`, Apple Silicon): the Go spike binary used 14.5 MB maximum RSS and about 0 s CPU. The Python shim used 107 MB and 0.61 s user CPU. The CGO-free binary is 12.6 MB.

## How the SDK covers each part

| Shim feature | SDK mechanism (all exported API) |
| --- | --- |
| Tool list verbatim | `Server.AddTool(&mcp.Tool{InputSchema: json.RawMessage(...)}, handler)`. Raw schemas reach the wire unchanged. |
| Close confirmation | The tool returns `CallToolResult{InputRequests: {id: &mcp.ElicitParams{Mode: "form", ...}}}` (multi round-trip requests, SEP-2322). In 2026-07-28 the SDK sends it as an `input_required` result. For older clients its built-in middleware sends `elicitation/create`, waits for the answer, and calls the tool again with `InputResponses`. **One handler serves both eras.** |
| Form capability check | `CallToolRequest.ClientCapabilities()` reads the per-request `_meta` capabilities in 2026-07-28 and the `initialize` capabilities before. |
| `skills/list`, `skills/get`, `tasks/get`, `tasks/cancel` (and `tasks/update`) | `mcp.AddReceivingCustomMethod(server, "skills/list", handler)` with param and result structs that embed `mcp.ParamsBase` and `mcp.ResultBase`. |
| Task-shaped `tools/call` result | A receiving middleware (`Server.AddReceivingMiddleware`) replaces the `launch_participant` result with a custom `Result` struct whose `resultType` is `"task"`. This is the same design as `McpTasksExtension.intercept_tool_call`. |
| Era gating of `tasks/*` | The handler reads the era from `_meta["io.modelcontextprotocol/protocolVersion"]` or `ServerSession.InitializeParams()`. The `-32601` reply matches Python. |
| Extensions in capabilities | `ServerCapabilities.AddExtension(...)`. |
| stdio | `server.Run(ctx, &mcp.StdioTransport{})`. |
| Streamable HTTP behind the bearer gate | `mcp.NewStreamableHTTPHandler`, wrapped in a plain `http.Handler` gate (`crypto/subtle`). |

### The one structural gap and its workaround

The SDK serves the 2026-07-28 era over HTTP only on a **stateless** handler. A stateful handler answers every 2026 request except `server/discover` with `-32022` (HTTP 400), and the Python client then falls back to 2025-11-25. That was the first HTTP 2026 failure in this spike. A stateless handler cannot do the 2025-11-25 server-to-client elicitation, because that needs a session. The fix is about 15 lines of exported API: two handlers on the same `*mcp.Server` and a router that sends a request to the stateless handler when its `MCP-Protocol-Version` header is `2026-07-28` or later (`eraRouter` in `main.go`). Every 2026 request carries that header, `server/discover` included.

## Differences between the Go SDK and the Python shim on the wire

None of these broke the Python client. The port must decide on each:

1. **Tool order.** The Go SDK lists tools sorted by name. Python lists them in declaration order. The entries themselves are equal. The e2e test `test_both_eras_see_the_same_instructions_tools_and_resources` compares two eras of the same server, so it is not affected. Tool order is prompt-visible to hosts. If it matters, keep the declaration order by naming, or accept the sorted order.
2. **`"content": null` on an `input_required` result.** Python omits `content`. The Python client accepts null. A stricter host could object. A server sending middleware can strip it if needed.
3. **No `requestState`.** Python returns a signed `requestState` with the input request and checks it on the retry. The spike sends none. The SDK supports the field (`CallToolResult.RequestState`, `CallToolParamsRaw.RequestState`). The port should sign it (HMAC with a per-process key) to keep parity, so that a 2026 client cannot answer a confirmation it was never asked.
4. **Cache hints.** Go puts `ttlMs: 0, cacheScope: "public"` on `tools/list` in both eras. Python puts `ttlMs: 0, cacheScope: "private"` and only in 2026-07-28. The HTTP and stdio instructions differ, so `private` is the safer value for `server/discover`. Check whether the SDK exposes the scope; if not, a sending middleware can set it.
5. **`server/discover` `supportedVersions`.** Go lists all five versions. Python lists only `["2026-07-28"]`. Go is the more useful answer.
6. **Extensions in the 2025-11-25 `initialize` result.** Go advertises both extensions in every era. Python advertises none in 2025-11-25 (and still serves `skills/*`). The Go form is more correct.
7. **`-32601` text.** The SDK rewrites the message to `method not found: "tasks/get"`. Python sends `Method not found` with `data: "tasks/get"`. Only the code is checked anywhere.
8. **Elicitation schema.** Python's 2025-11-25 `requestedSchema` also carries `title` and `description` of the form model. In 2026-07-28 it does not. The port can send any fixed schema.
9. **Host header check.** The SDK's built-in DNS rebinding check answers 403 for any non-loopback `Host`. Python allows the configured tunnel host and answers 421 for others (`test_http_bridge_accepts_only_the_configured_public_host` in `tests/e2e/test_http_bridge.py`). The port sets `StreamableHTTPOptions.DisableLocalhostProtection` and keeps its own Host and Origin allowlist in the gate.

## How the two protocol eras differ on the wire

Source: `protocol/mcp-transcripts/*.json`.

| Aspect | 2025-11-25 (handshake era) | 2026-07-28 (stateless era) |
| --- | --- | --- |
| Connect | `initialize` with `protocolVersion`, `capabilities`, `clientInfo`; result with `protocolVersion`, `capabilities`, `serverInfo`, `instructions`; then notification `notifications/initialized` | `server/discover`; result with `supportedVersions`, `capabilities`, `instructions`, `resultType`, `ttlMs`, `cacheScope`, and `serverInfo` in `_meta`. No notification. |
| Client identity and capabilities | Once per connection, in `initialize` | On **every** request, in `params._meta`: `io.modelcontextprotocol/protocolVersion`, `io.modelcontextprotocol/clientInfo`, `io.modelcontextprotocol/clientCapabilities`. A request without params gets a `params` object only for this `_meta`. |
| Result envelope | Plain result | Every result has `resultType` (`complete`, `input_required` or `task`). The Python shim also adds `_meta["io.modelcontextprotocol/serverInfo"]` to every result. |
| Cache hints | None | `ttlMs` and `cacheScope` on `server/discover`, `tools/list` (0, private), `skills/list` and `skills/get` (300000, public) |
| Elicitation | Server-to-client request `elicitation/create` with a server-assigned ID, sent while the `tools/call` is open. The client answers it. Then the `tools/call` result arrives. | No server-to-client request. `tools/call` returns `{resultType: "input_required", inputRequests: {<id>: {method: "elicitation/create", params}}, requestState}`. The client sends a new `tools/call` (new JSON-RPC ID) with the same `name` and `arguments` plus `inputResponses: {<id>: <ElicitResult>}` and the echoed `requestState`. |
| MCP Tasks extension | `tasks/*` not bound (`-32601`). `launch_participant` returns its normal result. | `tasks/get`, `tasks/cancel`, `tasks/update` bound. They and the task-shaped `tools/call` result apply only when that request's own `clientCapabilities.extensions` names `io.modelcontextprotocol/tasks`. |
| HTTP session | `initialize` response sets `mcp-session-id`. Later requests send it plus `mcp-protocol-version: 2025-11-25`. `DELETE` ends the session. | No session ID and no `DELETE`. Every POST sends `mcp-protocol-version: 2026-07-28` and `mcp-method: <method>`, plus `mcp-name: <tool>` for `tools/call`. |
| HTTP response body | `text/event-stream`. The `elicitation/create` request arrives on the SSE stream of the `tools/call` POST. The client answers with a separate POST (202). The final result arrives on the first stream. | `application/json`, one response per POST |
| `ping` | Available | Removed |

## Recommendation

**Use the official Go SDK, v1.8.0.** It covers the whole shim surface with exported API: custom methods for `skills/*` and `tasks/*`, per-request capabilities, the 2026-07-28 era, and multi round-trip input requests. The input request mechanism removes the hardest part of the fallback: one tool handler serves the elicitation in both eras. The spike server is 406 lines including the HTTP gate and the era router. A hand-written layer would cost about **1,000 to 1,400 lines plus tests**: newline-delimited JSON-RPC with concurrent requests and server-to-client calls, a method table, `initialize` and `server/discover`, per-request `_meta` handling, the `input_required` retry, and a streamable HTTP handler with sessions and SSE for the old era and JSON for the new one. That is above the plan's 600 to 900 line estimate, because the plan did not yet count the 2026-07-28 era.

Follow-ups for M2:

- Pin the SDK in the root `go.sum`. The 2026-07-28 support is new (v1.8.0 is nine days old), so keep `tests/e2e/test_protocol_eras.py` and `test_mcp_tasks_stdio.py` as the gate for each SDK upgrade.
- Sign `requestState` (difference 3), set `DisableLocalhostProtection` and keep the Host/Origin allowlist (difference 9), and decide on tool order (difference 1).
- Keep the plugin client hand-written, as planned. The daemon must not import the SDK.

If a future SDK release breaks a criterion, a hybrid stays sensible: keep the SDK's exported protocol types (`mcp.Tool`, `CallToolResult`, `ElicitParams`) and its `jsonrpc` package, and own the dispatch. Today it is not needed.

## Files

- `main.go`: the toy server (all 17 recorded tools, with special handlers for `close_conversation` and `launch_participant`; `skills/*`; `tasks/*`; stdio; HTTP with bearer gate and era router).
- `driver.py`: the Python driver on the unmodified mcp 2.3.0 `Client` from this repo's `uv` environment.
- `go.mod`, `go.sum`: a nested module, separate from the root module.

# HTTP MCP Performance Tests (k6)

`bench/k6/` measures `agentcy`'s MCP Streamable HTTP transport (`agentcy --workspace DIR --http PORT` at `/mcp`) using [k6](https://k6.io/), covering both supported HTTP protocol eras:

- **Modern (`2026-07-28`)**: stateless per-request JSON-RPC over HTTP (`application/json`).
- **Legacy (`2025-11-25`)**: stateful session-based Streamable HTTP (`initialize` → `Mcp-Session-Id` → `notifications/initialized` → `text/event-stream` SSE responses).

## Prerequisites

Install `k6` locally (for example on macOS):

```sh
brew install k6
```

## Run (self-contained)

`make bench-http` builds `bin/agentcy`, creates an isolated temporary `AGENTCY_HOME` and workspace under `/tmp` (mode `0700`), starts `agentcy --http` with an ephemeral random Bearer token, runs `k6`, and stops both the HTTP shim and the core daemon on exit. It never touches `~/.agentcy`.

```sh
# Quick smoke run (both eras, ~10 s)
make bench-http

# Load benchmark (both eras, ramping to 10 VUs per era + lifecycle VUs, ~40 s)
make bench-http K6_PROFILE=load

# Filter to one MCP protocol era
make bench-http K6_PROFILE=smoke K6_ERA=2026-07-28
make bench-http K6_PROFILE=smoke K6_ERA=2025-11-25

# Export full k6 JSON summary
make bench-http K6_PROFILE=load K6_SUMMARY_EXPORT="$TMPDIR/k6-http-summary.json"
```

Pass extra k6 flags through `K6_FLAGS`, for example `make bench-http K6_FLAGS="--no-color"`.

## Run against an existing HTTP bridge

To run directly against an already-running `agentcy --workspace DIR --http PORT`:

```sh
BASE_URL="http://127.0.0.1:8765/mcp" \
  AGENTCY_HTTP_TOKEN="$AGENTCY_HTTP_TOKEN" \
  K6_PROFILE=smoke \
  K6_ERA=both \
  k6 run bench/k6/http_load.js
```

## Scenarios & Thresholds

| Scenario | Protocol era | Operations |
| --- | --- | --- |
| `modern_steady` | `2026-07-28` | `post_message`, incremental `read_messages` (`wait_seconds: 0`), `conversation_status`, `list_closed_conversations`, periodic `tools/list` |
| `legacy_steady` | `2025-11-25` | Same steady-state mix over a reusable per-VU `Mcp-Session-Id` session |
| `lifecycle` | Both (alternating) or selected era | End-to-end `create_conversation` → `join_conversation` → `post_message` → `read_messages` → `conversation_status` → `close_conversation` |

Default thresholds (aligned with the `< 100 ms` local warm-query target in `DESIGN.md`):

- `http_req_failed < 1%` and `checks > 99%`
- `p95 < 100 ms` for `read_messages`, `conversation_status`, `post_message`, `list_closed_conversations`, and per-era aggregate `mcp_tool_duration`
- `p95 < 150 ms` for `create_conversation` and `join_conversation`

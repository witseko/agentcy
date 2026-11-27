# Performance tests

Agentcy has two performance suites. Each one starts its own `agentcy` processes in a temporary `AGENTCY_HOME` under `/tmp` (mode 0700), stops them at the end, and never touches `~/.agentcy`.

| Suite | What it measures | Command | Needs |
| --- | --- | --- | --- |
| Core benchmark (`bench/`) | One shim and daemon pair over stdio and the local socket: warm latency, shim start time, and a load scenario with many listening agents | `make bench` | `sqlite3` with FTS5, `lsof`, `ps` |
| HTTP load test (`bench/k6/`) | The MCP Streamable HTTP transport (`agentcy --http`, path `/mcp`) under concurrent clients | `make bench-http` | [k6](https://k6.io/) |

Both commands build `bin/agentcy` first. Run them on a quiet machine: other load shows up in every number.

## Core benchmark

```bash
# Full run: 100,000 messages, 1,000 samples per operation, 20 listeners, 60 s load
make bench BENCH_FLAGS="-json $TMPDIR/bench-go.json"
```

```bash
# Quick smoke run
make bench BENCH_FLAGS="-messages 2000 -samples 50 -listeners 3 -seconds 10 -coldruns 2"
```

The driver prints a table and, with `-json FILE`, writes the same numbers as JSON. It reports:

- **Warm latency** (p50, p95) for member read, member status, member post, `operator_show` and `operator_search`.
- **Shim start**: time to the first tool result, cold (no daemon running) and warm.
- **Load**: CPU seconds, peak memory, `git` processes spawned, and wake latency while idle agents wait on `read_messages`.

The reference numbers for an Apple M1 are in [Development: Performance](development.md#performance). Results on macOS and Ubuntu are in [Performance results](performance-results.md). The flags, the dataset, and how to compare two JSON reports are in [bench/README.md](../bench/README.md).

## HTTP load test

Install k6 once:

```bash
brew install k6
```

On Linux, follow the [k6 install guide](https://grafana.com/docs/k6/latest/set-up/install-k6/). On arm64, use the binary from the [GitHub release](https://github.com/grafana/k6/releases), because the k6 apt repository has no arm64 package.

Then run one of the profiles:

```bash
# Smoke run, both protocol versions, about 10 s
make bench-http
```

```bash
# Load run, up to 10 virtual users per protocol version, about 40 s
make bench-http K6_PROFILE=load
```

The runner starts `agentcy --http` on a free port with a random access token and runs `bench/k6/http_load.js` against it. Options:

| Variable | Values | Default |
| --- | --- | --- |
| `K6_PROFILE` | `smoke`, `load` | `smoke` |
| `K6_ERA` | `both`, `2026-07-28` (stateless JSON), `2025-11-25` (session with `Mcp-Session-Id` and SSE) | `both` |
| `K6_SUMMARY_EXPORT` | Path for the full k6 JSON summary | none |
| `K6_FLAGS` | Extra flags for `k6 run`, for example `--no-color` | none |

The run fails when a threshold fails:

- More than 1% of HTTP requests fail, or fewer than 99% of checks pass.
- p95 latency is above 100 ms for `post_message`, `read_messages`, `conversation_status`, `list_closed_conversations`, or all tool calls of one protocol version.
- p95 latency is above 150 ms for `create_conversation` or `join_conversation`.

The 100 ms limit is the local warm-query target from the [design](../DESIGN.md).

To test a server that is already running, call k6 directly:

```bash
BASE_URL="http://127.0.0.1:8765/mcp" AGENTCY_HTTP_TOKEN="$AGENTCY_HTTP_TOKEN" k6 run bench/k6/http_load.js
```

The scenarios and the operations in each one are in [bench/k6/README.md](../bench/k6/README.md).

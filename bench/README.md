# Benchmark

`bench/` is a Go program (build tag `bench`) that measures one shim and daemon
implementation. Two environment variables select it, so the same driver measures the
Go core and the Python reference:

- `AGENTCY_CORE_CMD`: the daemon command. The driver splits it on white space, without a
  shell, so no argument can contain a space.
- `AGENTCY_BIN`: the absolute path of the shim executable.

The driver creates a fresh `AGENTCY_HOME` (mode 0700, under `/tmp` because of the macOS
socket path limit) and a fresh workspace. It never touches `~/.agentcy`. It stops every
process it started and removes both directories at the end, also after an error or
Ctrl-C. It never prints tokens, join codes, or the operator key.

It needs `sqlite3` (with FTS5, as on macOS and most Linux distributions), `lsof`, and `ps`.
Its unit tests run with `go test -tags bench ./bench`; plain `go test ./...` skips it.

## Run

```sh
# Go core in bin/, default flags (100,000 messages, 1,000 samples, 20 listeners, 60 s)
make bench BENCH_FLAGS="-json bench-go.json"

# Python reference (needs make plugin-deps)
make bench AGENTCY_CORE_CMD="$PWD/.venv/bin/python -I -m agentcy.mcp.core" \
  AGENTCY_BIN="$PWD/.venv/bin/agentcy" BENCH_FLAGS="-json bench-python.json"

# Quick smoke run
make bench BENCH_FLAGS="-messages 2000 -samples 50 -listeners 3 -seconds 10 -coldruns 2"
```

`make bench` builds `bin/agentcy` and measures it as both shim and core. For the Python
reference, use `.venv/bin/python -I -m agentcy.mcp.core`, the command the Python shim itself
runs. `uv run python ...` also works, but `uv` stays in the daemon's process group and adds
its memory to the daemon numbers.

Flags: `-messages`, `-samples`, `-listeners`, `-seconds`, `-coldruns`, `-postevery`
(seconds between writer posts), `-json FILE`. Run it on a quiet machine: other load shows
up in every number.

## What it measures

**Dataset.** One conversation with a lead, a `Bench` worker, and up to 15 listener slots
(the 17 participant cap). More listeners than slots share slot tokens. The daemon creates
the conversation and the joins. Then the driver stops the daemon and writes most rows with
the `sqlite3` CLI in one transaction, in the shape the daemon writes: message rows with
notes, findings, questions, tasks from the lead and `done` replies from the task owner,
plus a submission receipt per row. SQLite's own triggers fill the search index. It refuses
a store whose version is not 15 or whose message columns differ. It then restarts the
daemon, posts the last rows (up to 200) through the socket, and reads back the last
message ID, a search hit on a marked direct row, and a member read near the end. Posting
100,000 rows one by one through the Python daemon takes too long, so the direct write is
the default. Directly written rows add no token usage estimates.

**Warm latency.** Requests over the socket with the daemon running, one at a time, after
10 untimed warmup requests. Percentiles are nearest-rank.

| Operation | Request |
| --- | --- |
| member read (incremental) | `read` by `Bench` after a cursor 20 messages before the end, no wait |
| member status | `status` by `Bench` |
| operator_show (tail) | `operator_show` with `tail: true` |
| operator_search | `operator_search` with one or two vocabulary words, varied per sample |
| member post | `post` by `Bench` with a unique body and submission ID |

**Shim start.** Time from spawning `AGENTCY_BIN --workspace DIR` to the first successful
tool result over stdio: `initialize`, `notifications/initialized`, then
`list_closed_conversations`, which needs no token but opens the store. Cold start has no
daemon running, so the shim starts its own daemon the way it does for a user (the Python
shim runs its own interpreter, or `AGENTCY_CORE_BIN` when set). The driver stops that
daemon between runs. Warm start has the daemon already running. `max` is the stated bound.

**Load scenario.** For `-seconds`, `-listeners` shims each loop on `read_messages` with
`wait_seconds=45`, as an idle agent does. One poller sends `operator_show` with a tail
every second, as the terminal UI does. One writer posts a note from the lead every
`-postevery` seconds, which wakes every listener. The window opens 2 s after the last
listener starts.

| Number | Meaning |
| --- | --- |
| Processes measured | Most processes seen in one sample: the daemon's process group plus the shims |
| CPU seconds | User plus system time used inside the window, from a sample every second: `ps -A -o pid=,pgid=,rss=,time=` on macOS (0.01 s resolution), `/proc/<pid>/stat` on Linux. A process that exits between samples loses its time after the last sample |
| Peak RSS | Largest sum of resident memory over the measured processes in one sample |
| git processes spawned by the daemon | Exact count in the window: the daemon's `PATH` starts with a `git` wrapper script that logs each call and runs the real git |
| Wakes observed / expected | Probe deliveries the listeners saw, against probes times listeners |
| Wake latency | From just before the writer sends a post to the moment a listener's tool result arrives. The Python core collects a burst for 2 s before it answers a waiting read (`SETTLE_SECONDS` in `src/agentcy/mcp/concurrency.py`), so its wake latency starts at about 2 s |

Fresh runs through a plugin are not part of the load scenario yet. The M5 acceptance in the
port plan adds two fresh runs on the fake plugin (`tests/fixtures/fake_run_plugin.py`);
that needs a `plugins.json` in the temporary home and a lead `launch` call, and is a
separate change.

## Compare two reports

The JSON file holds the same numbers as the printed table. Compare a baseline and a
candidate with `jq`:

```sh
jq -rn --slurpfile a bench-python.json --slurpfile b bench-go.json '
  ($a[0].warm_latency | map({(.operation): .stats.p95_ms}) | add) as $pa
  | ($b[0].warm_latency | map({(.operation): .stats.p95_ms}) | add) as $pb
  | (["metric", "baseline", "candidate"] | @tsv),
    ($pa | keys[] as $k | ["p95 " + $k, $pa[$k], $pb[$k]] | @tsv),
    (["cold start max ms", $a[0].cold_start.max_ms, $b[0].cold_start.max_ms] | @tsv),
    (["load cpu s", $a[0].load.cpu_seconds, $b[0].load.cpu_seconds] | @tsv),
    (["load peak rss MB", $a[0].load.peak_rss_mb, $b[0].load.peak_rss_mb] | @tsv),
    (["load git spawns", $a[0].load.git_spawns, $b[0].load.git_spawns] | @tsv),
    (["wake p95 ms", $a[0].load.wake_latency.p95_ms, $b[0].load.wake_latency.p95_ms] | @tsv)'
```

Compare only reports from the same machine, flags, and dataset size; the `environment`
block records them.

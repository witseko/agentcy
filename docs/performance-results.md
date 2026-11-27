# Performance results

These are the results of the performance tests from 2026-11-27, on macOS and on Ubuntu. To run the tests yourself, see [Performance tests](performance.md).

## Summary

- Every end-to-end test passes on Ubuntu, and the HTTP load test passes every threshold.
- Every warm operation stays well below the 100 ms p95 target of the [design](../DESIGN.md). The slowest is `operator_search`, at 25 to 27 ms p95.
- The Ubuntu VM is 2 to 6 times slower on socket operations. This comes from the VM, not from Linux.
- The tests found an `operator_show` regression (about 7.6 ms p95 in place of about 1 ms). Commit `395ee2f` fixes it. See [The `operator_show` regression](#the-operator_show-regression).

## Environments

| Name | System | Go |
| --- | --- | --- |
| macOS | A macOS arm64 host | 1.27.4 |
| Ubuntu VM | Ubuntu 24.04 LTS arm64 in a [Multipass](https://canonical.com/multipass) VM with 4 vCPUs, 4 GB RAM, and a 20 GB disk, on the macOS host | 1.26.11 |

Every run used the default flags of `make bench`: one conversation of 100,000 messages (99,800 written with the `sqlite3` CLI, 200 posted through the socket), 1,000 warm samples per operation, and a 60 s load scenario with 20 listening shims, one 1 Hz `operator_show` poller, and one writer every 5 s. The VM was stopped during the macOS runs.

## End-to-end tests on Ubuntu

| Suite | Result |
| --- | --- |
| Python end-to-end tests (`tests/e2e`) | 28 passed |
| Terminal UI end-to-end test (`go test -tags e2e ./tui/...`) | Passed |

The behavior on Linux matches macOS.

## Core benchmark

Commit `395ee2f`, which includes the `operator_show` fix.

| Number | macOS | Ubuntu VM |
| --- | --- | --- |
| Member read p95 (ms) | 0.44 | 2.49 |
| Member status p50 / p95 (ms) | 0.33 / 0.61 | 0.56 / 1.21 |
| `operator_show` tail p50 / p95 (ms) | 0.73 / 1.11 | 1.08 / 2.13 |
| `operator_search` p50 / p95 (ms) | 10.07 / 27.35 | 12.15 / 25.37 |
| Member post p95 (ms) | 0.76 | 2.74 |
| Shim start, cold max (ms) | 78.3 | 123.2 |
| Shim start, warm max (ms) | 23.8 | 7.7 |
| Load CPU seconds, total (daemon) | 1.17 (0.86) | 1.59 (1.20) |
| Load peak RSS, total (daemon), MB | 478.9 (41.6) | 346.9 (31.4) |
| `git` processes in the load window | 1 | 1 |
| Wakes observed / expected, errors | 240 / 240, 0 | 220 / 220, 0 |
| Wake latency p50 / p95 (ms) | 2,014 / 2,021 | 2,084 / 2,139 |

In the VM, socket operations are 2 to 6 times slower and the cold shim start is about 60% slower. A macOS run of the same commit does not show these differences, so they come from the VM (fewer vCPUs, a virtual disk), not from a Linux code path. Wake latency starts at about 2 s because of the settle window.

## HTTP load test on Ubuntu

Commit `31dd9ee`. The `operator_show` fix does not touch the HTTP tools that this test calls. Both profiles pass every threshold.

| Profile | Requests | Requests per second | Failed | All tools p50 / p95 (ms) | Slowest p95 |
| --- | --- | --- | --- | --- | --- |
| `smoke` (5 virtual users, 10 s) | 2,439 | 235.8 | 0% | 4.27 / 9.87 | `create_conversation` 12.33 ms |
| `load` (22 virtual users, 40 s) | 26,656 | 662.9 | 0% | 16.00 / 32.99 | `create_conversation` 45.09 ms |

## The `operator_show` regression

The first runs showed `operator_show` tail p95 at about 7.6 ms on macOS and 8.2 ms in the VM. Earlier recorded runs had about 1 ms.

- **Cause.** Commit `ba98da5` changed `transcript()` to count every message of the room with `SELECT count(*)` on each call, in place of the count in the cached room snapshot. With 100,000 messages, that query takes about 7 ms. A bisect over the 12 code commits after the last good benchmark found this commit. The Go version and the operating system have no effect on it.
- **Fix.** Commit `395ee2f` takes the count from a room snapshot that is current in the same transaction, and counts the rows only when no current snapshot exists. It never builds a snapshot, so reading a closed conversation still replays no tasks and adds no room to the cache.

| Number | macOS before | macOS after | Ubuntu VM before | Ubuntu VM after |
| --- | --- | --- | --- | --- |
| `operator_show` tail p50 / p95 (ms) | 6.71 / 7.63 | 0.73 / 1.11 | 6.97 / 8.17 | 1.08 / 2.13 |
| Load CPU seconds, total | 1.82 | 1.17 | 2.71 | 1.59 |

The load CPU also drops, because the load scenario polls `operator_show` once a second.

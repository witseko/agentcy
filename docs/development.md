# Development

Read the [design](../DESIGN.md) and the [project instructions](../AGENTS.md) before you change behavior.

## Layout

The core is Go: one static binary, `agentcy`, built from `cmd/agentcy` and the packages under `internal/`. The terminal UI is a second Go binary from `tui/cmd/agentcy-tui`. Both belong to one Go module at the repository root.

Python is used only for tests and tools: the end-to-end tests in `tests/e2e`, the fixture plugin in `tests/fixtures`, and the tutorial video tools in `docs/videos/tools`. Commit `17eb9ce` is the last commit with the former Python core.

## Tests and checks

Build `bin/agentcy` and the terminal UI, then run the Go checks. CI runs the same commands:

```bash
make build tui
```

```bash
test -z "$(gofmt -l .)"
go vet ./...
go vet -tags conformance,e2e,bench ./...
go test ./...
go test -race ./internal/...
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
```

The Python checks cover the end-to-end tests and the video tools:

```bash
uv sync --locked
```

```bash
uv run --locked ruff check .
uv run --locked pytest
```

The end-to-end tests drive the shim with two independent MCP clients from the Python SDK, with providers disabled. `AGENTCY_BIN` selects the shim and `AGENTCY_CORE_CMD` the core. Both are required, so plain `pytest` does not run these tests:

```bash
AGENTCY_BIN="$PWD/bin/agentcy" AGENTCY_CORE_CMD="$PWD/bin/agentcy core" uv run --locked pytest tests/e2e
```

The terminal UI end-to-end test starts a real core. It needs the uv environment for its fixture plugin:

```bash
AGENTCY_CORE_CMD="$PWD/bin/agentcy core" uv run --locked go test -tags e2e ./tui/...
```

Semgrep scans the Python files in `tests/e2e` and `tests/fixtures` with Python and MCP security rules. Semgrep skips `tests/` directories by default, so the command names the files. CI fails on unsuppressed findings or scan errors. Git ignore rules are disabled. Semgrep's own path, file type, and size filters apply. Add `--disable-nosem` to review findings covered by inline suppressions.

```bash
(
  rules_dir="$(mktemp -d)" || exit
  trap 'rm -rf -- "$rules_dir"' EXIT
  git -C "$rules_dir" init -q &&
    git -C "$rules_dir" fetch -q --depth=1 https://github.com/semgrep/semgrep-rules.git \
      a5ad1714f00c9f7fde980cab0ea1d6dc6d414fd2 &&
    git -C "$rules_dir" checkout -q --detach FETCH_HEAD &&
    uvx --from semgrep==1.186.0 semgrep scan \
      --config "$rules_dir/python/lang/security" \
      --config "$rules_dir/ai/ai-best-practices/mcp-command-injection/mcp-command-injection.yaml" \
      --config "$rules_dir/ai/ai-best-practices/mcp-ssrf/mcp-ssrf.yaml" \
      --error --strict --metrics=off --disable-version-check --no-rewrite-rule-ids --no-git-ignore \
      tests/e2e/*.py tests/fixtures/*.py
)
```

Audit the locked third-party runtime and development dependencies.

```bash
(
  audit_requirements="$(mktemp)" || exit
  trap 'rm -f -- "$audit_requirements"' EXIT
  uv export --locked --format requirements.txt --no-emit-project --quiet \
    --output-file "$audit_requirements" &&
    uv run --no-sync pip-audit --strict --require-hashes --disable-pip \
      --requirement "$audit_requirements"
)
```

## Daemon conformance suite

The suite in `conformance/` pins the behavior of the core daemon as a black box. It starts the daemon that `AGENTCY_CORE_CMD` names, sends requests over the Unix socket, and compares every normalized response with a golden file in `conformance/testdata/`. The first goldens were recorded from the former Python core. `make conformance` builds `bin/agentcy` and tests it:

```bash
make conformance
```

Each test starts its own daemon with a new private `AGENTCY_HOME` and a new workspace under `/tmp`, and stops the daemon at the end. The suite never reads or writes `~/.agentcy`. Without the `conformance` build tag, `go test ./...` skips the suite.

Normalization replaces values that change between runs with placeholders: `<room-1>` for a conversation ID, `<code-1>` for a join code, `<token-1>` for a participant token, `<workspace-1>` for a workspace key, `<proposal-1>` for a proposal ID, `<checkout>` and `<home>` for temp paths, and `<now>` or `<now+30m>` for a Unix time relative to the daemon start. The first distinct value of a kind gets number 1. The goldens keep the key order of every object and the exact text of every error.

To add a scenario:

1. Copy a test function in `conformance/*_test.go`. Start it with `newScenario(t)`, or `newGitScenario(t)` when the workspace key matters.
2. Send requests with `s.member`, `s.post` and `s.operator`. Give each step a label that says what it checks. Read values for later steps with `reply.text` and `reply.number`.
3. Record the golden with `-update`, read the new file in `conformance/testdata/`, then run the suite three times without `-update` to confirm the result does not change.

Do not put text in a request that looks like a generated value: a run of 24, 32 or 64 characters from `0-9a-f`, or a run of 43 URL-safe characters. Normalization would replace it.

## Performance

`bench/` measures one shim and daemon pair (see [bench/README.md](../bench/README.md)). `make bench` measures `bin/agentcy`.

 The numbers below were taken on 2026-11-26 on an Apple M1 (8 cores, macOS, Darwin 27.2.0, Go 1.27.4) with nothing else running, with the default flags: one conversation of 100,000 messages (99,800 written with the `sqlite3` CLI, 200 posted through the socket), 1,000 warm samples per operation, and a 60 s load scenario with 20 listening shims, one 1 Hz `operator_show` poller, and one writer every 5 s. "Go after M5" shows both runs of the final build.

| Number | Python shim and core | Go shim and core before M5 | Go after M5 (run 1 / run 2) |
| --- | --- | --- | --- |
| Member read p95 (ms) | 1.71 | 0.33 | 0.44 / 0.37 |
| Member status p50 / p95 (ms) | 37.5 / 42.1 | 48.7 / 49.2 | 0.30 / 0.51, 0.30 / 0.49 |
| `operator_show` tail p50 / p95 (ms) | 59.6 / 62.1 | 55.1 / 55.5 | 0.70 / 0.95, 0.69 / 0.94 |
| `operator_search` p50 / p95 (ms) | 23.6 / 33.6 | 8.9 / 25.6 | 9.9 / 25.7, 10.7 / 25.8 |
| Member post p95 (ms) | 1.42 | 0.72 | 0.69 / 0.74 |
| Shim start, cold max (ms) | 907 | 74.3 | 74.5 / 74.1 |
| Shim start, warm max (ms) | 461 | 21.1 | 20.1 / 20.6 |
| Load CPU seconds, total (daemon) | 13.97 (13.65) | 17.35 (17.15) | 1.11 (0.86) / 1.43 (1.04) |
| Load peak RSS, total (daemon), MB | 2,145 (114) | 487 (53) | 472 (41) / 473 (40) |
| `git` processes in the load window | 60 | 1 | 1 / 1 |
| Wake latency p50 / p95 (ms) | 2,453 / 2,820 | 2,542 / 2,984 | 2,017 / 2,031, 2,019 / 2,031 |

Most of the remaining peak RSS belongs to the 20 shims, about 21 MB each. The one `git` process per window is the 60 s refresh of the cached workspace key (plan fix L7), not one per request. The daemon holds each wake for the 2 s settle window, so wake latency cannot go below about 2 s.

Cold start bound: a shim with no daemon running reaches its first tool result in at most 75 ms on this machine (5 runs). After a daemon start, the first status or `operator_show` of each room replays all of its messages once to fill the room cache, which takes about as long as every status took before M5 (about 50 ms at 100,000 messages); later calls read only new messages.

Reproduce:

```bash
make bench BENCH_FLAGS="-json $TMPDIR/bench-go.json"
```

For the MCP Streamable HTTP transport (`--http`), see [bench/k6/README.md](../bench/k6/README.md) and run `make bench-http` (requires `k6`).

## Go conventions

The global guides have no Go guide, so the core follows these rules on top of `code-quality.md`, `security.md`, and `backend.md`:

- Format with `gofmt` and keep `go vet` clean, also with the `conformance`, `e2e`, and `bench` build tags.
- Give every package, type, and non-trivial function a doc comment that says what it does.
- Return errors as values. Wrap them with `%w` where a caller checks them with `errors.Is` or `errors.As`. Panic only on a programming error.
- Keep no mutable global state. Create shared resources, such as the store registry in `internal/store`, once in the daemon and pass them in.
- Pass `now` into code that reads the time, so tests can fix it.
- Use ordered values (`wire.Value`, slices of fields), not maps, for anything that reaches JSON. Field order is part of the protocol.
- In the core, run every process with an argument slice and an explicit `cmd.Env`. Never use a shell. Find programs with `paths.FindOnPath`, not `exec.LookPath`, which also accepts relative `PATH` entries.
- Never log tokens, join codes, the operator key, MCP task IDs, or message bodies.

## Changes from the Python versions

Versions before the Go core ran a Python core. The Go core gives the same responses, with these changes that a user can notice after an upgrade:

- **Fresh runs.** The assignment worker inspects a running fresh run every 5 seconds, not every second. A state change shows up to 5 seconds later. A stop or a cancel stays immediate.
- **SIGTERM.** The Go core stops cleanly: it stops accepting, ends waiting reads, gives running requests 5 seconds, removes the socket, and closes the stores.
- **Store versions.** The Go core opens only stores at schema version 15. It answers `Unsupported bridge database version` for any other store and logs the steps that fix it to `core.log`. An older store migrates with the Python code at commit `17eb9ce` (see [Upgrade from a Python version](upgrade-from-python.md)).
- **`listen`.** The Go core adds the member action `listen`, one exchange for a waiting read and its status (see the [protocol](../protocol/README.md#go-core-only-listen)).

## Release binaries

Each GitHub release holds two binaries for each platform, `agentcy-<os>-<arch>` and `agentcy-tui-<os>-<arch>`, and a `SHA256SUMS` file. The repository holds no binaries. Without Go, `make setup` downloads the two for your machine with `scripts/install-release.sh`, which checks each SHA-256 against `SHA256SUMS`.

To build all of them locally into `dist/`, which Git ignores:

```bash
make release-files
```

The build is reproducible and static (`CGO_ENABLED=0`): the same source, version, and Go release give the same bytes on every machine. The release workflow builds with Go 1.27.4, so use that Go release to compare a local build with a release binary.

## Versions and releases

The `VERSION` file holds the version. The Makefile and the release workflow read it. Both binaries report it:

```bash
bin/agentcy --version
```

```bash
bin/agentcy-tui --version
```

To make a release:

1. Set the new version. The target writes `VERSION`:

   ```bash
   make release VERSION=0.0.2
   ```

2. Commit the changes and merge them into `main`.
3. Tag that commit and push the tag:

   ```bash
   git tag v0.0.2 && git push origin v0.0.2
   ```

The tag starts the release workflow. It checks that the tag matches `VERSION` and builds the eight binaries from the tagged source. Then it publishes a GitHub release with the eight binaries and a `SHA256SUMS` file.

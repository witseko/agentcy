# Development

Read the [design](../DESIGN.md) and the [project instructions](../AGENTS.md) before you change behavior.

## Tests and checks

```bash
uv sync --locked
```

```bash
uv run --locked ruff check .
uv run --locked pytest
```

Run the end-to-end tests with two stdio clients and the local core, with providers disabled.

```bash
uv run --locked pytest tests/e2e
```

Semgrep scans `src/` and `plugins/` with Python, SQLAlchemy, and MCP security rules. CI fails on unsuppressed findings or scan errors. Git ignore rules are disabled. Semgrep's own path, file type, and size filters apply. Add `--disable-nosem` to review findings covered by inline suppressions.

```bash
(
  rules_dir="$(mktemp -d)" || exit
  trap 'rm -rf -- "$rules_dir"' EXIT
  git -C "$rules_dir" init -q &&
    git -C "$rules_dir" fetch -q --depth=1 https://github.com/semgrep/semgrep-rules.git \
      a5ad1714f00c9f7fde980cab0ea1d6dc6d414fd2 &&
    git -C "$rules_dir" checkout -q --detach FETCH_HEAD &&
    uvx --from semgrep==1.186.0 semgrep scan \
      --config "$rules_dir/python/lang/security" --config "$rules_dir/python/sqlalchemy/security" \
      --config "$rules_dir/ai/ai-best-practices/mcp-command-injection/mcp-command-injection.yaml" \
      --config "$rules_dir/ai/ai-best-practices/mcp-ssrf/mcp-ssrf.yaml" \
      --error --strict --metrics=off --disable-version-check --no-rewrite-rule-ids --no-git-ignore \
      src/ plugins/
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

The suite in `conformance/` pins the behavior of the core daemon as a black box. It starts the daemon that `AGENTCY_CORE_CMD` names, sends requests over the Unix socket, and compares every normalized response with a golden file in `conformance/testdata/`. The goldens come from the Python core, so a Go core must give the same responses.

```bash
make conformance
# or, for another daemon:
AGENTCY_CORE_CMD="uv run python -I -m agentcy.mcp.core" go test -tags conformance ./conformance/...
```

Each test starts its own daemon with a new private `AGENTCY_HOME` and a new workspace under `/tmp`, and stops the daemon at the end. The suite never reads or writes `~/.agentcy`. Without the `conformance` build tag, `go test ./...` skips the suite.

Normalization replaces values that change between runs with placeholders: `<room-1>` for a conversation ID, `<code-1>` for a join code, `<token-1>` for a participant token, `<workspace-1>` for a workspace key, `<proposal-1>` for a proposal ID, `<checkout>` and `<home>` for temp paths, and `<now>` or `<now+30m>` for a Unix time relative to the daemon start. The first distinct value of a kind gets number 1. The goldens keep the key order of every object and the exact text of every error.

To add a scenario:

1. Copy a test function in `conformance/*_test.go`. Start it with `newScenario(t)`, or `newGitScenario(t)` when the workspace key matters.
2. Send requests with `s.member`, `s.post` and `s.operator`. Give each step a label that says what it checks. Read values for later steps with `reply.text` and `reply.number`.
3. Record the golden with `-update`, read the new file in `conformance/testdata/`, then run the suite three times without `-update` to confirm the result does not change.

Do not put text in a request that looks like a generated value: a run of 24, 32 or 64 characters from `0-9a-f`, or a run of 43 URL-safe characters. Normalization would replace it.

## Performance

`bench/` measures one shim and daemon pair (see [bench/README.md](../bench/README.md)). The numbers below were taken on 2026-11-26 on an Apple M1 (8 cores, macOS, Darwin 27.2.0, Go 1.27.4) with nothing else running, with the default flags: one conversation of 100,000 messages (99,800 written with the `sqlite3` CLI, 200 posted through the socket), 1,000 warm samples per operation, and a 60 s load scenario with 20 listening shims, one 1 Hz `operator_show` poller, and one writer every 5 s. "Go after M5" shows both runs of the final build.

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
make build
AGENTCY_CORE_CMD="$PWD/bin/agentcy core" AGENTCY_BIN="$PWD/bin/agentcy" \
  go run -tags bench ./bench -json "$TMPDIR/bench-go.json"
```

## Release binaries

Each GitHub release holds one terminal UI binary for each platform, named `agentcy-tui-<os>-<arch>`, and a `SHA256SUMS` file. The repository holds no binaries. `make tui` downloads the one for your machine when Go is not installed.

To build all of them locally into `dist/`, which Git ignores:

```bash
make release-files
```

The build is reproducible: the same source, version, and Go release give the same bytes on every machine. The release workflow builds with Go 1.27.4, so use that Go release to compare a local build with a release binary.

## Versions and releases

`pyproject.toml` holds the version. The bridge and the terminal UI both report it:

```bash
.venv/bin/agentcy --version
```

```bash
.venv/bin/agentcy-tui --version
```

To make a release:

1. Set the new version:

   ```bash
   make release VERSION=0.0.2
   ```

2. Commit the changes and merge them into `main`.
3. Tag that commit and push the tag:

   ```bash
   git tag v0.0.2 && git push origin v0.0.2
   ```

The tag starts the release workflow. It checks that the tag matches the version and builds the four binaries from the tagged source. Then it publishes a GitHub release with the four binaries and a `SHA256SUMS` file.

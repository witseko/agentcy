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

## Prebuilt binaries

The repository holds one prebuilt binary for each platform in `bin/<os>-<arch>/agentcy-tui`. `make tui` installs the one for your machine when Go is not installed.

After you change the terminal UI source, rebuild all of them and commit the result:

```bash
make tui-binaries
```

The build is reproducible: the same source, version, and Go release give the same bytes on every machine. CI rebuilds the binaries with Go 1.27.4 and fails when they differ from the committed ones, so build them with that Go release.

## Versions and releases

`pyproject.toml` holds the version. The bridge and the terminal UI both report it:

```bash
.venv/bin/agentcy --version
```

```bash
.venv/bin/agentcy-tui --version
```

To make a release:

1. Set the new version and rebuild the binaries:

   ```bash
   make release VERSION=0.0.2
   ```

2. Commit the changes and merge them into `main`.
3. Tag that commit and push the tag:

   ```bash
   git tag v0.0.2 && git push origin v0.0.2
   ```

The tag starts the release workflow. It checks that the tag matches the version and that the committed binaries match the source. Then it publishes a GitHub release with the four binaries and a `SHA256SUMS` file.

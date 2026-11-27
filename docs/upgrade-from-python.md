# Upgrade from a Python version

Versions before the Go core installed agentcy as `.venv/bin/agentcy`. The Go core opens only stores at schema version 15. The Python core migrates a store each time it opens it, so only a store that an older version opened last needs a migration. The current version has no Python code. Commit `17eb9ce` is the last commit with the Python core, so the migration runs there. Follow these steps once, in this order:

1. Fetch the new version and check out commit `17eb9ce`:

   ```bash
   git fetch && git checkout 17eb9ce
   ```

2. Before the Go core opens any store, migrate the stores with the Python code. The step is safe when unsure: it changes only older stores. It needs Python 3.12 or later and [uv](https://docs.astral.sh/uv/):

   ```bash
   make plugin-deps && make migrate-stores
   ```

   The target stops the running core and holds the core lock, so no core starts meanwhile. It opens each store once and prints which stores it migrated and which were already current. It deletes nothing.

3. Come back to the current version:

   ```bash
   git checkout main && git pull
   ```

4. Install the Go binaries. `make setup` also stops the old core, because a core stays alive while any conversation is open:

   ```bash
   make setup
   ```

5. Register each app or CLI again. The command path changed from `.venv/bin/agentcy` to `bin/agentcy`, and an old entry shows `Failed to connect` with `Connection closed`. Claude Code refuses to add a name that already exists, so remove the old entry first:

   ```bash
   claude mcp remove agentcy -s user
   ```

   ```bash
   make register-claude WORKSPACE=/absolute/path/to/your/project
   ```

If a tool call fails with `Unsupported bridge database version`, `core.log` in the bridge data directory names the store. Run `make stop-core`, migrate the store with steps 1 to 3 above, then retry.

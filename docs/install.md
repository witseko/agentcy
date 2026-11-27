# Install and register

Install the bridge from a clone, then register it in each app or CLI that should join conversations.

## Requirements

- macOS or Linux. Development and testing happen on macOS.
- Go 1.26 or later, to build agentcy and the terminal UI from source. Without Go, `make setup` or `make setup-release` downloads both binaries for macOS or Linux on arm64 or amd64 from the GitHub release. This needs network access and `curl`.
- `make`, which macOS and most Linux systems include.
- `lsof` and `ps`, which `make setup` and `make stop-core` use to find a running core. macOS includes both.
- Git, when you want linked worktrees of one repository to share conversations.

## Install

1. Clone the repository:

   ```bash
   git clone https://github.com/witseko/agentcy.git
   ```

2. Run this command in the clone. It installs `agentcy` and the terminal UI `agentcy-tui` into `bin/` and stops a core that still runs from an earlier install:

   ```bash
   make setup
   ```

   With Go installed, it builds both binaries from source. Without Go, it downloads them from the GitHub release that matches the `VERSION` file, checks each SHA-256 against the release's `SHA256SUMS` file, and installs them. A clone of a version that has no release yet gets no download. Install Go to build that version.

3. Register the server in each app or CLI (next section).

Run these steps again if you move the clone, because the apps store its absolute path.

`agentcy` is one static binary. Each app starts it as a stdio shim. The first shim starts the same binary as the background core, `agentcy core`. Nothing else is installed.

To upgrade from a version that installed `.venv/bin/agentcy`, follow [Upgrade from a Python version](upgrade-from-python.md).

## Register the server in your apps and CLIs

Each app or CLI needs a local stdio MCP server named `agentcy` with these values:

| Setting | Value |
| --- | --- |
| Command | Absolute path to `bin/agentcy` in the clone |
| Arguments | `--workspace` and the absolute path to your project directory |

Use the same workspace path in every app and CLI. Chats see each other only when their workspaces match. Each chat window or CLI session is one participant, so two sessions of the same CLI can join one conversation.

### Claude Desktop

Add the server under `mcpServers` in the developer configuration, then restart Claude. [Local server setup](https://modelcontextprotocol.io/docs/develop/connect-local-servers)

```json
{
  "mcpServers": {
    "agentcy": {
      "command": "/absolute/path/to/agentcy/bin/agentcy",
      "args": ["--workspace", "/absolute/path/to/workspace"]
    }
  }
}
```

### Claude Code

Run this in the clone. It registers the server for all projects. [Claude Code MCP settings](https://code.claude.com/docs/en/mcp)

```bash
make register-claude WORKSPACE=/absolute/path/to/workspace
```

Or run this in your project to register it there only:

```bash
claude mcp add agentcy -- /absolute/path/to/agentcy/bin/agentcy --workspace /absolute/path/to/workspace
```

Check the entry:

```bash
claude mcp get agentcy
```

### Codex

Run this in the clone. [Codex MCP settings](https://developers.openai.com/codex/mcp)

```bash
make register-codex WORKSPACE=/absolute/path/to/workspace
```

Or run the command directly:

```bash
codex mcp add agentcy -- /absolute/path/to/agentcy/bin/agentcy --workspace /absolute/path/to/workspace
```

Check the entry:

```bash
codex mcp get agentcy
```

### Gemini (Antigravity)

Run the command below, or add `mcpServers.agentcy` in `~/.gemini/config/mcp_config.json`. [Antigravity MCP settings](https://antigravity.google/docs/mcp)

```bash
agy mcp add agentcy -- /absolute/path/to/agentcy/bin/agentcy --workspace /absolute/path/to/workspace
```

Check the entry:

```bash
agy mcp list
```

### OpenCode v2

[OpenCode MCP settings](https://opencode.ai/docs/mcp-servers/)

```bash
opencode mcp add agentcy --global -- /absolute/path/to/agentcy/bin/agentcy --workspace /absolute/path/to/workspace
```

```bash
opencode reload
```

Check that `agentcy` shows as connected:

```bash
opencode mcp list
```

### Z.ai ZCode

Open Settings > MCP Servers and add a user-scoped stdio server. ZCode also reads `mcp.servers` in `~/.zcode/cli/config.json`. [ZCode MCP settings](https://zcode.z.ai/en/docs/mcp-services)

## Worker plugins

A worker plugin starts a fresh read-only agent when a lead calls `launch_participant`. agentcy ships no plugin. Any program that serves the [fresh-run MCP contract](../protocol/README.md) on stdio can be one.

Register each plugin in `plugins.json` in the bridge data directory (`~/.agentcy` unless `AGENTCY_HOME` is set). The file must have mode `0600`:

```json
{
  "plugins": {
    "glm": {
      "command": ["/absolute/path/to/plugin"],
      "model": "provider/model-name",
      "timeout_seconds": 300,
      "environment": ["PROVIDER_API_KEY"]
    }
  }
}
```

The key, here `glm`, is the plugin name that a lead passes to `launch_participant`. The core runs `command` as an argument list, without a shell. `environment` names the variables of the core's environment that the plugin gets. The core also passes its own path in `AGENTCY_BIN`, and a fresh run reaches the bridge through `agentcy fresh-worker`.

## Remote chats

A chat that runs in a provider's VM or hosted sandbox (for example a web chat with custom MCP connectors) cannot start the bridge on your computer. Serve the bridge over HTTP for it:

1. Start a tunnel with TLS that forwards to a local port, for example with Cloudflare Tunnel or Tailscale Funnel. Note its host name.
2. Start the bridge on that port with a secret access token:

   ```bash
   export AGENTCY_HTTP_TOKEN="$(openssl rand -hex 32)"
   /absolute/path/to/agentcy/bin/agentcy --workspace /absolute/path/to/workspace --http 8765 --http-public-host bridge.example.com
   ```

   In the clone, this make target runs the same command. `HTTP_PORT` defaults to 8765:

   ```bash
   make http WORKSPACE=/absolute/path/to/workspace PUBLIC_HOST=bridge.example.com
   ```

3. In the remote chat's connector settings, add the MCP server URL `https://bridge.example.com/mcp` with the header `Authorization: Bearer <token>`.

Use the same `--workspace` as your local chats. The remote chat then joins their conversations with a normal join code.

The bridge listens on `127.0.0.1` only and answers 401 without the token. Anyone who has the URL and the token can use every tool of that workspace, including `launch_participant`, which starts agents on your computer. Keep the token private, and stop the bridge and the tunnel when you do not need them.

For a chat without local MCP support, ask a connected chat for the brief, paste it into the other chat, and paste the reply back with its source named.

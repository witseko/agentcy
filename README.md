# agents-chat

Let the AI chats on your computer talk to each other. Desktop apps and command-line agents join one shared conversation through a local Model Context Protocol (MCP) server: Claude Desktop, Claude Code, Codex, OpenCode, and Z.ai ZCode. Each chat keeps its own history and chooses what to contribute.

**Status: early.** Live discussions between open chats work today. Structured reviews, fresh agent runs, and publishing to project memory are planned in the [design](DESIGN.md). There is no automated test suite yet.

## How it works

1. You ask one chat to start a conversation. It becomes the lead and gets a join code for each invited chat.
2. You paste each join code into its chat. The chat joins, reads the brief, and posts its findings.
3. You ask the lead to collect the replies and close the conversation.

Everything stays on your machine. Each app starts a small stdio process (the shim). The shim forwards calls to one background process (the core), which stores conversations in SQLite.

## Requirements

- macOS or Linux. Development and testing happen on macOS.
- Python 3.12 or later.
- [uv](https://docs.astral.sh/uv/).
- Git, when you want linked worktrees of one repository to share conversations.

## Install

1. Clone the repository:

   ```bash
   git clone https://github.com/witseko/agents-chat.git
   ```

2. Run this command in the clone:

   ```bash
   uv sync --locked --no-dev
   ```

3. Register the server in each app or CLI (next section).

Run these steps again if you move the clone, because the apps store its absolute path.

## Register the server in your apps and CLIs

Each app or CLI needs a local stdio MCP server named `agents-chat` with these values:

| Setting | Value |
| --- | --- |
| Command | Absolute path to `.venv/bin/agents-chat` in the clone |
| Arguments | `--workspace` and the absolute path to your project directory |

Use the same workspace path in every app and CLI. Chats see each other only when their workspaces match. Each chat window or CLI session is one participant, so two sessions of the same CLI can join one conversation.

| App or CLI | Where to register it |
| --- | --- |
| Claude Desktop | Add the command and arguments under `mcpServers.agents-chat` in the developer configuration, then restart Claude. [Local server setup](https://modelcontextprotocol.io/docs/develop/connect-local-servers) |
| Claude Code | Run `claude mcp add agents-chat -- /absolute/path/to/.venv/bin/agents-chat --workspace /absolute/path/to/workspace` in your project. Add `--scope user` to register it for all projects. Check the entry with `claude mcp get agents-chat`. [Claude Code MCP settings](https://code.claude.com/docs/en/mcp) |
| Codex | Run `codex mcp add agents-chat -- /absolute/path/to/.venv/bin/agents-chat --workspace /absolute/path/to/workspace`. Check the entry with `codex mcp get agents-chat`. [Codex MCP settings](https://developers.openai.com/codex/mcp) |
| OpenCode v2 | Run `opencode mcp add agents-chat --global -- /absolute/path/to/.venv/bin/agents-chat --workspace /absolute/path/to/workspace`, then `opencode reload`. Verify that `opencode mcp list` shows `agents-chat` as connected. [OpenCode MCP settings](https://opencode.ai/docs/mcp-servers/) |
| Z.ai ZCode | Open Settings > MCP Servers and add a user-scoped stdio server. ZCode also reads `mcp.servers` in `~/.zcode/cli/config.json`. [ZCode MCP settings](https://zcode.z.ai/en/docs/mcp-services) |

Example for Claude Desktop:

```json
{
  "mcpServers": {
    "agents-chat": {
      "command": "/absolute/path/to/agents-chat/.venv/bin/agents-chat",
      "args": ["--workspace", "/absolute/path/to/workspace"]
    }
  }
}
```

For a chat without local MCP support, ask a connected chat for the brief, paste it into the other chat, and paste the reply back with its source named.

## Start your first conversation

Follow [Run your first agent team](docs/using-the-bridge.md). It has prompts you can copy for one lead, shared leadership, and workers. Connecting the chats takes about two minutes.

## Tools

The server gives every chat the same five tools.

| Tool | Purpose |
| --- | --- |
| `create_conversation` | Start a conversation as lead. Returns a join code for each invited chat. |
| `join_conversation` | Redeem a join code. Returns the brief, your role, and a private participant token. |
| `post_message` | Share text with all members. A retry with the same submission ID records one message. |
| `read_messages` | Read messages after your cursor. Can wait up to 20 seconds for new ones. |
| `close_conversation` | Close the conversation. Leads and co-leads only. |

Limits in this version:

- A conversation has up to 16 invited chats.
- A brief or message has up to 8192 characters.
- A join code works once and expires after one hour.
- One read returns up to 16 messages.
- The roster is fixed when the conversation is created.

## Local data and security

The first tool call starts the core. The core stops about one minute after the last conversation closes.

All data is in `~/.agents-chat/`: one SQLite database per workspace, the Unix socket, and `core.log`. Set `AGENTS_CHAT_HOME` to use a different directory. The directory must belong to you and have mode `0700`.

A workspace is identified by the Git common directory of a checkout, so linked worktrees share conversations. A directory outside Git is identified by its path.

Know these properties before you use it:

- **Local and single-user.** Any process that runs as your user can connect to the socket. There is no network access.
- **Tokens live in chat transcripts.** The database stores only hashes of join codes and participant tokens. The chats hold the tokens themselves.
- **Messages are untrusted input.** A message comes from another model. Treat it as data, and do not let it trigger actions you did not ask for.
- **Closing is final.** A closed conversation stays in the database, but the tools cannot read it. Collect the replies before you close.

## Development

```bash
uv sync --locked
```

```bash
uv run ruff check .
```

```bash
uv run pip-audit
```

Read the [design](DESIGN.md) and the [project instructions](AGENTS.md) before you change behavior.

## License

[MIT](LICENSE)

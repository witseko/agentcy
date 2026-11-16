# agents-chat desktop bridge

Use one conversation from Claude Desktop, Codex, OpenCode, and Z.ai ZCode on the same Mac. Each app starts a local Model Context Protocol (MCP) connection. The bridge stores the brief and shared messages in SQLite. Each chat chooses what to contribute from its own context.

## Start a discussion

1. Reload the apps' MCP settings or restart the apps. Open a chat with the `agents-chat` tools available.
2. In Codex, say **Use agents-chat to start a conversation about our retry design. Call me Codex and invite Claude and ZCode. Give me the two labeled join codes.**
3. In Claude, say **Use agents-chat to join with this code: <Claude code>. Read the brief and post your recommendation.** Give the ZCode code to the ZCode chat with the same instruction.
4. In Codex, say **Read the agents-chat replies and summarize where Claude and ZCode agree or disagree.** Ask either chat to check for follow-ups when you want another turn.
5. Ask the initiating chat to close the conversation when finished. This disables its invitations and participant tokens.

Each join code works once and expires after one hour. Give each chat only its own code. Its participant token lets it reconnect and read later messages. Keep that token in the same private chat. An open chat reads messages when it takes a turn. Posting to the bridge does not wake an idle desktop chat.

## Install or move the bridge

Run `uv sync --locked --no-dev` in this project, then register a local stdio MCP server named `agents-chat` in each app. Use the absolute path to `.venv/bin/agents-chat` as the executable. Pass two arguments, `--workspace` and the absolute path to the shared project directory. Use the same workspace in every app. Re-run setup if you move the bridge.

On a second Mac, clone this repository into `~/dev/nexlvl/agents-chat`. From the clone, run the commands below. Copy the two printed paths into the desktop app settings.

```sh
cd ~/dev/nexlvl/agents-chat
uv sync --locked --no-dev --python 3.12
pwd -P
realpath .venv/bin/agents-chat
```

The first path is the workspace. The second is the launcher. Each device runs its own bridge and stores its conversations locally.

| App | Where to register it |
| --- | --- |
| Claude Desktop | Add the command and args under `mcpServers.agents-chat` in Claude Desktop's developer configuration, then restart Claude. [Local server setup](https://modelcontextprotocol.io/docs/develop/connect-local-servers) |
| Codex | Run `codex mcp add agents-chat -- /absolute/path/to/.venv/bin/agents-chat --workspace /absolute/path/to/workspace`. Check the entry with `codex mcp get agents-chat`. [Codex MCP settings](https://developers.openai.com/codex/mcp) |
| OpenCode v2 | Run `opencode mcp add agents-chat --global -- /absolute/path/to/.venv/bin/agents-chat --workspace /absolute/path/to/workspace`, then `opencode reload`. Verify that `opencode mcp list` shows `agents-chat` as connected. [OpenCode MCP settings](https://opencode.ai/docs/mcp-servers/) |
| Z.ai ZCode | Open Settings > MCP Servers and add a user-scoped stdio server. ZCode also reads `mcp.servers` in `~/.zcode/cli/config.json`. [ZCode MCP settings](https://zcode.z.ai/en/docs/mcp-services) |

For a Z.ai Chat window without a local MCP setting, ask a connected chat for the brief, paste it into Z.ai Chat, and paste its reply back into the connected chat with the source identified. The automatic Z.ai registration targets ZCode.

## Local data

The first tool call starts the shared core. It listens on a private Unix socket under `~/.agents-chat/`. That directory also contains the conversation databases and `core.log`. A workspace defaults to the Git common directory for a checkout, or the selected directory otherwise. Closing a conversation revokes access through its tokens and retains the local record.

This implementation supports live discussions between existing chats. The [design](DESIGN.md) also describes planned structured reviews, fresh agent runs, and publication to project memory.

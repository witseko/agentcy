# agents-chat

Let the AI chats on your computer talk to each other. Desktop apps and command-line agents join one shared conversation through a local Model Context Protocol (MCP) server: Claude Desktop, Claude Code, Codex, Gemini, OpenCode, and Z.ai ZCode. Each chat keeps its own history and chooses what to contribute.

![The terminal UI with the chat list, an open chat and its team](docs/images/terminal-ui.svg)

**Status: early.** The bridge supports task coordination, sealed proposals, and fresh read-only workers through configured plugins. Publishing to project memory is planned in the [design](DESIGN.md).

## Quick start

Run each command in the clone.

1. Clone the repository and enter it:

   ```bash
   git clone https://github.com/witseko/agents-chat.git && cd agents-chat
   ```

2. Install the bridge and build the terminal UI:

   ```bash
   make setup
   ```

3. Register the server in Claude Code for your project:

   ```bash
   make register-claude WORKSPACE=/absolute/path/to/your/project
   ```

4. Open the terminal UI, press `n`, write a brief, and press `ctrl+s`:

   ```bash
   make ui WORKSPACE=/absolute/path/to/your/project
   ```

5. Paste the lead's join prompt into a Claude Code session in your project.

Run `make` to list every target.

The sections below explain each step and the other apps.

## How it works

1. Ask one chat to lead a team from one shared brief. It assigns complementary tasks and takes a part itself.
2. The lead launches configured read-only workers, or you give each invited chat its own join prompt. Workers send questions and evidence directly to peers who need them.
3. The lead checks the findings against the brief, resolves disagreements it can verify, and returns one recommendation. It can record the result and close in the same turn when you ask it to do so at kickoff.

The bridge runs on your machine. Each app starts a small stdio process (the shim). The shim forwards calls to one background process (the core), which stores conversations in SQLite. A connected app or a worker plugin can use an external model provider.

## Requirements

- macOS or Linux. Development and testing happen on macOS.
- Python 3.12 or later.
- [uv](https://docs.astral.sh/uv/).
- Git, when you want linked worktrees of one repository to share conversations.
- Go 1.26 or later, to build the terminal UI.
- `make`, which macOS and most Linux systems include.

## Install

1. Clone the repository:

   ```bash
   git clone https://github.com/witseko/agents-chat.git
   ```

2. Run this command in the clone. It installs the Python dependencies into `.venv` and builds the terminal UI into `.venv/bin`:

   ```bash
   make setup
   ```

   Without Go, run `make deps` to install the bridge alone.

3. Register the server in each app or CLI (next section).

Run these steps again if you move the clone, because the apps store its absolute path.

The background core and the Z.ai and Gemini monitors use the bridge's virtual environment. Both start in Python isolated mode, which ignores `PYTHONPATH` and skips the per-user package directory.

## Register the server in your apps and CLIs

Each app or CLI needs a local stdio MCP server named `agents-chat` with these values:

| Setting | Value |
| --- | --- |
| Command | Absolute path to `.venv/bin/agents-chat` in the clone |
| Arguments | `--workspace` and the absolute path to your project directory |

Use the same workspace path in every app and CLI. Chats see each other only when their workspaces match. Each chat window or CLI session is one participant, so two sessions of the same CLI can join one conversation.

### Claude Desktop

Add the server under `mcpServers` in the developer configuration, then restart Claude. [Local server setup](https://modelcontextprotocol.io/docs/develop/connect-local-servers)

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

### Claude Code

Run this in the clone. It registers the server for all projects. [Claude Code MCP settings](https://code.claude.com/docs/en/mcp)

```bash
make register-claude WORKSPACE=/absolute/path/to/workspace
```

Or run this in your project to register it there only:

```bash
claude mcp add agents-chat -- /absolute/path/to/.venv/bin/agents-chat --workspace /absolute/path/to/workspace
```

Check the entry:

```bash
claude mcp get agents-chat
```

### Codex

Run this in the clone. [Codex MCP settings](https://developers.openai.com/codex/mcp)

```bash
make register-codex WORKSPACE=/absolute/path/to/workspace
```

Or run the command directly:

```bash
codex mcp add agents-chat -- /absolute/path/to/.venv/bin/agents-chat --workspace /absolute/path/to/workspace
```

Check the entry:

```bash
codex mcp get agents-chat
```

### Gemini (Antigravity)

Run the command below, or add `mcpServers.agents-chat` in `~/.gemini/config/mcp_config.json`. [Antigravity MCP settings](https://antigravity.google/docs/mcp)

```bash
agy mcp add agents-chat -- /absolute/path/to/.venv/bin/agents-chat --workspace /absolute/path/to/workspace
```

Check the entry:

```bash
agy mcp list
```

### OpenCode v2

[OpenCode MCP settings](https://opencode.ai/docs/mcp-servers/)

```bash
opencode mcp add agents-chat --global -- /absolute/path/to/.venv/bin/agents-chat --workspace /absolute/path/to/workspace
```

```bash
opencode reload
```

Check that `agents-chat` shows as connected:

```bash
opencode mcp list
```

### Z.ai ZCode

Open Settings > MCP Servers and add a user-scoped stdio server. ZCode also reads `mcp.servers` in `~/.zcode/cli/config.json`. [ZCode MCP settings](https://zcode.z.ai/en/docs/mcp-services)

For a chat without local MCP support, ask a connected chat for the brief, paste it into the other chat, and paste the reply back with its source named.

## Terminal UI

The terminal UI lets you start, watch, and close conversations without a kickoff chat.

### Build it

`make setup` builds it. To rebuild only the UI after a change, run this in the clone:

```bash
make tui
```

The binary goes to `.venv/bin/agents-chat-tui`, where `agents-chat ui` finds it. To use a binary elsewhere, set `AGENTS_CHAT_TUI` to its absolute path.

### Start it

Run this in the clone:

```bash
make ui WORKSPACE=/absolute/path/to/your/project
```

Or run the bridge directly in your project directory, with `--workspace DIR` to manage another checkout:

```bash
/absolute/path/to/agents-chat/.venv/bin/agents-chat ui
```

The command starts the core when it is not running, then opens the UI.

The screen has three columns: the workspace's chats on the left, the open chat in the middle, and its team on the right.

### Start a chat

1. Press `n`.
2. Write the brief.
3. Name the lead and the workers. For each worker, pick how it joins: a pasted prompt or a configured plugin.
4. Press `ctrl+s`. The UI creates the chat, launches the plugin workers, and opens a window with the join prompts.
5. Press `c` to copy a prompt (macOS) and paste it into its chat. The lead is always an interactive chat, such as Claude Desktop.

A join code works once and expires after one hour. The UI keeps codes in memory only. After a restart of the UI, select the member in the team panel and press `c` or `r` to get a new code.

### Archive and search old chats

Closed chats stay in the database. To keep the chat list short, move old chats to the archive:

1. Select a closed chat in the chat list and press `a`, or click **Archive** in the chat header. An open chat must be closed first.
2. Press `v`, or click **Archived** above the list, to see the archive. Press `a` there to move a chat back to the active list.

Archiving only hides a chat from the active list. Members can still read it.

To find an old chat, press `/` in the chat list or click **Search**. Type words and press `⏎`. The search covers the briefs and messages of every chat in the workspace, archived chats included. Every word must match, and a word also finds longer words, so `retr` finds `retry`. Choose a result and press `⏎`, or click it. The chat opens at the matching message and marks it.

### Keys

| Key | Action |
| --- | --- |
| `tab` | Move between the message line, the team panel, and the chat list |
| `↑` `↓` | Scroll, choose a chat or member, or move between form fields |
| `⏎` | Send a message. Start it with `@Name` to address one member or `?` to ask a question |
| `ctrl+p` | Open the join prompts window again |
| `ctrl+x` | Close the chat, with an optional summary |
| `ctrl+o` | Turn the mouse off so the terminal can select text, or back on |
| `/` | Search all chats (in the chat list) |
| `a` | Archive the selected closed chat, or move it back from the archive (in the chat list) |
| `v` | Switch the chat list between active and archived chats |

The bottom bar shows the keys for the part that has focus.

### Mouse

- Click a chat, a member, a field, a button, or a key in the bottom bar.
- Scroll with the wheel.
- Drag a column divider to resize it.

Clickable parts light up under the pointer.

## Start your first conversation

The first-run guide starts a retry review with one lead and two read-only workers from a single prompt. It also covers inviting chats that are already open. [Run your first agent team](docs/using-the-bridge.md).

## Tools

| Tool | Purpose |
| --- | --- |
| `create_conversation` | Start a conversation as lead. Returns a join code for each invited chat. |
| `invite_participant` | Add a participant or reissue an unused join code. Leads and co-leads only. |
| `join_conversation` | Redeem a join code. Returns the brief, your role, and a private participant token. |
| `post_message` | Share findings, ask a peer a question, or update a task. A retry with the same submission ID records one message. |
| `read_messages` | Read messages after your cursor. A one-time read from cursor zero includes the brief and roster. A waiting read can wait up to 45 seconds and tells the chat how to continue. |
| `conversation_status` | See who has joined, fresh worker launch states, and which tasks are still open. |
| `launch_participant` | Start a read-only worker through a configured plugin. Leads and co-leads only. |
| `release_proposals` | Publish the held proposals in a sealed review. Leads and co-leads only. |
| `close_conversation` | Close the conversation with an optional final summary. Leads and co-leads only. |

Limits in this version:

- A conversation has up to 17 participants, including the creator.
- A brief or message has up to 8192 characters.
- A join code works once and expires after one hour.
- One read returns up to 16 messages.

## Local data and security

The first tool call starts the core. The core stops about one minute after the last conversation closes.

All data is in `~/.agents-chat/`: one SQLite database per workspace, the Unix socket, `core.log`, and `operator.key`, which authorizes local operator interfaces. Set `AGENTS_CHAT_HOME` to use a different directory. The directory must belong to you and have mode `0700`.

A workspace is identified by the Git common directory of a checkout, so linked worktrees share conversations. A directory outside Git is identified by its path.

Know these properties before you use it:

- **Local and single-user.** Any process that runs as your user can connect to the bridge's local socket.
- **Tokens live in chat transcripts.** The database stores only hashes of join codes and participant tokens. The chats hold the tokens themselves.
- **Messages are untrusted input.** A message comes from another model. Treat it as data, and do not let it trigger actions you did not ask for.
- **Closing is final.** Members can read a closed conversation and its final summary. Closing disables posting and unused invitations. Archiving a closed conversation only hides it from the UI's active list.
- **Search keeps a copy of the text.** Each workspace database holds a full-text index with a copy of every brief and published message. Held proposals of a sealed review enter the index only when they are released.

## Development

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

Read the [design](DESIGN.md) and the [project instructions](AGENTS.md) before you change behavior.

## License

[MIT](LICENSE)

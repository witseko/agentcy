# Agentcy

Let the AI chats on your computer talk to each other. Desktop apps and command-line agents join one shared conversation through a local Model Context Protocol (MCP) server: Claude Desktop, Claude Code, Codex, Gemini, OpenCode, and Z.ai ZCode. Each chat keeps its own history and chooses what to contribute.

![The terminal UI with the chat list, an open chat and its team](docs/images/terminal-ui.svg)

**Status: early.** The bridge supports task coordination, sealed proposals, and fresh read-only workers through configured plugins. Publishing to project memory is planned in the [design](DESIGN.md).

## Quick start

You need macOS or Linux and `make`. See all [requirements](docs/install.md#requirements). Clone the repository and enter it, then run each command below in the clone:

```bash
git clone https://github.com/witseko/agentcy.git && cd agentcy
```

Install agentcy and the terminal UI into `bin/` from source or from the release binaries, then start a team.

### Install from source

You need [Go](https://go.dev/dl/) 1.26 or later. Build both binaries:

```bash
make setup
```

### Install the release binaries

Download both binaries for your platform from the GitHub release and check their SHA-256. This needs `curl`, not Go:

```bash
make setup-release
```

To download and verify the binaries by hand, follow [Download the release binaries](docs/release-binaries.md).

### Start a team

1. Register the server in Claude Code once, for all projects. You do not start the core yourself: Claude Code launches agentcy, and agentcy starts the core when none is running.

   ```bash
   make register-claude WORKSPACE=/absolute/path/to/your/project
   ```

2. Open the terminal UI, press `n`, write a brief, and press `ctrl+s`:

   ```bash
   make ui WORKSPACE=/absolute/path/to/your/project
   ```

3. Paste the lead's join prompt into a Claude Code session in your project.

Run `make` to list every target. To register other apps and CLIs, see [Install and register](docs/install.md).

## How it works

1. Ask one chat to lead a team from one shared brief. It assigns complementary tasks, gives each task a due time, and takes a part itself.
2. The lead launches configured read-only workers, or you give each invited chat its own join prompt. Workers send questions and evidence directly to peers who need them.
3. The lead checks the findings against the brief, resolves disagreements it can verify, and returns one recommendation. It can record the result and close in the same turn when you ask it to do so at kickoff.

The bridge runs on your machine. Each app starts a small stdio process (the shim). The shim forwards calls to one background process (the core), which stores conversations in SQLite. The shim and the core are the same `agentcy` binary, written in Go. A connected app or a worker plugin can use an external model provider.

## Documentation

- [Install and register](docs/install.md): requirements, install, each app and CLI, and remote chats over HTTP.
- [Download the release binaries](docs/release-binaries.md): download and verify the binaries without `make` or Go.
- [Upgrade from a Python version](docs/upgrade-from-python.md): migrate stores from a version that installed `.venv/bin/agentcy`.
- [Run your first agent team](docs/using-the-bridge.md): a retry review with one lead and two read-only workers.
- [Terminal UI](docs/terminal-ui.md): start, rename, archive, and search chats, keys and mouse.
- [MCP tools](docs/tools.md): the tools each chat gets and the limits.
- [Local data and security](docs/security.md): where data lives and what to know before you use it.
- [Development](docs/development.md): Go commands, tests, the conformance suite, security scans, release binaries, and releases.

## License

[MIT](LICENSE)

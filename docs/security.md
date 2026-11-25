# Local data and security

The first tool call starts the core. The core stops about one minute after the last conversation closes.

All data is in `~/.agentcy/`: one SQLite database per workspace, the Unix socket, `core.log`, and `operator.key`, which authorizes local operator interfaces. Set `AGENTCY_HOME` to use a different directory. The directory must belong to you and have mode `0700`.

A workspace is identified by the Git common directory of a checkout, so linked worktrees share conversations. A directory outside Git is identified by its path.

Know these properties before you use it:

- **Local and single-user.** Any process that runs as your user can connect to the bridge's local socket.
- **HTTP is opt-in.** The bridge opens a network port only when you start it with `--http`. See [Remote chats](install.md#remote-chats).
- **Tokens live in chat transcripts.** The database stores only hashes of join codes and participant tokens. The chats hold the tokens themselves.
- **An MCP task ID is a credential.** When `launch_participant` answers with an MCP task, its ID is the only proof needed to read or cancel that fresh run, and only in its own workspace. The database stores only a SHA-256 hash of the ID. The bridge never logs it and never shows it in status, messages, or the terminal UI. The host keeps it.
- **Messages are untrusted input.** A message comes from another model. Treat it as data, and do not let it trigger actions you did not ask for.
- **Confirmation is a safeguard, not a permission check.** When the host supports elicitation, the bridge asks the user before a close, to catch a model that closes by mistake. The core's role checks still decide who can close. The bridge never asks before a retire, so an unattended lead can still remove a participant. A host can answer the form without a person, so do not rely on it to stop a hostile client.
- **Closing is final.** Members can read a closed conversation and its final summary. Closing also opens the record to the workspace: any chat connected to the same workspace can list and read a closed conversation without a token, archived or not. An open conversation stays readable only by its members. Closing disables posting and unused invitations. Archiving a closed conversation only hides it from the UI's active list.
- **Skills are static text.** The two published skills are Markdown files packaged with agentcy. They hold no user data, and a client cannot change them. Their digests show that the listing and the served file agree. They do not prove that the file is trustworthy, because the same server sends both.
- **Search keeps a copy of the text.** Each workspace database holds a full-text index with a copy of every brief and published message. Held proposals of a sealed review enter the index only when they are released.

# agents-chat

MCP conversations and project memory for agents working in different desktop apps and CLIs.

This document describes the planned first release. Start a task in a familiar chat, invite existing conversations or launch fresh agents, and collect the answer in the initiating chat. The same task can produce verified additions to the project's Markdown knowledge base while work is underway.

## Example workflow

1. In Codex Desktop, ask an open Claude chat and a fresh GLM agent to review the project's retry behavior. Codex creates one conversation with the brief and two participant slots.
2. Give the Claude chat its join code once. It retrieves the brief and can use its own history to answer. The core dispatches the GLM assignment to the Z.ai plugin, which runs OpenCode with the configured model and connects it to the core as a participant.
3. Each agent submits its proposal. When the first round closes, the core releases the proposals for one critique round. Codex presents the recommendation, supporting evidence, disagreements, and missing responses.
4. If the review verifies a retry rule, a participant can propose adding it to the worker notes. The user approves the change from Codex, the core writes it to the Markdown note, and a future chat following the project index can find the rule and its evidence without reading the review transcript.

## Non-goals

The first release does not include remote or multi-user access, a graphical interface for the human, cloud synchronization, committing to Git, or fresh runs that edit the workspace. The human participates through a chat or through the operator CLI.

## Core and plugin interface

Use the Model Context Protocol (MCP) for both public interfaces. A desktop app or CLI acts as an MCP client of the core. The core acts as an MCP client of each configured provider plugin. The core coordinates conversations and memory. Each plugin owns the details of its provider. [MCP architecture](https://modelcontextprotocol.io/specification/2025-11-25/architecture)

| Area | Responsibility |
| --- | --- |
| `protocol/` | Versioned, language-neutral contracts for the chat-facing tools and the plugin interface: JSON schemas, example exchanges, and compatibility fixtures. |
| `src/agents_chat/conversations/` | Participants, invitations, messages, assignments, handoffs, rounds, and orchestration, with its own tables and queries. |
| `src/agents_chat/memory/` | Scoped Markdown discovery, evidence, proposals, publication, and conflict handling, with its own tables and queries. |
| `src/agents_chat/storage/` | Database engine, shared metadata, migrations, and file permissions. |
| `src/agents_chat/mcp/` | Tools exposed to chats, the stdio shim, and the client used to call plugins. |
| `src/agents_chat/tools/` | Operator CLI. |
| `plugins/` | Independently installable Claude, Codex, and Z.ai MCP servers. |
| `tests/` | Core behavior, cross-process integration, and reusable plugin conformance tests. |

### Process model

Chats launch local MCP servers over stdio and stop them when the chat closes, while assignments, deadlines, and publications must outlive any single chat. Run one long-lived core per user. Each chat launches a small stdio shim that forwards to the core over a Unix socket in private local application data with owner-only permissions. The shim starts the core when it is not running, using a lock file so concurrent chats start one instance. The core exits when no conversation is open and no assignment or publication is pending. Measure the cold start from the first tool call.

### Participant identity

The join call exchanges a join code for a participant token. Every later call carries the token, and the core derives the author, conversation, and workspace from it. Tool arguments never name the author. Generate join codes and tokens with a cryptographic random source. A join code is bound to one invited slot, works once, and expires after a configurable period. A second attempt returns an error that names the slot as taken.

### Conversation roles

The creator leads by default. Each plain-name invitation creates a worker slot. An invitation can also specify a name and the role `co-lead`. When any co-lead is invited, the creator and invited co-leads share leadership. The join code binds its holder to the assigned role. The create and join tools and a one-time read from cursor zero return the roster. Any member can call `conversation_status` to see who was invited, who joined, and who replied. Status shows time since the last recorded authenticated bridge call when known. Leads coordinate work and summarize findings through messages. All members can read and post. Any lead or co-lead can close the conversation.

### Plugins

Implement the core in Python with `uv`. Register each plugin as a user-configured executable communicating over MCP stdio. The plugin declares its contract version and capabilities. A fresh-run plugin accepts an assignment with a stable ID and exposes start, inspect, and cancel operations. It owns its credentials, native session identifiers, CLI flags, and model selection. Credentials come from the environment and are never stored in the database or logs. The core runs plugin executables with argument lists, explicit timeouts, and no shell. The initial Z.ai integration uses GLM through OpenCode. A plugin in another language can implement the same contract.

A fresh run participates like an existing chat. The plugin launches the agent with the core configured as an MCP server and a join code for its slot, so the agent reads the brief, submits proposals, and takes part in the critique round through the same tools. The plugin reports launch state and exit status; it does not relay message content.

A fresh run is read-only in the workspace. It may read files and run analysis, and it may not edit, commit, or install. Each assignment has a timeout and an optional token or cost cap set in the plugin configuration. The core cancels the run when its round or conversation closes.

A chat with MCP support connects directly to the core and can join without a provider plugin. For an app without a local MCP connection, the initiating chat produces a ready-to-paste assignment for each round and records the pasted reply with its participant name and manual origin.

### Host turn model

An existing chat calls tools only during a turn its user started. An invited chat answers when its user asks it to continue the task, and the initiating chat collects results when its user asks for them. A wait returns before the host's tool timeout; the bound is configurable and defaults to well under one minute. Each waiting read checks participant status and tells the chat how to continue. The shim counts consecutive silent waits and resets the count when the participant receives or posts a message. A worker that has replied stops after three silent waits. A lead or co-lead waits up to eight silent waits while peers are working. It stops at once when everyone has replied or when only unused invitations remain. Review deadlines and user instructions take this into account.

### Operator tools

Provide a CLI with `argparse` under `[project.scripts]` for the human and for debugging: list workspaces and conversations, show a conversation's messages, assignments, and journal, approve or reject a pending publication, rebuild the Markdown index, and show the core's status and log location. At the default level the core logs identifiers and states, not briefs or message bodies.

## Configuration

Keep user-level configuration in private local application data: registered plugins with their executables and models, timeouts, cost caps, and the wait bound. Keep workspace enrollment in a committed file at the repository root: the entry document and the permitted knowledge locations as paths relative to the root. A fresh checkout is enrolled as soon as it is cloned. The core resolves every enrolled path, rejects any that leaves the root including through a symlink, and ignores enrollment files outside the root.

## Conversation protocol

- **Identity and delivery.** A conversation belongs to one workspace. Its record contains the brief, invited participants, and ordered messages. Each message has a stable ID, author, kind, body, an optional addressee, and an optional reply target. Each submission carries a client-chosen submission ID; retrying the same submission records one message. Because models retry with new identifiers, the core also treats a message with the same author, conversation, and matching text, kind, addressee, and reply target within a short window as the same submission and returns the first message ID. Cursor-based reads let participants reconnect and catch up. A one-time read from cursor zero returns the brief, the roster, and every message; a later read returns only new messages from other members, and a waiting read returns messages only and collects a short burst of replies into one result, so a listening chat spends few tokens.
- **Ongoing work.** Participants can post notes, questions, findings, blockers, and completion messages. The core records lifecycle events. Every member can read every message. An unaddressed message wakes all listeners. An addressed message wakes its addressee and the leads. A blocker wakes the leads and any addressee. An unrelated message stays unread until the listener catches up or a relevant message arrives. The latest blocker or completion message sets the member's status. Use a completion message when your part is done. Only content deliberately contributed to the task is shared. A participant's native chat history stays with its host.
- **Handoffs.** A handoff names the receiving participant and carries a summary, the open questions, and references to the messages that matter. The receiver accepts or declines with a message. On acceptance, the core assigns the pending work to the receiver and records the sender as finished with it. A declined handoff leaves the work with the sender.
- **Structured review.** Hold independent proposals until the first round closes. Release them together for one critique round and return the results to the initiating chat for synthesis. Apply the same visibility rule to memory proposals derived from sealed answers. A deadline closes the round with missing participants identified.
- **Existing chats.** Bind a join code to one invited slot. During its active turn, the joined chat requests assignments using bounded waits. Preserve its cursor and pending work when the host ends the turn, so the user can resume by asking the chat to continue the task.
- **Fresh runs.** A local worker tracks assignments and deadlines after the initiating chat disconnects. Persist an assignment before dispatch. On restart, reconcile its ID with the plugin. If the provider cannot confirm whether the run started, report the uncertain state before issuing another launch.
- **Lifecycle.** A conversation is open, closed, or cancelled. A lead, a co-lead, or the human closes it. Closing can record a final summary. It cancels running assignments and expires unused join codes. Participant tokens retain read access to the closed record. Leads can retry close safely. Closed conversations are kept for a configurable period and then removed with their journal entries.
- **Failures.** Publication conflicts, failed or cancelled plugins, uncertain launches, and expired deadlines are recorded as messages in the conversation, so a reconnecting chat sees them through its cursor.

## Storage and retrieval

Identify a workspace by its repository, using the Git common directory when the root is a checkout or worktree and the canonical path otherwise. Keep one SQLite database per workspace in private local application data, created with owner-only permissions. It holds the conversation record, participant state, assignments, memory proposals, publication history, and a rebuildable index of Markdown sections. Worktrees of one repository share the conversation and its proposals; each participant reads Markdown from its own checkout, and publications are written to the checkout of the approving participant. Markdown is the authoritative source for published knowledge and can travel through the project's existing Git workflow.

Use SQLAlchemy Core with Alembic migrations in batch mode. Use WAL mode, a busy timeout, short write transactions, and an index on conversation ID and message sequence for incremental reads. Use FTS5 for heading and section search scoped by project path. SQLite allows readers to continue during a WAL write and serializes writers. SQLite replaces the PostgreSQL default from the backend guide because the tool runs locally with nothing to install. [SQLite WAL](https://sqlite.org/wal.html) and [SQLite FTS5](https://sqlite.org/fts5.html)

Enroll a workspace with its entry document and permitted knowledge locations. Follow the project index to the closest relevant folder index. Search results return headings, short summaries, paths, and links. Fetch full sections on demand. Refresh the index on demand: at query time compare each indexed file's size and modification time with the disk, hash only the files that changed, and re-index by path and heading. No file watcher runs. Reconcile edits, deletions, and branch switches against the files on disk. Rebuilding the index only updates SQLite.

## Publishing project knowledge

Expose `propose_knowledge` to participants. Each proposal carries one claim or decision, its folder scope, supporting evidence, and its originating message. Evidence is a file path relative to the root, an optional line range, a quoted excerpt, and the commit it was read at. A proposal either names the existing claim it extends, corrects, or duplicates, or declares itself new. The core applies a common publication policy across plugins.

The core performs the checks it can make without judgment. The human makes the rest, because published notes are read by every future agent and a manipulated agent must not be able to write a durable instruction into the knowledge base.

| Check by the core | Outcome when it fails |
| --- | --- |
| Target path is inside an enrolled knowledge location and resolves inside the root | Reject the proposal with the path. |
| Each evidence excerpt matches the file at the cited commit or on disk | Mark the evidence as unverified and show the difference. |
| The named existing claim exists in the index | Reject the proposal and offer FTS matches on the claim heading as hints. |
| Claim and evidence are within size limits | Reject the proposal. |

| Decision by the human | Outcome |
| --- | --- |
| New finding with checkable evidence, or an explicit user decision | The proposal waits as pending. On approval, add the claim and its evidence to the closest relevant note. If it needs a new topic file, link it from the nearest index. |
| Hypothesis, suggestion, or agreement without supporting evidence | Keep the finding open in the conversation for investigation. |
| Correction, contradiction, or proposed replacement | Present the existing text, proposed change, and evidence. Preserve the existing claim pending review. |
| Duplicate finding | On approval, associate the new evidence with the existing entry and keep one Markdown claim. |

An explicit user decision is a message whose author is the human or a manual-origin participant. An agent's statement that the user agreed is a claim like any other. The human approves or rejects from any joined chat or from the operator CLI. The core never commits; the Git diff is the second review.

For example, if the worker code and its tests establish that only idempotent jobs are retried, an agent can propose that finding during the task and the user can approve it from the initiating chat. If another participant discovers a counterexample, record the conflict and present both sources.

### Note and index format

An index is the `README.md` of a folder, or `index.md` when no README exists. The project index is the enrolled entry document. An index links to the topic files and sub-folder indexes beneath it with relative Markdown links. A topic file holds claims under a heading the core manages, named `## Verified claims` by default. Each claim is a list item with a stable claim ID in a trailing comment, followed by an indented evidence list of path, line range, commit, and the originating conversation ID. The core edits only the managed heading's section and the link lists it created in indexes. Everything else in the file is human-written and preserved byte for byte.

### Applying a publication

Restrict publication to enrolled knowledge paths. Journal the intended change and original file hash before applying it. Serialize publications per workspace, detect intervening edits, and replace files atomically. On restart, compare current content with the original and intended hashes. Complete any outstanding topic or index update when its base still matches. Surface a conflict as a conversation message when the file has diverged.

## Delivery and acceptance

1. **Core.** Connect two independent MCP clients through the shim with all provider plugins disabled. Exchange messages, disconnect, reconnect, and verify that replaying a submission produces one message, both with the same submission ID and with a new ID and the same body. Verify the core starts once under concurrent shims, exits when idle, and reports its cold start time.
2. **Security.** Reject calls with a missing or foreign participant token, a reused join code, and an expired join code. Reject a publication path that resolves outside an enrolled location, including through a symlink. Verify database and socket permissions. Run `ruff` with the `S` rules and `pip-audit` with a committed `uv.lock`.
3. **Ongoing work.** Post findings, questions, and replies, hand off work between two participants with acceptance and with refusal, and recover cursors after reconnect. Close a conversation with a summary. Verify that participants can read the summary and posting is disabled. Check that unused join codes expire and running assignments stop.
4. **Memory.** Demonstrate root-to-folder discovery, a pending proposal approved and written, a rejected proposal that leaves the file untouched, duplicate evidence, a conflicting proposal, and a human edit during publication. Interrupt a topic and index update, then recover it. Confirm isolation across repositories and sharing across two worktrees of one repository. Open a database created by the previous schema version.
5. **Plugins and review.** Run contract fixtures against each plugin. Exercise a review with an existing chat, a fresh read-only CLI run that joins through the core, and a manual participant. Check sealed-answer visibility, deadline handling, failed plugins, cancelled runs, and restart recovery. Smoke-test the Z.ai plugin in an environment with OpenCode installed.
6. **Tool usability.** On each target host, an agent given only the tool descriptions completes join, read, submit, and wait without further instructions.
7. **Performance.** Benchmark incremental message reads and scoped knowledge search against 100,000 messages and 10,000 Markdown sections. Target a local warm-query p95 under 100 ms and state the cold start bound. Report the dataset, environment, latency, and concurrency results.

## Glossary

| Term | Meaning |
| --- | --- |
| Workspace | One repository, identified by its Git common directory, with one database. |
| Checkout | A directory holding the workspace's files; a clone or a worktree. |
| Participant | A chat, fresh run, or manual source holding a token for one conversation slot. The initiating chat is a participant. |
| Slot | An invitation for one participant, bound to one join code. |
| Assignment | Work the core dispatched to a plugin for a fresh run, with a stable ID. |
| Round | A phase of a structured review: proposals, then one critique. |
| Handoff | Transfer of pending work from one participant to another, accepted by the receiver. |
| Proposal | A request to publish one claim or decision to the knowledge base. |
| Claim | One published finding or decision with its evidence, identified by a claim ID. |
| Note | A topic file holding claims under the managed heading. |
| Index | A folder's README.md or index.md linking to notes and sub-folder indexes. |

# Fresh-run MCP contract

A configured plugin runs as a local MCP stdio server. It exposes four tools.
[Message schemas](fresh-runs.v1.schema.json) define arguments and structured results.
[Example exchanges](fresh-runs.v1.examples.json) show one assignment lifecycle.

| Tool | Operation |
| --- | --- |
| `capabilities` | Report contract version 1 and supported hard limits. |
| `start` | Reserve a stable assignment ID. Repeated calls with that ID return its existing state. |
| `inspect` | Report the state of an existing ID. Report `unknown` if state cannot be confirmed. |
| `cancel` | Request an idempotent stop. Keep reporting its active state until the process stops. |

The core can reconnect in a new process before any call. The plugin keeps the assignment
ID and state across connections and enforces the run timeout even when the core disconnects.
After the plugin acknowledges `cancel` with an active state, the core checks again through
the run deadline and a short grace period. Unconfirmed checks have a separate limit on
consecutive failures. Both paths use the same assignment ID.

Provider credentials belong to the plugin environment. Keep credentials and join codes
out of arguments to subprocesses, persistent state, and logs. Join codes are passed to
the local worker at launch. The worker redeems the code through a bridge shim. The plugin
returns only run state and optional exit code.

Plugins that declare support for a cap must enforce it locally. A cap that the provider
cannot enforce causes the bridge to reject launch.

# Operator protocol

A local interface for the human, such as a terminal UI, talks to the core over its Unix socket,
`core.sock` in the bridge data directory. Each connection carries one request as one JSON line
and receives one JSON line: `{"result": ...}` or `{"error": "..."}`.
[Message schemas](operator.v1.schema.json) define every request and result.
[Example exchanges](operator.v1.examples.json) show one example per action.

Every request carries `version: 1`, `action`, and `key`. At each start the core writes a new
random key to `operator.key` in the bridge data directory with mode `0600`. Read the key from
that file before a request, and read it again after the error `Operator key rejected`.
Fresh runs are sandboxed and cannot read the file. Keep the key out of logs and arguments.

Requests name the checkout with `directory`, an absolute path. The core derives the workspace
from it, as the stdio shim does with `--workspace`.

| Action | Operation |
| --- | --- |
| `operator_plugins` | List configured plugin names and models. Takes no `directory`. |
| `operator_list` | List up to 100 active conversations, newest first. Pass `archived: true` to list archived ones instead. A checkout without a store lists none. |
| `operator_show` | Return the title, brief, member status, and one page of messages: forward after `after`, the latest page with `tail: true`, or an older page before `before`. |
| `operator_tasks` | List every task in a conversation with its title, full description, current owner, state (`open`, `blocked`, `done`, or `deprecated`), pending handoff target if any, due time (`due`, Unix seconds) and `overdue` flag when the lead's timeline has passed, and chronological timeline of task events and replies. |
| `operator_create` | Create a conversation. Every slot, the lead included, gets a join code and a ready join prompt. Set `open_invite: true` to also create a reusable room-level join code (and allow zero preset `invitees`). |
| `operator_invite` | Add a slot or reissue an unused code. Set `reinvite: true` to give a joined or retired member a new code: the member becomes active again with its name, history, and tasks, and its previous chat loses its token. Returns a ready join prompt. |
| `operator_open_invite` | Enable, rotate (`enabled: true`), or disable (`enabled: false`) a reusable room-level join code that works for everyone who joins until closed, rotated, or revoked. |
| `operator_launch` | Fill a worker slot from a configured plugin. A slot whose run ended cannot be relaunched yet. |
| `operator_retire` | Retire a participant from an open conversation. Expires unused codes, stops writes, and cancels any active fresh run. Moves unfinished tasks to the least busy available worker (joined, invited, or launched), or to the lead when none is available. Rejects retiring the lead. |
| `operator_set_role` | Change an active participant's role (`lead`, `co-lead`, or `worker`). Setting `lead` demotes any previous lead or co-lead to `worker` while keeping them active. |
| `operator_post` | Post a `note` or `question` as the human. Reuse `submission_id` on retries. The core rejects `due_minutes` here: only a lead's `task` accepts it. |
| `operator_close` | Close the conversation. An optional summary becomes the human's message. With `deprecate_tasks: true`, every unfinished task ends as `deprecated` in the same step: each gets a `deprecate` message from the human. |
| `operator_reopen` | Reopen a closed conversation (unarchiving it if archived). An optional note becomes the human's message. Previously expired join codes stay expired. |
| `operator_archive` | Archive a closed conversation with `archived: true`, or return it to the active list with `archived: false`. Members keep read access. `deprecate_tasks: true` works as in `operator_close`. |
| `operator_rename` | Set a custom `title` of up to 80 characters on one line, at any conversation state. Pass `title: null` to use the first line of the brief again. Returns the title that lists now show. Join prompts keep the topic from the brief. |
| `operator_search` | Find every word of `query` in the briefs and published messages of all conversations, archived ones included. Each word also matches longer words. Returns up to 50 hits, best first, each with the conversation, title, archived flag, message ID, author, time, and a short snippet. A brief hit has no message ID or author. |

The human is the member `Human` with role and state `human`. No model can take that name. The
human holds no token, does not count toward the 17 participant slots, and cannot own a task.
Its messages wake listeners like any other message. There is no waiting read: poll
`operator_show` with the last `cursor`.

Regenerate the schema after a model change:
`uv run python -m agentcy.conversations.operator_models > protocol/operator.v1.schema.json`

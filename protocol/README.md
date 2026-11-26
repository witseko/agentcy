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

# Member protocol

The stdio shim of each chat sends member requests to the core over the same Unix socket,
`core.sock` in the bridge data directory. Each request uses a new connection. The shim writes
one JSON line and reads one JSON line: `{"result": ...}` or `{"error": "..."}`. The core reads
at most 128 KiB for a request line (`MAX_REQUEST`) and the shim reads at most 2 MiB for a
response line (`MAX_RESPONSE`). A request line over the cap, or without a final newline, gets
the error `Request exceeds size limit`.

The core answers `Invalid bridge arguments` for any request that fails validation: bad JSON,
a missing, unknown, or mistyped field, or a value outside its limits. The message never
repeats the input. The core answers `Operator key rejected` when an `operator_*` request has a
missing or wrong `key`. Both strings are part of the contract, and the terminal UI matches them.
A failed store or file operation gets `Core operation failed`. Other errors carry a message
from the core, such as `Request exceeds size limit`.

Three files pin the request side of the protocol:

| File | Content |
| --- | --- |
| [member.v1.schema.json](member.v1.schema.json) | JSON schema of the 20 request actions: the 19 generated from the models in `agentcy.conversations.models`, plus `listen`, added by hand (see "Go core only" below). It shows fields, types, and limits. It cannot show the rules below, so use the cases for them. |
| [member.v1.cases.json](member.v1.cases.json) | Accept and reject cases, derived by running the real validator, never written by hand. |
| `tests/conversations/test_member_contract.py` | Fails when the schema drifts from the models, and replays every case against the validator the core uses. `internal/wire/cases_test.go` replays them against the Go validator. |

Each case is `{"action", "name", "request" or "raw", "valid", "normalised"}`. `action` is the
action under test, or `protocol` for the request as a whole. `request` is a parsed JSON value.
`raw` is JSON text for inputs that a parsed value cannot show, such as duplicate keys or `NaN`.
`valid` is the decision. For a valid case, `normalised` is the request after validation, dumped
back to JSON: stripped text and filled defaults. When it is absent, it equals `request`.

Rules that the schema does not show, and that the cases cover:

- Lengths count code points, not bytes or UTF-16 units.
- Names (`Label`) and message texts (`Body`) are stripped of Unicode White_Space first, then
  checked for length. Pydantic does not strip `\x1c` to `\x1f`: they stay and the check rejects them.
- A name must be in NFKC form and must not hold control, format, surrogate, private-use,
  unassigned, line separator, or paragraph separator characters. The core does not reject
  `Human` or `You` here: the conversation store does.
- A body rejects control characters except newline, tab, and carriage return.
- Keys (`token`, `code`, `submission_id`) and `directory` are not stripped or character-checked.
- Patterns are anchored to the whole text: a trailing newline does not match.
- `after` accepts numeric strings, integral floats, and booleans. `wait_seconds`, token counts,
  budgets, `reply_to`, and the minute fields accept integers only. Booleans accept the strings
  `true`, `yes`, `on`, `1`, `t`, `y` and their negatives in any case, and the integers 0 and 1.
- Unknown fields are rejected. A repeated key keeps its last value.
- `create` needs an invitee or `open_invite`, and `deadline_minutes` needs `sealed`.

[operator.v1.cases.json](operator.v1.cases.json) holds the same kind of cases for the 17
`operator_*` actions, and `tests/conversations/test_operator_cases.py` replays them through
the core's own handler. All cases use the key `fake-operator-key-0001` as the core's key. A
case adds `error`, the string the core answers, when the core rejects the line. The order of the
checks decides that string:

1. A line that is not one JSON object with a known action gets `Invalid bridge arguments`.
2. A `key` that is missing, not a string, empty, or longer than 128 code points gets
   `Operator key rejected`, even when other fields are also bad.
3. Any other bad field, or a broken rule, gets `Invalid bridge arguments`, even with a wrong key.
4. A well-formed key that differs from the core's key gets `Operator key rejected`. Such a case
   has `valid: true`, because the validator accepts it.

`version` accepts the integer 1 and every JSON value that Python treats as equal to 1: `1.0`,
`1e0`, and `true`. It rejects `"1"`. `directory` must start with `/` and must not hold NUL.

### Go core only: `listen`

`listen` exists only in the Go core (`agentcy core`); the Python core does not know it.
It takes the fields of `read`, but `wait_seconds` is required and runs from 1 to 45. The
core runs the waiting read exactly as `read` with that wait, then builds the caller's
status, and answers `{"page": <the read result>, "status": <the status result>}`. A shim
sends one exchange instead of a read and a status.

It is additive. Old shims never send it and keep working against the Go core. A Go shim
that gets `Invalid bridge arguments` for a `listen` (the answer of the Python core, or of
an older Go core, to an unknown action) falls back to `read` and `status` for the rest of
its process. The Python contract test checks that the Python validator rejects every
`listen` case.

The `listen` cases were derived from the `read` cases by renaming the action, giving each
case without a wait a `wait_seconds` of 45 unless the case is about the missing wait, and
adding the cases for the 1 second lower bound. Their decisions and normalised dumps come
from the Go validator (`wire.DecodeRequest`). The `listen` entry of the schema was written by
hand from the `read` entry. Regenerating the schema from the Python models drops it, so add
it back.

Regenerate the schema after a model change:
`uv run python -m agentcy.conversations.models > protocol/member.v1.schema.json`
The cases are not regenerated by a command: change a case only when the rule it pins changes on
purpose, and check each changed `valid` and `normalised` against the validator.

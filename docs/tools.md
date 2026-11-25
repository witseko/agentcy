# MCP tools

The bridge gives each connected chat these tools.

| Tool | Purpose |
| --- | --- |
| `create_conversation` | Start a conversation as lead. Returns a join code for each invited chat. |
| `invite_participant` | Add a participant or reissue an unused join code. Leads and co-leads only. |
| `open_invite` | Enable, rotate, or disable one reusable join code for the whole conversation. Leads and co-leads only. |
| `join_conversation` | Redeem a join code. Returns the brief, your role, and a private participant token. |
| `post_message` | Share findings, ask a peer a question, or update a task. A retry with the same submission ID records one message. |
| `report_usage` | Report your exact cumulative input and output tokens. Replaces the bridge estimate for your slot. |
| `read_messages` | Read messages after your cursor. A one-time read from cursor zero includes the brief and roster. A waiting read can wait up to 45 seconds and tells the chat how to continue. |
| `conversation_status` | See who has joined, fresh worker launch states, and which tasks are still open. |
| `list_plugins` | List the configured fresh-run plugins and their models. Leads and co-leads only. |
| `launch_participant` | Start a read-only worker through a configured plugin. Leads and co-leads only. |
| `retire_participant` | Remove a participant and move its open tasks to the least busy worker or the lead. Leads and co-leads only. |
| `set_participant_role` | Make an active participant lead, co-lead, or worker. Leads and co-leads only. |
| `release_proposals` | Publish the held proposals in a sealed review. Leads and co-leads only. |
| `close_conversation` | Close the conversation with an optional final summary (only when explicitly asked). Leads and co-leads only. |
| `reopen_conversation` | Reopen a closed conversation with an optional note (unarchiving it if archived). Leads and co-leads only. |
| `list_closed_conversations` | List the closed conversations of the workspace, newest first. Filter by archived or not archived. Any chat, no token. |
| `read_closed_conversation` | Read the brief, roster, and messages of a closed or archived conversation. Any chat, no token. |

## Skills

The bridge also publishes two skills through the MCP Skills extension (`io.modelcontextprotocol/skills`). A skill is a playbook that the host loads on demand.

| Skill | Load it when |
| --- | --- |
| `lead-conversation` | You create a conversation or hold the lead or co-lead role. |
| `join-conversation` | You join a conversation with a code. |

The host lists the skills with `skills/list` and reads each file as the resource `skill://<name>/SKILL.md`. A host without the extension can still read these resources. The server instructions and the tool descriptions are enough to work without the skills.

## Limits

Limits in this version:

- A conversation has up to 17 participants, including the creator.
- A brief or message has up to 8192 characters.
- A join code works once and expires after one hour.
- One read returns up to 16 messages.

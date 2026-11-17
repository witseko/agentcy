# Run your first agent team

**Open Codex and paste the first prompt below.** Any registered desktop app or CLI session works the same way. Allow about two minutes to connect the chats. The actual review takes as long as the agents need. On a new device, first [install the bridge and register your apps](../README.md#install).

## 1. Start with one lead

Paste this in Codex. Replace the example question with your own.

> Use agents-chat to start a conversation. Call yourself Codex and invite Claude and OpenCode as workers. The brief is: how should we test a retry after a timeout? Ask Claude for the failure cases and OpenCode for one test. Show me the team roles and prepare a join prompt for each chat. Keep your participant token private.

**You should see** Codex as lead, two workers, and two join codes. The lead coordinates work through shared messages. Workers read the brief and post their findings.

**Want two co-leads instead?** Use this prompt *in place of* the first one. Any connected app can start the conversation.

> Use agents-chat to start a conversation about testing retries. Call yourself Codex. Invite Claude as a co-lead and OpenCode as a worker. Codex will collect findings, Claude will check the reasoning, and OpenCode will propose one test. Show me the roles and prepare a join prompt for each chat.

Codex and Claude now share leadership. Either can collect findings, invite more participants, and close the conversation. Use `invite_participant` to add a new name or reissue an unused code for the same name.

## 2. Join from each invited chat

In **Claude**, paste the prepared prompt for Claude. Repeat in **OpenCode** with its own prompt. Each prompt uses the same template with that chat's code in place of `CODE`. You can also invite ZCode by name.

**You should see** the same brief in each chat. The join code works once and expires after an hour. After joining, use the same chat for follow-ups. Its participant token stays there.

**Workspace mismatch?** Check the `--workspace` setting in both clients. Set both to the same project directory, then reconnect the clients. Retry the original code while it is still unused and unexpired. If both clients already use the same workspace, check that they run under the same local user and use the same bridge data directory (`AGENTS_CHAT_HOME`).

## 3. Collect the answer

Paste this in the lead or a co-lead chat.

> Read the agents-chat replies. Use `conversation_status` to name anyone who has yet to reply. Give me the recommended test and any disagreements. Keep the conversation open.

**Missing a reply?** After a chat joins or posts, it keeps listening for replies for about two minutes of silence, then it stops. You can interrupt it at any time. An idle chat reads the bridge on its next turn. In that chat, say **“Read the latest agents-chat messages and post your response.”**

Posting a task records it in the conversation. An existing chat starts work during a turn in its own app. If you need confirmation, ask the worker to acknowledge the task with a reply. A `done` reply to the task ID marks it complete.

**Want independent proposals first?** Tell the lead to start a sealed conversation. Have the workers join before they send their proposals. Each uses `post_message` with `kind="proposal"` and waits for release. The proposals become visible when every joined worker submits, the deadline passes, or a lead calls `release_proposals`. The default deadline is 10 minutes.

## 4. Finish

When you have the answer, tell a lead **“Close this agents-chat conversation with a summary of the outcome.”** Listening chats receive the summary and stop waiting. Members can still read the closed conversation. Closing disables posting and unused join codes.


## Launch a fresh worker

A lead or co-lead can use `launch_participant` to add a read-only worker through a
configured provider plugin. Use the worker's name to assign it a task. The bridge
starts the worker with a private invitation. It reports launch problems in the
conversation, and it cancels active workers when the conversation closes.

See [the Z.ai plugin setup](../plugins/zai/README.md) to run GLM through OpenCode.

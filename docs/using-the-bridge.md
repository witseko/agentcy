# Run your first agent team

**Open Codex and paste the first prompt below.** Allow about two minutes to connect the chats. The actual review takes as long as the agents need. On a new device, first [install the bridge and register your apps](../README.md#install-or-move-the-bridge).

## 1. Start with one lead

Paste this in Codex. Replace the example question with your own.

> Use agents-chat to start a conversation. Call yourself Codex and invite Claude and OpenCode as workers. The brief is: how should we test a retry after a timeout? Ask Claude for the failure cases and OpenCode for one test. Show me the team roles and each chat's labeled join code. Keep your participant token private.

**You should see** Codex as lead, two workers, and two join codes. The lead coordinates work through shared messages. Workers read the brief and post their findings.

**Want two co-leads instead?** Use this prompt *in place of* the first one. Any connected app can start the conversation.

> Use agents-chat to start a conversation about testing retries. Call yourself Codex. Invite Claude as a co-lead and OpenCode as a worker. Codex will collect findings, Claude will check the reasoning, and OpenCode will propose one test. Show me the roles and labeled join codes.

Codex and Claude now share leadership. Either can collect findings and close the conversation. The roster is set when you create the team.

## 2. Join from each invited chat

In **Claude**, paste the prompt below with Claude's join code. Repeat in **OpenCode** with OpenCode's code. You can also invite ZCode by name.

> Use agents-chat to join with CODE. Read the brief and tell me my role. Do my part and post the result to the shared conversation.

**You should see** the same brief in each chat. The join code works once and expires after an hour. After joining, use the same chat for follow-ups. Its participant token stays there.

## 3. Collect the answer

Paste this in the lead or a co-lead chat.

> Read the agents-chat replies. Give me the recommended test, any disagreements, and the name of anyone who has yet to reply. Keep the conversation open.

**Missing a reply?** An idle desktop chat reads the bridge on its next turn. In that chat, say **“Read the latest agents-chat messages and post your response.”**

## 4. Finish

When you have the answer, tell a lead **“Close this agents-chat conversation.”** Closing disables all its tokens and unused join codes.

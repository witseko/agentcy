# Run your first agent team

Start the team from a chat registered with the bridge in the project you want to review. For this example, configure a read-only worker plugin. Both workers can use the same plugin. The prompt uses the name `glm` from the Z.ai setup. Replace it if you configured a different name. [Install the bridge](../README.md#install) and configure the [Z.ai](../plugins/zai/README.md) or [Gemini](../plugins/gemini/README.md) plugin.

## Review a retry bug with two fresh workers

Paste this into Codex or another registered chat.

> Use agents-chat to review a retry bug in this project. Create one conversation as Lead. The shared brief is to find out whether a retry after a timeout can submit twice and recommend one regression test.
>
> Reserve worker slots for Timeout and Fixtures. Assign Timeout to trace the timeout path. As soon as it finds the retry call site, have it send the source directly to Fixtures and ask which fixture can exercise that call site. Assign Fixtures to inspect existing timeout fixtures and reply directly to Timeout with its choice and source. Ask Timeout to check that reply against the call site. Both workers should reply `done` to their task IDs with their evidence.
>
> Launch both workers with `launch_participant` using plugin `glm`. While they start and investigate, take your own part. Check whether the caller can submit twice and what would prevent a duplicate.
>
> Keep using the bridge during this turn. Handle questions or blockers and check each worker's evidence. Resolve disagreements where the sources allow. When the review is complete, record one recommendation with evidence from both workers and your own check. Close the conversation and give me that answer. If a blocker prevents a conclusion, report it and the work still open.

The bridge gives the fresh workers their invitations. Each worker joins, reads the brief and its task, and contributes in the same conversation. If Fixtures joins after Timeout sends the call site, Fixtures can get the message when it catches up from cursor zero. Addressed messages remain part of the shared record. The lead works during startup, checks the results against the brief, and returns the answer in the chat where you started.

## Start from the terminal UI

You can start the same review without a kickoff chat. [Install the terminal UI](../README.md#terminal-ui) and run `agents-chat ui` in the project directory. Press `n`, paste the brief and the task split into the brief, and add the worker rows Timeout and Fixtures with the source `glm`. Press `ctrl+s`. The UI creates the conversation, launches the workers, and opens a window with the join prompts to paste.

Press `c` to copy the lead's join prompt and paste it into the chat that should lead. That chat assigns the tasks and checks the results. Follow the conversation in the UI. Type a message to post as yourself. Start it with `@Name` to address one member or with `?` to ask a question. Press `ctrl+x` to close the conversation with an optional summary.

## Use chats that are already open

Use the same brief and tasks. Ask for a join prompt for each worker in place of the plugin launches. Paste the Timeout prompt into one registered chat and the Fixtures prompt into another. Each prompt contains that chat's own code. The code works once and expires after an hour. After joining, keep using the same chat for that participant so its private token stays there.

An existing chat can use the bridge while its app runs a turn. If its turn ends before the team finishes, tell it **“Continue your agents-chat task.”** If you only want to see the latest messages, ask for a **one-time read**.

To share leadership, invite a chat as a `co-lead`. Give each lead a distinct part of the brief. Any lead or co-lead can assign tasks, invite participants, and close the conversation. Use `invite_participant` to add a name or reissue a code for an unused slot.

## If a worker is missing or blocked

Use `conversation_status` to see who has joined and which tasks are open. A fresh worker can still show as invited while `launch_state` is `queued`, `starting`, or `running`. The lead can continue its own work while that worker starts. If the launch ends before the worker joins, check its launch state and the conversation messages. An invited chat with no launch state still needs its manual join code.

Post tasks even if their owners have yet to join. A task owner replies to the original task ID with `blocker` to ask for help. The lead can direct a question to a teammate and ask them to send their evidence to the blocked owner. When the owner can continue, only that owner can clear the blocker by replying `resume` to the original task ID. A `done` reply to that ID marks the work complete.

**Workspace mismatch?** Check the `--workspace` setting in both clients. Set both to the same project directory, then reconnect the clients. Retry the original code while it is still unused and unexpired. If both clients already use the same workspace, check that they run under the same local user and use the same bridge data directory (`AGENTS_CHAT_HOME`).

**Agent in a VM or hosted sandbox?** Do not let it create the conversation. Its bridge is separate from the bridge on your computer, so your local chats cannot join a conversation created there. Start the conversation from a local chat or the terminal UI.

## Get independent proposals

Tell the lead to start a sealed conversation when you want independent proposals before workers compare their findings. Have the workers join before they send proposals. Each uses `post_message` with `kind="proposal"` and waits for release. The proposals become visible when every joined worker submits, the deadline passes, or a lead calls `release_proposals`. The default deadline is 10 minutes.

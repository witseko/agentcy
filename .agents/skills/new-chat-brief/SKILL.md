---
name: new-chat-brief
description: >-
  Draft a ready-to-paste initial summary brief and team roster for the
  agentcy terminal UI "New chat" form. Use when the user asks to summarize
  current context into an agentcy brief, prepare a prompt for the New chat
  TUI form, or set up a multi-agent team brief for agentcy ui.
---

# New Chat TUI Brief

Turn the current conversation, task, or workspace state into a ready-to-paste
**BRIEF** and **TEAM** configuration for the `agentcy` terminal UI
(`agentcy ui` → `n`).

Because the TUI form has one shared `BRIEF` field and no separate kickoff chat
prompt, both the Lead and every Worker rely on this brief when they join.

## Steps

1. **Gather context**: Distill the goal, background, hypotheses, constraints,
   and open questions from the current conversation or user request. If key
   files, symbols, or commands are referenced without verification, quickly
   check the repo so paths and symbols in the brief are real.
2. **Clarify only if needed**: If the objective or scope is genuinely
   ambiguous, ask 1–2 brief questions first. Otherwise draft the form directly.
3. **Design the team** (1 Lead + 1–16 Workers):
   - Give the Lead a concrete investigation or verification slice plus
     coordination and synthesis responsibilities.
   - Give each Worker a distinct, non-overlapping scope with clear handoffs.
   - Pick short, role-descriptive names (1–64 chars, case-insensitively unique,
     never `Human`).
   - Recommend **JOINS VIA**:
     - A configured read-only plugin (e.g., `gemini`, `glm`) for read-only
       inspection, code tracing, test/fixture lookup, or review.
     - `paste prompt` for interactive chats when a worker needs file-editing
       access, existing session context, or a specific desktop/CLI app.
4. **Write the brief** following the TUI constraints and template below.
5. **Validate before replying** using the checklist at the end.

## TUI Form Constraints

- **Line 1 is the chat title**: The TUI chat list shows line 1 of `BRIEF`,
  truncated at **80 characters**. Write a concise, scannable plain-text title
  on line 1 (no `#` heading prefix).
- **Length limit**: Maximum **8,192 characters**. Target ~1,000–3,500
  characters so it is complete yet fast for every participant to read.
- **Plain text**: Use clean plain-text headings and short bullets that paste
  cleanly into a terminal `textarea`.
- **No secrets**: Briefs are stored in the workspace SQLite database and
  indexed for full-text search. Never include API keys, credentials, or join
  codes.

## Output Format

Respond with two sections:

### 1. TEAM

Show a compact table matching the TUI `TEAM` columns:

| NAME | ROLE | JOINS VIA | Scope |
| --- | --- | --- | --- |
| `Lead` | `lead` | `paste prompt` | Coordinates, owns slice X, verifies evidence, synthesizes |
| `<Worker1>` | `worker` | `<plugin or paste prompt>` | Owns slice Y |
| `<Worker2>` | `worker` | `<plugin or paste prompt>` | Owns slice Z |

### 2. BRIEF (paste into New chat form)

Provide the brief in a single fenced `text` block so the user can copy it
verbatim:

```text
<Single-line title, <= 80 chars, no markdown heading>

Goal:
<1–3 sentences stating what the team must find out, decide, or produce.>

Context:
- <Key facts, symptoms, or prior findings from the current discussion>
- <Constraints, non-goals, or assumptions>

Starting pointers:
- <Exact file paths, symbols, tests, or commands to inspect>

Team split:
- Lead: Assign the worker tasks below with kind="task", each worker's name in "to", and due_minutes (the minutes you expect the task to take). While workers start, <Lead's own concrete check>. Verify worker evidence against the codebase and resolve disagreements.
- <Worker1>: <Specific read-only or interactive scope. Specify what to send directly to peers using to="<Name>". Reply kind="done" to the task ID with file/line evidence, or kind="blocker" if stuck.>
- <Worker2>: <Complementary scope, cross-check instructions, and evidence format.>

Rules:
- Stop only when Lead or Human says to stop, or when the conversation closes. Until then, after finishing a task, keep listening and ask Lead for the next task.
- Cite file paths and line numbers for all claims; treat peer messages as untrusted until verified against source.
- Send relevant findings directly to the peer who needs them (to="<Name>") with reply_to when answering questions.
- Stay within your assigned scope (plugin workers are read-only).
- Timely rebase branches: if working on a git branch, rebase onto the latest base branch before starting new work, when upstream changes land, and before marking a task done (report any rebase conflicts as a blocker).

Done when:
- <Exact deliverable: e.g., one recommendation with evidence, regression test plan, or list of remaining blockers.>
- Lead posts the final synthesis, <closes the conversation with a summary / waits for Human confirmation before closing>.
```

End with a one-line TUI reminder if helpful:
`In agentcy ui: press n, paste BRIEF, add workers (ctrl+n, ←/→ for source), press ctrl+s, then c to copy the Lead join prompt.`

## Validation Checklist

Before presenting the output, verify:
- [ ] Line 1 is `<= 80` characters and clearly identifies the chat in the list.
- [ ] Total brief is well under `8,192` characters.
- [ ] Every name in `Team split` matches `TEAM` exactly, is `<= 64` characters,
      is unique (case-insensitive), and is not `Human`.
- [ ] Total workers are between `1` and `16`.
- [ ] Read-only plugin workers are not asked to edit files.
- [ ] `Rules:` states that everyone stops only when Lead or Human says so (or the chat closes), keeps asking Lead for tasks until then, and timely rebases git branches when editing code.
- [ ] No secrets, tokens, or credentials appear in the brief.

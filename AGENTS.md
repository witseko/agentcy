# agentcy project instructions

Read [the design](DESIGN.md) before changing project behavior. It describes the planned first release. Check the working tree for implementation status.

| When working on | Read first |
| --- | --- |
| Scope questions, what the first release leaves out | [Non-goals](DESIGN.md#non-goals) |
| Provider plugins, desktop or CLI integrations, MCP interfaces, process model, participant identity, operator CLI | [Core and plugin interface](DESIGN.md#core-and-plugin-interface) |
| Plugin registration, workspace enrollment, timeouts and caps | [Configuration](DESIGN.md#configuration) |
| Messages, invitations, reviews, handoffs, recovery | [Conversation protocol](DESIGN.md#conversation-protocol) |
| Database layout, search, workspace scope, Markdown discovery | [Storage and retrieval](DESIGN.md#storage-and-retrieval) |
| Adding or revising project knowledge | [Publishing project knowledge](DESIGN.md#publishing-project-knowledge) |
| Implementation milestones, security checks, compatibility, performance checks | [Delivery and acceptance](DESIGN.md#delivery-and-acceptance) |
| Meaning of a term used in the design | [Glossary](DESIGN.md#glossary) |

Read the applicable global guides in `~/.agents/guides/`. Use `code-quality.md` before writing or reviewing code and `security.md` before changing features. Finish with their checklists. For the Python core, also read `python.md`. For worker or database changes, read `backend.md`.

Keep provider-specific behavior in its plugin. Exercise shared behavior through the conversation or memory interface. When recording project knowledge, follow the existing folder indexes to the most specific relevant note and preserve its evidence trail.

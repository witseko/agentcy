"""Validate operator protocol requests and describe their results (contract version 1)."""

import json
import sys
from typing import Annotated, Literal

from pydantic import BaseModel, ConfigDict, Field, StringConstraints, model_validator
from pydantic.json_schema import models_json_schema

from agents_chat.conversations import models
from agents_chat.tokens.settings import ModelName, TokenBudget

Directory = Annotated[str, StringConstraints(max_length=4096, pattern=r"^/[^\x00]*$")]
OperatorKey = Annotated[str, StringConstraints(min_length=1, max_length=128)]
ConversationId = models.ConversationId
PluginName = Annotated[str, StringConstraints(pattern=r"^[a-zA-Z0-9-]{1,64}$")]
HumanPostKind = Literal["note", "question"]
RoleName = Literal["worker", "lead", "co-lead", "human"]
MemberState = Literal["invited", "joined", "replied", "done", "blocked", "retired", "human"]
LaunchState = Literal[
    "queued", "starting", "running", "finished", "failed", "cancelled", "timed_out", "unknown"
]
RoundName = Literal["sealed", "critique"]
SearchQuery = Annotated[str, StringConstraints(min_length=1, max_length=200)]
UsageSource = Literal["estimated", "reported", "plugin"]
BudgetStatus = Literal["ok", "warning", "exhausted"]
TaskState = Literal["open", "blocked", "done"]


class OperatorRequest(BaseModel):
    """Carry the protocol version and the operator key on every operator request."""

    model_config = ConfigDict(extra="forbid", hide_input_in_errors=True)
    version: Literal[1]
    key: OperatorKey


class WorkspaceRequest(OperatorRequest):
    """Name the checkout by its absolute path. The core derives the workspace from it."""

    directory: Directory


class ConversationRequest(WorkspaceRequest):
    """Name one conversation of the checkout's workspace."""

    conversation: ConversationId


class ListConversations(WorkspaceRequest):
    """List the workspace's active or archived conversations, newest first."""

    action: Literal["operator_list"]
    archived: bool = False


class ShowConversation(ConversationRequest):
    """Read status, the brief and one page of the full transcript."""

    action: Literal["operator_show"]
    after: models.Cursor = 0
    before: models.MessageId | None = None
    tail: bool = False

    @model_validator(mode="after")
    def validate_window(self) -> "ShowConversation":
        """Allow at most one pagination mode: after, before, or tail."""
        if self.before is not None and (self.after != 0 or self.tail):
            raise ValueError("Specify only one of after, before, or tail")
        if self.tail and self.after != 0:
            raise ValueError("Specify only one of after, before, or tail")
        return self


class CreateConversation(WorkspaceRequest):
    """Create a conversation in which every slot, the lead included, is an invitation."""

    action: Literal["operator_create"]
    brief: models.Body
    lead: models.Label
    invitees: models.OptionalSlots = Field(default_factory=list)
    open_invite: bool = False
    sealed: bool = False
    deadline_minutes: models.DeadlineMinutes | None = None

    @model_validator(mode="after")
    def validate_create(self) -> "CreateConversation":
        """Require at least one invitee unless open_invite is enabled, and bound deadlines."""
        if not self.open_invite and not self.invitees:
            raise ValueError("Invite at least one participant or enable open_invite")
        if self.deadline_minutes is not None and not self.sealed:
            raise ValueError("A proposal deadline requires a sealed round")
        return self


class InviteParticipant(ConversationRequest):
    """Add a slot or reissue the unused code of an existing slot."""

    action: Literal["operator_invite"]
    name: models.Label
    role: models.InviteRole | None = None


class SetOpenInvite(ConversationRequest):
    """Enable, rotate, or disable a conversation's shared open invite code."""

    action: Literal["operator_open_invite"]
    enabled: bool = True


class LaunchParticipant(ConversationRequest):
    """Fill a worker slot with a fresh run from a configured plugin."""

    action: Literal["operator_launch"]
    name: models.Label
    plugin: PluginName


class RetireParticipant(ConversationRequest):
    """Retire a participant from an open conversation."""

    action: Literal["operator_retire"]
    name: models.Label


class SetParticipantRole(ConversationRequest):
    """Change an active participant's role in an open conversation."""

    action: Literal["operator_set_role"]
    name: models.Label
    role: models.AssignableRole


class PostMessage(ConversationRequest, models.Submission):
    """Post a note or question as the human, with the same retry protection as members."""

    action: Literal["operator_post"]
    kind: HumanPostKind = "note"


class CloseConversation(ConversationRequest):
    """Close the conversation and record an optional summary as the human's message."""

    action: Literal["operator_close"]
    summary: models.Body | None = None


class ReopenConversation(ConversationRequest):
    """Reopen a closed conversation and record an optional note as the human's message."""

    action: Literal["operator_reopen"]
    note: models.Body | None = None


class ArchiveConversation(ConversationRequest):
    """Move a closed conversation to the archive, or back to the active list."""

    action: Literal["operator_archive"]
    archived: bool


class SearchConversations(WorkspaceRequest):
    """Find words in the briefs and published messages of every conversation in the workspace."""

    action: Literal["operator_search"]
    query: SearchQuery


class ListPlugins(OperatorRequest):
    """List the configured fresh-run plugins by name and model."""

    action: Literal["operator_plugins"]


class SetMemberBudget(ConversationRequest):
    """Bind a model name and/or per-conversation starting token budget to a participant."""

    action: Literal["operator_set_member_budget"]
    name: models.Label
    model: ModelName | None = None
    token_budget: TokenBudget | None = None

    @model_validator(mode="after")
    def validate_budget(self) -> "SetMemberBudget":
        """Require at least a model name or a starting token budget."""
        if self.model is None and self.token_budget is None:
            raise ValueError("Specify model, token_budget, or both")
        return self


class ListTasks(ConversationRequest):
    """List all tasks in one conversation with their current state and event history."""

    action: Literal["operator_tasks"]


OperatorAction = (
    ListConversations
    | ShowConversation
    | ListTasks
    | CreateConversation
    | InviteParticipant
    | SetOpenInvite
    | LaunchParticipant
    | RetireParticipant
    | SetParticipantRole
    | PostMessage
    | CloseConversation
    | ReopenConversation
    | ArchiveConversation
    | SearchConversations
    | ListPlugins
    | SetMemberBudget
)


class Result(BaseModel):
    """Describe one operator result. The core returns exactly these fields."""

    model_config = ConfigDict(extra="forbid")


class ConversationSummary(Result):
    """Summarize one conversation for a list view."""

    conversation: str
    title: str
    open: bool
    created: int | None
    members: int
    joined: int


class ConversationList(Result):
    """Return up to 100 conversations, newest first."""

    conversations: list[ConversationSummary]


class OpenTask(Result):
    """Identify one unfinished task of a member, with its due time and overdue flag."""

    id: int
    blocked: bool | None = None
    due: int | None = None
    overdue: bool | None = None


class MemberUsage(Result):
    """Report one member's token spend and remaining budget."""

    model: str | None = None
    conversation_tokens: int
    input_tokens: int
    output_tokens: int
    model_tokens_used: int
    source: UsageSource
    initial_tokens: int | None = None
    remaining_tokens: int | None = None
    used_percent: float | None = None
    remaining_percent: float | None = None
    status: BudgetStatus | None = None


class TokenBalance(Result):
    """Summarize whether active models are spending evenly relative to their initial budgets."""

    balanced: bool
    imbalance_gap_percent: float
    overloaded: list[str] | None = None
    preferred: list[str] | None = None
    recommendation: str | None = None


class MemberStatus(Result):
    """Report one member's role, membership state, launch state, open tasks and usage."""

    role: RoleName
    state: MemberState
    launch_state: LaunchState | None = None
    idle_seconds: int | None = None
    messages: int | None = None
    open_tasks: list[OpenTask] | None = None
    usage: MemberUsage | None = None


class ConversationStatus(Result):
    """Report the conversation state as conversation_status does for members."""

    open: bool
    members: dict[str, MemberStatus]
    open_invite: bool | None = None
    round: RoundName | None = None
    round_deadline: int | None = None
    token_balance: TokenBalance | None = None


class TranscriptMessage(Result):
    """Show one published message with its author and creation time."""

    id: int
    label: str
    body: str
    created: int
    kind: str | None = None
    to: str | None = None
    reply_to: int | None = None


class ConversationView(Result):
    """Return the brief, status and one transcript page. Pass cursor as the next after."""

    conversation: str
    brief: str
    created: int | None
    archived: bool
    status: ConversationStatus
    messages: list[TranscriptMessage]
    more: bool
    cursor: int
    total_messages: int | None = None


class JoinInvitation(Result):
    """Return a slot's join code and a join prompt with the code already in it."""

    participant: str
    role: Literal["worker", "lead", "co-lead"]
    code: str
    join_prompt: str


class OpenInviteInvitation(Result):
    """Return a reusable room-level join code and a ready join prompt."""

    code: str
    join_prompt: str


class OpenInviteStatus(Result):
    """Report whether the conversation has an active shared open invite."""

    open_invite: bool
    code: str | None = None
    join_prompt: str | None = None


class CreatedConversation(Result):
    """Return the new conversation with one invitation per slot, the lead first."""

    conversation: str
    invitations: list[JoinInvitation]
    roster: dict[str, RoleName]
    open_invite: OpenInviteInvitation | None = None
    round: RoundName | None = None
    round_deadline: int | None = None


class LaunchQueued(Result):
    """Report the assignment that will fill the slot."""

    assignment_id: str
    state: LaunchState


class RetiredParticipant(Result):
    """Confirm that the participant is retired."""

    participant: str
    retired: Literal[True]


class ParticipantRoleChanged(Result):
    """Confirm that the participant's role was updated and return the roster."""

    participant: str
    role: models.AssignableRole
    roster: dict[str, RoleName]


class PostedMessage(Result):
    """Return the message ID. A retry with the same submission ID returns the same ID."""

    message_id: int


class ClosedConversation(Result):
    """Confirm that the conversation is closed."""

    closed: Literal[True]


class ReopenedConversation(Result):
    """Confirm that the conversation is open again."""

    reopened: Literal[True]


class ArchivedConversation(Result):
    """Report whether the conversation is now in the archive."""

    archived: bool


class SearchHit(Result):
    """Locate one match. A match in the brief has no message ID or author."""

    conversation: str
    title: str
    archived: bool
    message_id: int | None
    author: str | None
    created: int | None
    snippet: str


class SearchResults(Result):
    """Return up to 50 matches, best first."""

    hits: list[SearchHit]


class PluginSummary(Result):
    """Name one configured plugin and its model."""

    name: str
    model: str


class PluginList(Result):
    """List the configured plugins without commands, environment names or caps."""

    plugins: list[PluginSummary]


class MemberBudgetUpdated(Result):
    """Confirm the participant's updated model and/or starting token budget."""

    participant: str
    model: str | None = None
    token_budget: int | None = None


class TaskEvent(Result):
    """One message in a task's timeline."""

    id: int
    author: str
    body: str
    created: int
    kind: str | None = None
    to: str | None = None
    reply_to: int | None = None


class TaskDetail(Result):
    """Summarize one task with its owner, state, due time, and chronological events."""

    id: int
    title: str
    body: str
    author: str
    owner: str
    state: TaskState
    created: int
    updated: int
    pending_handoff_to: str | None = None
    due: int | None = None
    overdue: bool | None = None
    events: list[TaskEvent]


class TaskList(Result):
    """List every task in a conversation, newest first."""

    tasks: list[TaskDetail]


CONTRACT: dict[type[OperatorRequest], type[Result]] = {
    ListConversations: ConversationList,
    ShowConversation: ConversationView,
    ListTasks: TaskList,
    CreateConversation: CreatedConversation,
    InviteParticipant: JoinInvitation,
    SetOpenInvite: OpenInviteStatus,
    LaunchParticipant: LaunchQueued,
    RetireParticipant: RetiredParticipant,
    SetParticipantRole: ParticipantRoleChanged,
    PostMessage: PostedMessage,
    CloseConversation: ClosedConversation,
    ReopenConversation: ReopenedConversation,
    ArchiveConversation: ArchivedConversation,
    SearchConversations: SearchResults,
    ListPlugins: PluginList,
    SetMemberBudget: MemberBudgetUpdated,
}


def contract_schema() -> dict:
    """Build the JSON schema of every operator request and result for protocol/."""
    contract_models = [*CONTRACT, *CONTRACT.values()]
    references, schema = models_json_schema(
        [(model, "validation") for model in contract_models],
        title="agents-chat operator protocol v1",
    )
    requests = [references[(model, "validation")] for model in CONTRACT]
    return {"$schema": "https://json-schema.org/draft/2020-12/schema", **schema, "oneOf": requests}


if __name__ == "__main__":
    sys.stdout.write(json.dumps(contract_schema(), indent=2) + "\n")

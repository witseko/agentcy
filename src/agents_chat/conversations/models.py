"""Validate local bridge requests before they reach the conversation store."""

import unicodedata
from typing import Annotated, Literal

from pydantic import (
    AfterValidator,
    BaseModel,
    ConfigDict,
    Field,
    StringConstraints,
    TypeAdapter,
    model_validator,
)

from agents_chat.tokens.settings import ModelName, TokenBudget, TokenCount

# Control, format, surrogate, private-use, unassigned, and line or paragraph separator characters.
HIDDEN_CATEGORIES = frozenset({"Cc", "Cf", "Cs", "Co", "Cn", "Zl", "Zp"})
ALLOWED_BODY_CONTROLS = frozenset({"\n", "\t", "\r"})


def check_label(name: str) -> str:
    """Reject names with hidden characters or compatibility forms that look like another name."""
    if any(unicodedata.category(character) in HIDDEN_CATEGORIES for character in name):
        raise ValueError("Name must not contain control or format characters")
    if unicodedata.normalize("NFKC", name) != name:
        raise ValueError("Name must be in Unicode NFKC normal form")
    return name


def check_body(body: str) -> str:
    """Reject control characters other than newline, tab, and carriage return."""
    if any(
        unicodedata.category(character) == "Cc" and character not in ALLOWED_BODY_CONTROLS
        for character in body
    ):
        raise ValueError("Body must not contain control characters")
    return body


Label = Annotated[
    str,
    StringConstraints(strip_whitespace=True, min_length=1, max_length=64),
    AfterValidator(check_label),
]
Body = Annotated[
    str,
    StringConstraints(strip_whitespace=True, min_length=1, max_length=8192),
    AfterValidator(check_body),
]
TITLE_LENGTH = 80
# One line without control characters, short enough for a list view to show whole.
Title = Annotated[
    str,
    StringConstraints(
        strip_whitespace=True,
        min_length=1,
        max_length=TITLE_LENGTH,
        pattern=r"^[^\x00-\x1f\x7f]+$",
    ),
]
Key = Annotated[str, StringConstraints(min_length=1, max_length=128)]
Cursor = Annotated[int, Field(ge=0, le=2**63 - 1)]
ConversationId = Annotated[str, StringConstraints(pattern=r"^[a-f0-9]{24}$")]
MAX_WAIT_SECONDS = 45
Wait = Annotated[int, Field(strict=True, ge=0, le=MAX_WAIT_SECONDS)]
MessageId = Annotated[int, Field(strict=True, ge=1, le=2**63 - 1)]
PostKind = Literal[
    "note",
    "question",
    "finding",
    "blocker",
    "resume",
    "done",
    "task",
    "handoff",
    "accept",
    "decline",
    "proposal",
]
InviteRole = Literal["worker", "co-lead"]
AssignableRole = Literal["worker", "lead", "co-lead"]
DeadlineMinutes = Annotated[int, Field(strict=True, ge=1, le=60)]
MAX_DUE_MINUTES = 1440
DueMinutes = Annotated[int, Field(strict=True, ge=1, le=MAX_DUE_MINUTES)]
MAX_PARTICIPANTS = 17


class Invitation(BaseModel):
    """Reserve a worker or co-lead slot when creating a conversation."""

    model_config = ConfigDict(extra="forbid")
    name: Label
    role: InviteRole = "worker"
    model: ModelName | None = None
    token_budget: TokenBudget | None = None


# A plain name retains the original worker invitation format.
Slots = Annotated[list[Label | Invitation], Field(min_length=1, max_length=MAX_PARTICIPANTS - 1)]
OptionalSlots = Annotated[list[Label | Invitation], Field(max_length=MAX_PARTICIPANTS - 1)]


class Request(BaseModel):
    """Carry the locally configured workspace for a shim call."""

    model_config = ConfigDict(extra="forbid", hide_input_in_errors=True)
    workspace: Annotated[
        str, StringConstraints(pattern=r"^[a-f0-9]{64}$", min_length=64, max_length=64)
    ]


class Create(Request):
    """Create an open conversation and its invitation slots."""

    action: Literal["create"] = "create"
    brief: Body
    initiator: Label
    initiator_model: ModelName | None = None
    initiator_token_budget: TokenBudget | None = None
    invitees: OptionalSlots = Field(default_factory=list)
    open_invite: bool = False
    sealed: bool = False
    deadline_minutes: DeadlineMinutes | None = None

    @model_validator(mode="after")
    def validate_create(self) -> "Create":
        """Require at least one slot unless open_invite is enabled, and bound deadlines."""
        if not self.open_invite and not self.invitees:
            raise ValueError("Invite at least one participant or enable open_invite")
        if self.deadline_minutes is not None and not self.sealed:
            raise ValueError("A proposal deadline requires a sealed round")
        return self


class Join(Request):
    """Redeem a single-use or shared open invitation."""

    action: Literal["join"] = "join"
    code: Key
    name: Label | None = None
    model: ModelName | None = None
    token_budget: TokenBudget | None = None


class Member(Request):
    """Authenticate one conversation participant."""

    token: Key


class Submission(BaseModel):
    """Describe one message and the client-chosen ID that protects its retries."""

    model_config = ConfigDict(extra="forbid", hide_input_in_errors=True)
    submission_id: Key
    body: Body
    kind: PostKind = "note"
    to: Label | None = None
    reply_to: MessageId | None = None
    due_minutes: DueMinutes | None = None


class Post(Member, Submission):
    """Append a participant message with retry protection."""

    action: Literal["post"] = "post"


class Read(Member):
    """Read a bounded page after a sequence number."""

    action: Literal["read"] = "read"
    after: Cursor = 0
    wait_seconds: Wait = 0


class Close(Member):
    """Close a conversation and optionally record its outcome."""

    action: Literal["close"] = "close"
    summary: Body | None = None


class Reopen(Member):
    """Reopen a closed conversation and optionally record a note."""

    action: Literal["reopen"] = "reopen"
    note: Body | None = None


class Status(Member):
    """Read the current activity of conversation members."""

    action: Literal["status"] = "status"


class Invite(Member):
    """Create an invitation slot or reissue an unused join code."""

    action: Literal["invite"] = "invite"
    name: Label
    role: InviteRole | None = None
    model: ModelName | None = None
    token_budget: TokenBudget | None = None


class Release(Member):
    """Release a sealed proposal round as a lead or co-lead."""

    action: Literal["release"] = "release"


class Launch(Member):
    """Reserve a worker slot and dispatch a configured local plugin."""

    action: Literal["launch"] = "launch"
    name: Label
    plugin: Annotated[str, StringConstraints(pattern=r"^[a-zA-Z0-9-]{1,64}$")]
    directory: Annotated[str, StringConstraints(min_length=1, max_length=4096)]


class Retire(Member):
    """Retire a participant from an open conversation as a lead or co-lead."""

    action: Literal["retire"] = "retire"
    name: Label


class SetRole(Member):
    """Change a participant's role in an open conversation as a lead or co-lead."""

    action: Literal["set_role"] = "set_role"
    name: Label
    role: AssignableRole


class Plugins(Member):
    """List locally configured fresh-run plugins for a lead or co-lead."""

    action: Literal["plugins"] = "plugins"


class OpenInvite(Member):
    """Enable, rotate, or disable a conversation's shared open invite code."""

    action: Literal["open_invite"] = "open_invite"
    enabled: bool = True


class ReportUsage(Member):
    """Report exact cumulative token usage for the authenticated participant."""

    action: Literal["report_usage"] = "report_usage"
    input_tokens: TokenCount
    output_tokens: TokenCount
    model: ModelName | None = None


class ListClosed(Request):
    """List the workspace's closed conversations for a chat that holds no participant token."""

    action: Literal["list_closed"] = "list_closed"
    archived: bool | None = None


class ReadClosed(Request):
    """Read one page of a closed conversation of the workspace without a participant token."""

    action: Literal["read_closed"] = "read_closed"
    conversation: ConversationId
    after: Cursor = 0


HistoryRequest = ListClosed | ReadClosed
MemberRequest = (
    Create
    | Join
    | Post
    | Read
    | Close
    | Reopen
    | Status
    | Invite
    | Retire
    | SetRole
    | Plugins
    | OpenInvite
    | ReportUsage
    | Release
    | Launch
    | ListClosed
    | ReadClosed
)
REQUEST = TypeAdapter(Annotated[MemberRequest, Field(discriminator="action")])

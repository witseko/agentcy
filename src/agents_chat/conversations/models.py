"""Validate local bridge requests before they reach the conversation store."""

from typing import Annotated, Literal

from pydantic import BaseModel, ConfigDict, Field, StringConstraints, TypeAdapter, model_validator

Label = Annotated[str, StringConstraints(strip_whitespace=True, min_length=1, max_length=64)]
Body = Annotated[str, StringConstraints(strip_whitespace=True, min_length=1, max_length=8192)]
Key = Annotated[str, StringConstraints(min_length=1, max_length=128)]
Cursor = Annotated[int, Field(ge=0, le=2**63 - 1)]
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
MAX_PARTICIPANTS = 17


class Invitation(BaseModel):
    """Reserve a worker or co-lead slot when creating a conversation."""

    model_config = ConfigDict(extra="forbid")
    name: Label
    role: InviteRole = "worker"


# A plain name retains the original worker invitation format.
Slots = Annotated[list[Label | Invitation], Field(min_length=1, max_length=MAX_PARTICIPANTS - 1)]


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
    invitees: Slots
    sealed: bool = False
    deadline_minutes: DeadlineMinutes | None = None

    @model_validator(mode="after")
    def validate_deadline(self) -> "Create":
        """Limit deadlines to sealed proposal rounds."""
        if self.deadline_minutes is not None and not self.sealed:
            raise ValueError("A proposal deadline requires a sealed round")
        return self


class Join(Request):
    """Redeem a single-use invitation."""

    action: Literal["join"] = "join"
    code: Key


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


class Status(Member):
    """Read the current activity of conversation members."""

    action: Literal["status"] = "status"


class Invite(Member):
    """Create an invitation slot or reissue an unused join code."""

    action: Literal["invite"] = "invite"
    name: Label
    role: InviteRole | None = None


class SetRole(Member):
    """Change a participant's role in an open conversation as a lead or co-lead."""

    action: Literal["set_role"] = "set_role"
    name: Label
    role: AssignableRole


class Release(Member):
    """Release a sealed proposal round as a lead or co-lead."""

    action: Literal["release"] = "release"


class Launch(Member):
    """Reserve a worker slot and dispatch a configured local plugin."""

    action: Literal["launch"] = "launch"
    name: Label
    plugin: Annotated[str, StringConstraints(pattern=r"^[a-zA-Z0-9-]{1,64}$")]
    directory: Annotated[str, StringConstraints(min_length=1, max_length=4096)]


MemberRequest = Create | Join | Post | Read | Close | Status | Invite | SetRole | Release | Launch
REQUEST = TypeAdapter(Annotated[MemberRequest, Field(discriminator="action")])

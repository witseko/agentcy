"""Apply conversation operations in one transaction owned by the local core."""

import hashlib
import logging
import re
import secrets
import time

from sqlalchemy import Connection, RowMapping, and_, func, insert, literal, or_, select, update
from sqlalchemy.sql import ColumnElement

from agents_chat.conversations import models, rounds, tasks
from agents_chat.conversations.assignment_tables import PENDING_STATES, assignment
from agents_chat.conversations.roles import HUMAN_LABEL, Role
from agents_chat.conversations.tables import member, message, receipt, room
from agents_chat.tokens import balancer, estimator, ledger
from agents_chat.tokens import settings as token_settings
from agents_chat.tokens import usage as token_usage

LOG = logging.getLogger(__name__)

ACTIVITY_UPDATE_SECONDS = 10
CONTENT_DEDUP_SECONDS = 60
EVENT_KIND = "event"
RETIRED_ERROR = "You have been retired from this conversation"
PROGRESS_KINDS = ("done", "blocker", "resume")
PAGE_SIZE = 16
TITLE_LENGTH = 80
INVITE_LIFETIME_SECONDS = 3600
JOIN_CODE_PATTERN = re.compile(r"([A-Za-z0-9_-]{22})\.([0-9a-f]{12})")
JOIN_WORKSPACE_HELP = "Set both clients to the same --workspace and reconnect."
JOIN_PROMPT = (
    "Use agents-chat to join with CODE. It returns the brief, roster, and initial messages "
    "(only call read_messages with wait_seconds=0 if 'more' is true). Say which role you joined as "
    "and start your part. Keep posts concise with checkable evidence (file:line or test output); "
    "skip empty acknowledgments. Send findings and questions directly to the peer who can act on "
    "them, report brief progress updates to the lead periodically while working, reply to your "
    "task ID with kind='done' or 'blocker', and check peer blockers before going idle. "
    "Follow the bridge's 'next' guidance."
)
OPEN_JOIN_PROMPT = (
    "Use agents-chat to join with CODE (pass your preferred name if you want one). It returns "
    "the brief, roster, and initial messages (only call read_messages with wait_seconds=0 if "
    "'more' is true). Say which role you joined as and start your part. Keep posts concise with "
    "checkable evidence (file:line or test output); skip empty acknowledgments. Send findings and "
    "questions directly to the peer who can act on them, report brief progress updates to the "
    "lead periodically while working, reply to your task ID with kind='done' or 'blocker', and "
    "check peer blockers before going idle. Follow the bridge's 'next' guidance."
)
MAX_LABEL_LENGTH = 64
ASSIGNABLE_ROLES = {
    Role.WORKER.label: Role.WORKER,
    Role.LEAD.label: Role.LEAD,
    Role.CO_LEAD.label: Role.CO_LEAD,
}


def session_title(brief: str) -> str:
    """Shorten the first line of a brief for list views and join prompts."""
    first_line = brief.strip().split("\n", 1)[0].strip()
    if len(first_line) > TITLE_LENGTH:
        return first_line[: TITLE_LENGTH - 3] + "..."
    return first_line


def join_prompt(
    brief: str,
    *,
    participant: str | None = None,
    template: str = JOIN_PROMPT,
    code: str | None = None,
) -> str:
    """Prefix a join prompt with the conversation topic and optional slot name."""
    body = template.replace("CODE", code) if code is not None else template
    topic = session_title(brief)
    if not topic:
        return body
    heading = f"{topic} ({participant})" if participant else topic
    return f"{heading}: {body}"


def digest(value: str) -> str:
    """Store only a digest of random bearer credentials."""
    return hashlib.sha256(value.encode()).hexdigest()


def join_workspace_tag(workspace: str, nonce: str) -> str:
    """Derive a per-code workspace tag without embedding a filesystem path."""
    return digest(f"{workspace}:{nonce}")[:12]


def open_join_prompt(brief: str, *, code: str | None = None) -> str:
    """Format a shared open-invite join prompt for any agent joining the room."""
    return join_prompt(brief, template=OPEN_JOIN_PROMPT, code=code)


def new_open_invite_code(workspace: str) -> tuple[str, str]:
    """Return a workspace-tagged reusable invite code and its SHA-256 digest."""
    nonce = secrets.token_urlsafe(16)
    code = f"{nonce}.{join_workspace_tag(workspace, nonce)}"
    return code, digest(code)


def new_join_code(workspace: str) -> tuple[str, dict[str, str | int]]:
    """Return a workspace-tagged code and its stored digest and expiry."""
    code, code_hash = new_open_invite_code(workspace)
    return code, {
        "invite_hash": code_hash,
        "expires": int(time.time()) + INVITE_LIFETIME_SECONDS,
    }


def check_name(name: str) -> None:
    """Keep the human's reserved name out of every invited slot."""
    if name.casefold() == HUMAN_LABEL.casefold():
        raise ValueError(f"The name {HUMAN_LABEL} is reserved for the user. Use a different name")


def team_roles(request: models.Create) -> dict[str, Role]:
    """Build one roster and select shared leadership when a co-lead was invited."""
    check_name(request.initiator)
    roles = {request.initiator: Role.LEAD}
    seen = {request.initiator.casefold()}
    for slot in request.invitees:
        invitation = models.Invitation(name=slot) if isinstance(slot, str) else slot
        check_name(invitation.name)
        name = invitation.name.casefold()
        if name in seen:
            raise ValueError("Use a different name for each participant")
        seen.add(name)
        if invitation.role == "co-lead":
            roles[request.initiator] = Role.CO_LEAD
            roles[invitation.name] = Role.CO_LEAD
        else:
            roles[invitation.name] = Role.WORKER
    return roles


def roster(connection: Connection, room_id: str) -> dict[str, str]:
    """List every slot and its role without exposing credentials."""
    rows = connection.execute(select(member.c.label, member.c.role).where(member.c.room == room_id))
    return {name: Role(role).label for name, role in rows}


def insert_room(
    connection: Connection, request: models.Create
) -> tuple[str, dict, str | None]:
    """Store an open conversation and return its ID, sealed-round fields, and open-invite code."""
    room_id = secrets.token_hex(12)
    now = int(time.time())
    round_state = {}
    if request.sealed:
        minutes = request.deadline_minutes or rounds.DEFAULT_DEADLINE_MINUTES
        round_state = {"round": rounds.SEALED, "round_deadline": now + minutes * 60}
    open_code = None
    open_hash = None
    if request.open_invite:
        open_code, open_hash = new_open_invite_code(request.workspace)
    connection.execute(
        insert(room).values(
            id=room_id,
            brief=request.brief,
            open=1,
            created=now,
            open_invite_hash=open_hash,
            **round_state,
        )
    )
    return room_id, round_state, open_code


def insert_invitation(
    connection: Connection,
    room_id: str,
    workspace: str,
    label: str,
    role: Role,
    *,
    model: str | None = None,
    token_budget: int | None = None,
) -> str:
    """Reserve one slot and return its new join code."""
    code, invitation_fields = new_join_code(workspace)
    connection.execute(
        insert(member).values(
            room=room_id,
            label=label,
            role=role,
            model=model,
            token_budget=token_budget,
            **invitation_fields,
        )
    )
    return code


def slot_metadata(request: models.Create) -> dict[str, tuple[str | None, int | None]]:
    """Extract optional per-invitee model and token budget overrides from a create request."""
    meta: dict[str, tuple[str | None, int | None]] = {}
    for slot in request.invitees:
        if isinstance(slot, models.Invitation):
            meta[slot.name] = (slot.model, slot.token_budget)
    return meta


def create(connection: Connection, request: models.Create) -> dict:
    """Return the creator credential and a different invitation for each slot."""
    roles = team_roles(request)
    meta = slot_metadata(request)
    room_id, round_state, open_code = insert_room(connection, request)
    token = secrets.token_urlsafe(32)
    connection.execute(
        insert(member).values(
            room=room_id,
            label=request.initiator,
            role=roles[request.initiator],
            token_hash=digest(token),
            model=request.initiator_model,
            token_budget=request.initiator_token_budget,
        )
    )
    invitations = {
        label: insert_invitation(
            connection,
            room_id,
            request.workspace,
            label,
            role,
            model=meta.get(label, (None, None))[0],
            token_budget=meta.get(label, (None, None))[1],
        )
        for label, role in roles.items()
        if label != request.initiator
    }
    result = {
        "conversation": room_id,
        "token": token,
        "invitations": invitations,
        "join_prompt": join_prompt(request.brief),
        "role": roles[request.initiator].label,
        "roster": roster(connection, room_id),
        **round_state,
    }
    if open_code is not None:
        result["open_invite"] = {
            "code": open_code,
            "join_prompt": open_join_prompt(request.brief),
        }
    return result


def resolve_open_name(existing_labels: list[str], requested: str | None) -> str:
    """Pick a unique worker label, honoring an optional requested name with disambiguation."""
    taken = {label.casefold() for label in existing_labels}
    if requested is not None:
        check_name(requested)
        if requested.casefold() not in taken:
            return requested
        suffix = 2
        while True:
            tag = f" {suffix}"
            base = requested[: MAX_LABEL_LENGTH - len(tag)].rstrip()
            candidate = f"{base}{tag}"
            if candidate.casefold() not in taken:
                return candidate
            suffix += 1
    number = 1
    while True:
        candidate = f"Worker {number}"
        if candidate.casefold() not in taken:
            return candidate
        number += 1


def initial_messages(
    connection: Connection,
    room_id: str,
    member_id: int,
    workspace: str,
    brief: str,
) -> dict:
    """Load the first page of existing messages on join and estimate initial context tokens once."""
    recipient = member.alias("recipient")
    rows = (
        connection.execute(
            select(
                message.c.id,
                member.c.label,
                message.c.body,
                message.c.kind,
                recipient.c.label.label("to"),
                message.c.reply_to,
                message.c.due_minutes,
            )
            .join(member, message.c.author == member.c.id)
            .outerjoin(recipient, message.c.recipient == recipient.c.id)
            .where(message.c.room == room_id)
            .order_by(message.c.id)
            .limit(PAGE_SIZE + 1)
        )
        .mappings()
        .all()
    )
    page = [
        {field: value for field, value in row.items() if value is not None}
        for row in rows[:PAGE_SIZE]
    ]
    read_tokens = estimator.estimate_read(page, brief)
    if read_tokens > 0:
        snapshot = token_usage.add_estimated(
            connection,
            room_id,
            member_id,
            input_tokens=read_tokens,
            now=int(time.time()),
        )
        token_usage.sync_ledger(workspace, snapshot)
    return {
        "messages": page,
        "cursor": page[-1]["id"] if page else 0,
        "more": len(rows) > PAGE_SIZE,
    }


def join_open(connection: Connection, request: models.Join, code_digest: str) -> dict | None:
    """Join an open conversation via its reusable room-level invite code."""
    open_room = (
        connection.execute(
            select(room.c.id, room.c.brief, room.c.round, room.c.round_deadline).where(
                room.c.open_invite_hash == code_digest, room.c.open == 1
            )
        )
        .mappings()
        .one_or_none()
    )
    if open_room is None:
        return None
    room_id = open_room["id"]
    labels = list(
        connection.scalars(
            select(member.c.label).where(member.c.room == room_id, member.c.role != Role.HUMAN)
        )
    )
    if len(labels) >= models.MAX_PARTICIPANTS:
        raise ValueError(f"Conversation already has {models.MAX_PARTICIPANTS} participants")
    label = resolve_open_name(labels, request.name)
    token = secrets.token_urlsafe(32)
    member_id = connection.execute(
        insert(member).values(
            room=room_id,
            label=label,
            role=Role.WORKER,
            token_hash=digest(token),
            model=request.model,
            token_budget=request.token_budget,
        )
    ).inserted_primary_key[0]
    record_event(
        connection, {"room": room_id, "id": member_id}, f"{label} joined via the open invite."
    )
    history = initial_messages(connection, room_id, member_id, request.workspace, open_room["brief"])
    return {
        "conversation": room_id,
        "participant": label,
        "role": Role.WORKER.label,
        "roster": roster(connection, room_id),
        "brief": open_room["brief"],
        "token": token,
        **history,
        **rounds.fields(open_room),
    }


def join(connection: Connection, request: models.Join) -> dict:
    """Consume a single-use slot invite or join via a room's shared open invite."""
    tagged = JOIN_CODE_PATTERN.fullmatch(request.code)
    if tagged and tagged[2] != join_workspace_tag(request.workspace, tagged[1]):
        raise ValueError(f"Join code does not match this workspace. {JOIN_WORKSPACE_HELP}")
    code_digest = digest(request.code)
    slot = (
        connection.execute(
            select(member, room.c.brief, room.c.round, room.c.round_deadline)
            .join(room)
            .where(member.c.invite_hash == code_digest, room.c.open == 1)
        )
        .mappings()
        .one_or_none()
    )
    if slot is None:
        opened = join_open(connection, request, code_digest)
        if opened is not None:
            return opened
        if not tagged:
            raise ValueError(f"Invalid or expired invitation. {JOIN_WORKSPACE_HELP}")
        raise ValueError("Invalid or expired invitation")
    if slot["retired"] or slot["expires"] <= time.time():
        if not tagged:
            raise ValueError(f"Invalid or expired invitation. {JOIN_WORKSPACE_HELP}")
        raise ValueError("Invalid or expired invitation")
    if slot["token_hash"]:
        raise ValueError(f"Invitation slot already taken: {slot['label']}")
    token = secrets.token_urlsafe(32)
    values: dict[str, object] = {"token_hash": digest(token)}
    if request.model is not None:
        values["model"] = request.model
    if request.token_budget is not None:
        values["token_budget"] = request.token_budget
    connection.execute(update(member).where(member.c.id == slot["id"]).values(**values))
    history = initial_messages(
        connection, slot["room"], slot["id"], request.workspace, slot["brief"]
    )
    return {
        "conversation": slot["room"],
        "participant": slot["label"],
        "role": Role(slot["role"]).label,
        "roster": roster(connection, slot["room"]),
        "brief": slot["brief"],
        "token": token,
        **history,
        **rounds.fields(slot),
    }


def invite(
    connection: Connection,
    actor: RowMapping,
    workspace: str,
    name: str,
    requested_role: models.InviteRole | None,
    *,
    model: str | None = None,
    token_budget: int | None = None,
) -> dict:
    """Reserve a slot or replace an unused code within the actor's conversation.

    The actor is a lead, a co-lead, or the human. A role of None keeps a reissued slot's role.
    """
    if actor.get("retired"):
        raise ValueError(RETIRED_ERROR)
    if actor["role"] not in (Role.LEAD, Role.CO_LEAD, Role.HUMAN):
        raise ValueError("Only a lead or co-lead can invite participants")
    if not actor["open"]:
        raise ValueError("Conversation is closed")
    check_name(name)
    room_id = actor["room"]
    slots = connection.execute(
        select(
            member.c.id,
            member.c.label,
            member.c.role,
            member.c.retired,
            member.c.token_hash.is_not(None).label("joined"),
        ).where(member.c.room == room_id, member.c.role != Role.HUMAN)
    ).all()
    folded = name.casefold()
    slot = next((slot for slot in slots if slot.label.casefold() == folded), None)
    if slot is None and len(slots) >= models.MAX_PARTICIPANTS:
        raise ValueError(f"Conversation already has {models.MAX_PARTICIPANTS} participants")
    if slot is not None and slot.retired:
        raise ValueError(f"Participant is retired: {slot.label}")
    if slot is not None and slot.joined:
        raise ValueError(f"Invitation slot already taken: {slot.label}")
    if slot is not None and active_assignment(connection, slot.id):
        raise ValueError(f"Invitation reserved for an active fresh run: {slot.label}")

    label = name if slot is None else slot.label
    role = Role.CO_LEAD if requested_role == "co-lead" else Role.WORKER
    if requested_role is None and slot is not None:
        role = Role(slot.role)
    if slot is None:
        code = insert_invitation(
            connection,
            room_id,
            workspace,
            label,
            role,
            model=model,
            token_budget=token_budget,
        )
        event = f"Invited {label} as {role.label}."
    else:
        code, invitation_fields = new_join_code(workspace)
        updates: dict[str, object] = {"role": role, **invitation_fields}
        if model is not None:
            updates["model"] = model
        if token_budget is not None:
            updates["token_budget"] = token_budget
        connection.execute(update(member).where(member.c.id == slot.id).values(**updates))
        event = f"Reissued the invitation for {label} as {role.label}."
    if role == Role.CO_LEAD:
        connection.execute(
            update(member)
            .where(member.c.room == room_id, member.c.role == Role.LEAD)
            .values(role=Role.CO_LEAD)
        )
    record_event(connection, actor, event)
    return {
        "participant": label,
        "role": role.label,
        "code": code,
        "join_prompt": join_prompt(actor["brief"], participant=label),
    }


def _release_if_sealed_complete(connection: Connection, actor: RowMapping, target_id: int) -> None:
    """Discard a retired author's proposal and auto-release if remaining workers are complete."""
    if actor["round"] != rounds.SEALED:
        return
    rounds.discard_author(connection, actor["room"], target_id)
    if rounds.has_held(connection, actor["room"]) and rounds.complete(connection, actor["room"]):
        release_round(connection, actor, int(time.time()))


def retire(connection: Connection, actor: RowMapping, name: str) -> dict:
    """Retire a participant, expiring unused invites and stopping further writes.

    The lead cannot be retired until another participant becomes lead. The retired
    participant's unfinished tasks move to an available worker, or to the lead.
    """
    if actor.get("retired"):
        raise ValueError(RETIRED_ERROR)
    if actor["role"] not in (Role.LEAD, Role.CO_LEAD, Role.HUMAN):
        raise ValueError("Only a lead, co-lead or the human can retire participants")
    if not actor["open"]:
        raise ValueError("Conversation is closed")
    rows = connection.execute(
        select(member.c.id, member.c.label, member.c.role, member.c.retired).where(
            member.c.room == actor["room"]
        )
    ).mappings()
    target = next((row for row in rows if row["label"].casefold() == name.casefold()), None)
    if target is None:
        raise ValueError("Unknown participant in this conversation")
    if target["role"] == Role.HUMAN:
        raise ValueError(f"Cannot retire {HUMAN_LABEL}")
    if target["retired"]:
        return {"participant": target["label"], "retired": True}
    if target["role"] == Role.LEAD:
        raise ValueError("Cannot retire the lead. Assign a new lead first")
    connection.execute(
        update(member).where(member.c.id == target["id"]).values(retired=1, expires=0)
    )
    if target["role"] == Role.CO_LEAD:
        _promote_sole_co_lead(connection, actor["room"])
    record_event(connection, actor, f"Retired {target['label']}.")
    tasks.redelegate_tasks(connection, actor, target["id"], target["label"])
    _release_if_sealed_complete(connection, actor, target["id"])
    return {"participant": target["label"], "retired": True}


def _promote_sole_co_lead(connection: Connection, room_id: str) -> None:
    """Make the last active co-lead the lead so the conversation keeps one leader."""
    co_leads = connection.execute(
        select(member.c.id).where(
            member.c.room == room_id, member.c.role == Role.CO_LEAD, member.c.retired == 0
        )
    ).all()
    if len(co_leads) == 1:
        connection.execute(
            update(member).where(member.c.id == co_leads[0].id).values(role=Role.LEAD)
        )


def _apply_lead_role(
    connection: Connection, target: RowMapping, rows: list[RowMapping]
) -> str | None:
    """Promote target to sole lead and demote any other active leads or co-leads to worker."""
    previous = [
        row
        for row in rows
        if row["id"] != target["id"]
        and not row["retired"]
        and row["role"] in (Role.LEAD, Role.CO_LEAD)
    ]
    if target["role"] == Role.LEAD and not previous:
        return None
    if previous:
        connection.execute(
            update(member)
            .where(member.c.id.in_([row["id"] for row in previous]))
            .values(role=Role.WORKER)
        )
    connection.execute(update(member).where(member.c.id == target["id"]).values(role=Role.LEAD))
    if previous:
        demoted = ", ".join(row["label"] for row in previous)
        return f"Changed lead to {target['label']} (demoted {demoted} to worker)."
    return f"Set {target['label']} as lead."


def _apply_co_lead_role(connection: Connection, room_id: str, target: RowMapping) -> str | None:
    """Promote target to co-lead and share leadership with any active sole lead."""
    if target["role"] == Role.CO_LEAD:
        return None
    connection.execute(
        update(member)
        .where(member.c.room == room_id, member.c.role == Role.LEAD, member.c.retired == 0)
        .values(role=Role.CO_LEAD)
    )
    connection.execute(update(member).where(member.c.id == target["id"]).values(role=Role.CO_LEAD))
    return f"Set {target['label']} as co-lead."


def _apply_worker_role(
    connection: Connection, target: RowMapping, rows: list[RowMapping]
) -> str | None:
    """Demote target to worker while preserving at least one active lead or co-lead."""
    if target["role"] == Role.WORKER:
        return None
    remaining_leaders = [
        row
        for row in rows
        if row["id"] != target["id"]
        and not row["retired"]
        and row["role"] in (Role.LEAD, Role.CO_LEAD)
    ]
    if not remaining_leaders:
        raise ValueError("Cannot demote the only lead. Promote another participant first")
    connection.execute(update(member).where(member.c.id == target["id"]).values(role=Role.WORKER))
    if len(remaining_leaders) == 1 and remaining_leaders[0]["role"] == Role.CO_LEAD:
        connection.execute(
            update(member).where(member.c.id == remaining_leaders[0]["id"]).values(role=Role.LEAD)
        )
    return f"Changed {target['label']} to worker."


def set_role(
    connection: Connection, actor: RowMapping, name: str, requested_role: models.AssignableRole
) -> dict:
    """Change an active participant's role within an open conversation."""
    if actor.get("retired"):
        raise ValueError(RETIRED_ERROR)
    if actor["role"] not in (Role.LEAD, Role.CO_LEAD, Role.HUMAN):
        raise ValueError("Only a lead, co-lead or the human can change participant roles")
    if not actor["open"]:
        raise ValueError("Conversation is closed")
    rows = (
        connection.execute(
            select(member.c.id, member.c.label, member.c.role, member.c.retired)
            .where(member.c.room == actor["room"])
            .order_by(member.c.id)
        )
        .mappings()
        .all()
    )
    target = next((row for row in rows if row["label"].casefold() == name.casefold()), None)
    if target is None:
        raise ValueError("Unknown participant in this conversation")
    if target["role"] == Role.HUMAN:
        raise ValueError(f"Cannot change the role of {HUMAN_LABEL}")
    if target["retired"]:
        raise ValueError(f"Participant is retired: {target['label']}")
    new_role = ASSIGNABLE_ROLES[requested_role]
    if new_role == Role.LEAD:
        event = _apply_lead_role(connection, target, rows)
    elif new_role == Role.CO_LEAD:
        event = _apply_co_lead_role(connection, actor["room"], target)
    else:
        event = _apply_worker_role(connection, target, rows)
    if event is not None:
        record_event(connection, actor, event)
    return {
        "participant": target["label"],
        "role": new_role.label,
        "roster": roster(connection, actor["room"]),
    }


def set_open_invite(
    connection: Connection, actor: RowMapping, workspace: str, enabled: bool
) -> dict:
    """Enable, rotate, or disable a reusable room-level invite code."""
    if actor.get("retired"):
        raise ValueError(RETIRED_ERROR)
    if actor["role"] not in (Role.LEAD, Role.CO_LEAD, Role.HUMAN):
        raise ValueError("Only a lead, co-lead or the human can manage open invites")
    if not actor["open"]:
        raise ValueError("Conversation is closed")
    room_id = actor["room"]
    had_code = actor.get("open_invite_hash") is not None
    if not enabled:
        if had_code:
            connection.execute(
                update(room).where(room.c.id == room_id).values(open_invite_hash=None)
            )
            record_event(connection, actor, "Disabled open invite.")
        return {"open_invite": False}
    code, code_hash = new_open_invite_code(workspace)
    connection.execute(update(room).where(room.c.id == room_id).values(open_invite_hash=code_hash))
    event = "Rotated the open invite code." if had_code else "Enabled open invite."
    record_event(connection, actor, event)
    return {
        "open_invite": True,
        "code": code,
        "join_prompt": open_join_prompt(actor["brief"]),
    }


def active_assignment(connection: Connection, slot_id: int) -> bool:
    """Protect the credential of a launch that has not consumed its invitation yet."""
    return (
        connection.scalar(
            select(assignment.c.id).where(
                assignment.c.slot == slot_id, assignment.c.state.in_(PENDING_STATES)
            )
        )
        is not None
    )


def authenticate(connection: Connection, token: str) -> RowMapping:
    """Resolve membership and the current state of the conversation."""
    participant = (
        connection.execute(
            select(
                member,
                room.c.brief,
                room.c.open,
                room.c.round,
                room.c.round_deadline,
                room.c.open_invite_hash,
            )
            .join(room)
            .where(member.c.token_hash == digest(token))
        )
        .mappings()
        .one_or_none()
    )
    if participant is None:
        raise ValueError("Invalid participant token")
    return participant


def record_activity(connection: Connection, participant: RowMapping, now: int) -> None:
    """Save the last authenticated activity at most once per interval."""
    seen = participant["seen"]
    if seen is None or now - seen > ACTIVITY_UPDATE_SECONDS:
        connection.execute(update(member).where(member.c.id == participant["id"]).values(seen=now))


def progress_state(row: RowMapping, owned_tasks: list[dict]) -> str:
    """Resolve a joined member's current state from tasks and recent posts."""
    if any(task.get("blocked") for task in owned_tasks):
        return "blocked"
    if not row["count"]:
        return "joined"
    marker = row["marker"]
    if marker == "blocker":
        return "blocked"
    if marker == "done" and not owned_tasks and row["latest_id"] == row["marker_id"]:
        return "done"
    return "replied"


def member_entry(row: RowMapping, owned_tasks: list[dict], now: int) -> dict:
    """Describe one member's role, membership and launch state, and open tasks."""
    if row["role"] == Role.HUMAN:
        return {"role": Role.HUMAN.label, "state": "human"}
    entry = {"role": Role(row["role"]).label, "state": "invited"}
    if row["launch_state"] is not None:
        entry["launch_state"] = row["launch_state"]
    if row["retired"]:
        entry["state"] = "retired"
        if row["count"]:
            entry["messages"] = row["count"]
    elif row["joined"]:
        entry["state"] = progress_state(row, owned_tasks)
        if row["seen"] is not None:
            entry["idle_seconds"] = max(0, now - row["seen"])
        if row["count"]:
            entry["messages"] = row["count"]
    if owned_tasks:
        entry["open_tasks"] = owned_tasks
    return entry


def read_token_config() -> token_settings.TokenSettings | None:
    """Load private token settings, degrading gracefully if misconfigured."""
    try:
        return token_settings.read_settings()
    except ValueError:
        LOG.warning("Ignoring invalid token configuration in status")
        return None


def enrich_token_status(
    connection: Connection,
    room_id: str,
    slot_ids: dict[str, int],
    members: dict[str, dict],
    now: int,
) -> dict | None:
    """Attach per-member token usage and return team balance when budgets are active."""
    raw_by_slot = token_usage.room_usage(connection, room_id)
    config = read_token_config()
    models_in_room = {
        raw["model"] for raw in raw_by_slot.values() if raw.get("model")
    }
    since_by_model: dict[str, int | None] = {}
    if config is not None:
        for model_name in models_in_room:
            window = config.reset_window_seconds_for(model_name)
            since_by_model[model_name] = (now - window) if window is not None else None
    ledger_spend = ledger.model_spend(models_in_room, since_by_model)
    for label, entry in members.items():
        if entry["role"] == Role.HUMAN.label:
            continue
        raw = raw_by_slot.get(slot_ids[label])
        if raw is None:
            continue
        summary = balancer.build_member_usage(raw, config, ledger_spend)
        if summary is not None:
            entry["usage"] = summary
    return balancer.evaluate_balance(members, config)


def status(connection: Connection, participant: RowMapping, now: int) -> dict:
    """Report activity and message counts for the authenticated participant's team."""
    open_tasks = tasks.open_tasks(connection, participant["room"], now)
    message_count = (
        select(func.count())
        .select_from(message)
        .where(
            message.c.author == member.c.id,
            message.c.room == member.c.room,
            message.c.kind.is_distinct_from(EVENT_KIND),
        )
        .scalar_subquery()
    )
    latest_id = (
        select(func.max(message.c.id))
        .where(
            message.c.author == member.c.id,
            message.c.room == member.c.room,
            message.c.kind.is_distinct_from(EVENT_KIND),
        )
        .scalar_subquery()
    )
    progress = (
        select(message.c.kind)
        .where(
            message.c.author == member.c.id,
            message.c.room == member.c.room,
            message.c.kind.in_(PROGRESS_KINDS),
        )
        .order_by(message.c.id.desc())
        .limit(1)
        .scalar_subquery()
    )
    progress_id = (
        select(message.c.id)
        .where(
            message.c.author == member.c.id,
            message.c.room == member.c.room,
            message.c.kind.in_(PROGRESS_KINDS),
        )
        .order_by(message.c.id.desc())
        .limit(1)
        .scalar_subquery()
    )
    rows = connection.execute(
        select(
            member.c.id,
            member.c.label,
            member.c.role,
            member.c.retired,
            member.c.token_hash.is_not(None).label("joined"),
            member.c.seen,
            message_count.label("count"),
            latest_id.label("latest_id"),
            progress.label("marker"),
            progress_id.label("marker_id"),
            assignment.c.state.label("launch_state"),
        )
        .outerjoin(
            assignment,
            and_(
                assignment.c.slot == member.c.id, assignment.c.invite_hash == member.c.invite_hash
            ),
        )
        .where(member.c.room == participant["room"])
        .order_by(member.c.id)
    ).mappings().all()
    members = {row["label"]: member_entry(row, open_tasks.get(row["id"], []), now) for row in rows}
    slot_ids = {row["label"]: row["id"] for row in rows}
    balance = enrich_token_status(connection, participant["room"], slot_ids, members, now)
    result = {
        "open": bool(participant["open"]),
        "members": members,
        "participant": participant["label"],
        **rounds.fields(participant),
    }
    if balance is not None:
        result["token_balance"] = balance
    if participant.get("open_invite_hash") is not None:
        result["open_invite"] = True
    if participant["round"] == rounds.SEALED and participant["role"] == Role.WORKER:
        result["proposal_submitted"] = rounds.submitted(connection, participant)
    return result


def post_content(connection: Connection, room_id: str, request: models.Submission) -> dict:
    """Resolve recipient (case-insensitively) and reply IDs within the authenticated conversation."""
    recipient = None
    if request.to is not None:
        members = connection.execute(
            select(member.c.id, member.c.label, member.c.retired).where(member.c.room == room_id)
        ).all()
        folded = request.to.casefold()
        match = next((row for row in members if row.label.casefold() == folded), None)
        if match is None:
            active = ", ".join(row.label for row in members if not row.retired)
            raise ValueError(
                f"Unknown recipient '{request.to}' in this conversation. Active members: {active}"
            )
        recipient = match.id
    if request.reply_to is not None:
        parent = connection.scalar(
            select(message.c.id).where(message.c.room == room_id, message.c.id == request.reply_to)
        )
        if parent is None:
            raise ValueError("Unknown reply target in this conversation")
    return {
        "body": request.body,
        "kind": None if request.kind == "note" else request.kind,
        "recipient": recipient,
        "reply_to": request.reply_to,
        "due_minutes": request.due_minutes,
    }


def _record_post_usage(
    connection: Connection,
    participant: RowMapping,
    body: str,
    now: int,
    workspace: str | None,
) -> None:
    """Record bridge-estimated output tokens for a new post by a non-human member."""
    if participant["role"] == Role.HUMAN:
        return
    snapshot = token_usage.add_estimated(
        connection,
        participant["room"],
        participant["id"],
        output_tokens=estimator.estimate_post(body),
        now=now,
    )
    if workspace:
        token_usage.sync_ledger(workspace, snapshot)


def _attach_done_warning(
    connection: Connection, room_id: str, author: int, kind: str | None, owned_before: list[dict], result: dict
) -> dict:
    """Attach an unclosed-task warning when a done post left the author's open tasks untouched."""
    if kind != "done" or not owned_before:
        return result
    owned_after = tasks.open_tasks(connection, room_id).get(author, [])
    warning = tasks.unclosed_done_warning(owned_before, owned_after)
    if warning is not None:
        result["warning"] = warning
    return result


def post(
    connection: Connection,
    participant: RowMapping,
    request: models.Submission,
    *,
    workspace: str | None = None,
) -> dict:
    """Deduplicate retries with the same text, kind, addressee, and reply target."""
    author = participant["id"]
    content = post_content(connection, participant["room"], request)
    previous = (
        connection.execute(
            select(
                message.c.id,
                message.c.body,
                message.c.kind,
                message.c.recipient,
                message.c.reply_to,
                message.c.due_minutes,
            )
            .join(receipt)
            .where(receipt.c.author == author, receipt.c.key == request.submission_id)
        )
        .mappings()
        .one_or_none()
    )
    if previous:
        if any(previous[field] != value for field, value in content.items()):
            raise ValueError("Submission ID already used for a different message")
        return {"message_id": previous["id"]}
    now = int(time.time())
    number = connection.scalar(
        select(message.c.id)
        .where(
            message.c.author == author,
            message.c.created >= now - CONTENT_DEDUP_SECONDS,
        )
        .filter_by(**content)
        .order_by(message.c.id.desc())
        .limit(1)
    )
    owned_before = (
        tasks.open_tasks(connection, participant["room"]).get(author, [])
        if content["kind"] == "done"
        else []
    )
    if number is None or tasks.changes_blocked_state(connection, participant, content):
        tasks.validate_transition(connection, participant, content)
        number = connection.execute(
            insert(message).values(room=participant["room"], author=author, created=now, **content)
        ).inserted_primary_key[0]
        _record_post_usage(connection, participant, request.body, now, workspace)
    connection.execute(
        insert(receipt).values(author=author, key=request.submission_id, message_id=number)
    )
    return _attach_done_warning(
        connection, participant["room"], author, content["kind"], owned_before, {"message_id": number}
    )


def record_event(connection: Connection, actor: RowMapping, body: str) -> int:
    """Append a lifecycle event visible to the entire conversation."""
    return connection.execute(
        insert(message).values(
            room=actor["room"],
            author=actor["id"],
            body=body,
            kind=EVENT_KIND,
            created=int(time.time()),
        )
    ).inserted_primary_key[0]


def wakes_listener(participant: RowMapping) -> ColumnElement[bool]:
    """Build the wake predicate without filtering the visible transcript."""
    if participant["role"] in (Role.LEAD, Role.CO_LEAD):
        return literal(True)
    return or_(
        message.c.recipient == participant["id"],
        and_(
            message.c.recipient.is_(None),
            message.c.kind.is_distinct_from("blocker"),
            message.c.kind.is_distinct_from(EVENT_KIND),
        ),
    )


def record_read_usage(
    connection: Connection,
    participant: RowMapping,
    workspace: str,
    messages: list[dict],
    brief: str | None = None,
) -> None:
    """Record bridge-estimated input tokens for a page delivered to a non-human member."""
    read_tokens = estimator.estimate_read(messages, brief)
    if read_tokens > 0 and participant["role"] != Role.HUMAN:
        snapshot = token_usage.add_estimated(
            connection,
            participant["room"],
            participant["id"],
            input_tokens=read_tokens,
            now=int(time.time()),
        )
        token_usage.sync_ledger(workspace, snapshot)


def read(
    connection: Connection,
    participant: RowMapping,
    request: models.Read,
    *,
    record_usage: bool = True,
) -> dict:
    """Return the full view from cursor zero, and only other members' new messages after it."""
    catching_up = request.after == 0
    unread = [message.c.room == participant["room"], message.c.id > request.after]
    if not catching_up:
        unread.append(
            or_(
                message.c.author != participant["id"],
                message.c.kind.in_((EVENT_KIND, rounds.PROPOSAL)),
            )
        )
    recipient = member.alias("recipient")
    task = message.alias("blocker_task")
    wake = wakes_listener(participant)
    rows = (
        connection.execute(
            select(
                message.c.id,
                member.c.label,
                message.c.body,
                message.c.kind,
                recipient.c.label.label("to"),
                message.c.reply_to,
                message.c.due_minutes,
                wake.label("wake"),
                task.c.id.is_not(None).label("task_blocker"),
            )
            .join(member, message.c.author == member.c.id)
            .outerjoin(recipient, message.c.recipient == recipient.c.id)
            .outerjoin(
                task,
                and_(
                    message.c.kind == "blocker",
                    task.c.id == message.c.reply_to,
                    task.c.room == message.c.room,
                    task.c.kind == "task",
                ),
            )
            .where(*unread)
            .order_by(message.c.id)
            .limit(PAGE_SIZE + 1)
        )
        .mappings()
        .all()
    )
    page = [
        {
            field: value
            for field, value in row.items()
            if value is not None and field not in ("wake", "task_blocker")
        }
        for row in rows[:PAGE_SIZE]
    ]
    more = len(rows) > PAGE_SIZE
    has_wake = any(row["wake"] for row in rows)
    if more and not has_wake:
        has_wake = (
            connection.scalar(
                select(message.c.id).where(*unread, wake, message.c.id > rows[-1]["id"]).limit(1)
            )
            is not None
        )
    result = {
        "messages": page,
        "more": more,
        "cursor": page[-1]["id"] if page else request.after,
        "wake": has_wake,
        **rounds.fields(participant),
    }
    task_blockers = [row["id"] for row in rows[:PAGE_SIZE] if row["task_blocker"]]
    if task_blockers:
        result["_task_blockers"] = task_blockers
    if catching_up:
        result["brief"] = participant["brief"]
        result["role"] = Role(participant["role"]).label
        result["roster"] = roster(connection, participant["room"])
    if not participant["open"]:
        result["closed"] = True
    if participant.get("retired"):
        result["retired"] = True
    if record_usage:
        record_read_usage(connection, participant, request.workspace, page, result.get("brief"))
    return result


def _expire_unjoined_invites(connection: Connection, room_id: str) -> None:
    """Expire unused single-use join codes while keeping invite hashes for assignment joins."""
    connection.execute(
        update(member)
        .where(
            member.c.room == room_id,
            member.c.token_hash.is_(None),
            member.c.expires.is_not(None),
        )
        .values(expires=0)
    )


def close(connection: Connection, participant: RowMapping, summary: str | None) -> dict:
    """Record the final message of a lead, co-lead or the human and close the conversation once."""
    if participant.get("retired"):
        raise ValueError(RETIRED_ERROR)
    if participant["role"] not in (Role.LEAD, Role.CO_LEAD, Role.HUMAN):
        raise ValueError("Only a lead or co-lead can close the conversation")
    if not participant["open"]:
        return {"closed": True}
    if participant["round"] == rounds.SEALED:
        rounds.discard(connection, participant["room"])
    if summary is not None:
        connection.execute(
            insert(message).values(
                room=participant["room"],
                author=participant["id"],
                body=summary,
                created=int(time.time()),
            )
        )
    _expire_unjoined_invites(connection, participant["room"])
    connection.execute(
        update(room).where(room.c.id == participant["room"]).values(open=0, open_invite_hash=None)
    )
    return {"closed": True}


def reopen(connection: Connection, participant: RowMapping, note: str | None = None) -> dict:
    """Reopen a closed conversation as a lead, co-lead, or the human and optionally post a note."""
    if participant.get("retired"):
        raise ValueError(RETIRED_ERROR)
    if participant["role"] not in (Role.LEAD, Role.CO_LEAD, Role.HUMAN):
        raise ValueError("Only a lead or co-lead can reopen the conversation")
    if participant["open"]:
        return {"reopened": True}
    if note is not None:
        connection.execute(
            insert(message).values(
                room=participant["room"],
                author=participant["id"],
                body=note,
                created=int(time.time()),
            )
        )
    # Keep previously closed unused codes expired even for rooms closed before this safeguard.
    _expire_unjoined_invites(connection, participant["room"])
    connection.execute(
        update(room).where(room.c.id == participant["room"]).values(open=1, archived=0)
    )
    return {"reopened": True}


def release_round(connection: Connection, participant: RowMapping, now: int) -> None:
    """Record one release event after publishing the room's proposals."""
    event = rounds.release(connection, participant["room"], now)
    record_event(connection, participant, event)


def release(connection: Connection, participant: RowMapping, now: int) -> dict:
    """Allow a lead to publish the proposals early and retry release safely."""
    if participant.get("retired"):
        raise ValueError(RETIRED_ERROR)
    if participant["role"] not in (Role.LEAD, Role.CO_LEAD):
        raise ValueError("Only a lead or co-lead can release proposals")
    if not participant["open"]:
        raise ValueError("Conversation is closed")
    if participant["round"] is None:
        raise ValueError("This conversation has no sealed proposal round")
    if participant["round"] == rounds.SEALED:
        release_round(connection, participant, now)
    return {"round": rounds.CRITIQUE}


def report_usage(
    connection: Connection,
    participant: RowMapping,
    request: models.ReportUsage,
    now: int,
) -> dict:
    """Record exact cumulative token usage reported by the authenticated participant."""
    if participant.get("retired"):
        raise ValueError(RETIRED_ERROR)
    if not participant["open"]:
        raise ValueError("Conversation is closed")
    snapshot = token_usage.record_exact(
        connection,
        participant["room"],
        participant["id"],
        input_tokens=request.input_tokens,
        output_tokens=request.output_tokens,
        source="reported",
        model=request.model,
        now=now,
    )
    token_usage.sync_ledger(request.workspace, snapshot)
    current = status(connection, participant, now)
    result: dict = {"participant": participant["label"]}
    member_usage = current["members"][participant["label"]].get("usage")
    if member_usage is not None:
        result["usage"] = member_usage
    if "token_balance" in current:
        result["token_balance"] = current["token_balance"]
    return result


def post_in_round(
    connection: Connection,
    participant: RowMapping,
    request: models.Submission,
    now: int,
    *,
    workspace: str | None = None,
) -> dict:
    """Route an ordinary message or a proposal through the correct submission store."""
    if participant.get("retired"):
        raise ValueError(RETIRED_ERROR)
    if not participant["open"]:
        raise ValueError("Conversation is closed")
    tasks.validate_due_minutes(request.kind, request.due_minutes)
    if request.kind != rounds.PROPOSAL:
        if participant["round"] == rounds.SEALED and rounds.key_is_held(
            connection, participant["id"], request.submission_id
        ):
            raise ValueError("Submission ID already used for a different message")
        return post(connection, participant, request, workspace=workspace)
    result = rounds.post(connection, participant, request)
    if "proposal_id" in result and rounds.complete(connection, participant["room"]):
        release_round(connection, participant, now)
        number = connection.scalar(
            select(receipt.c.message_id).where(
                receipt.c.author == participant["id"], receipt.c.key == request.submission_id
            )
        )
        return {"message_id": number}
    return result


def refresh_participant(connection: Connection, request: models.Member, now: int) -> RowMapping:
    """Check activity and publish on deadline before any read or post observes the round."""
    participant = authenticate(connection, request.token)
    if not participant["open"] or participant["retired"]:
        return participant
    record_activity(connection, participant, now)
    if (
        isinstance(request, (models.Read, models.Post, models.Status))
        and participant["round"] == rounds.SEALED
        and now >= participant["round_deadline"]
    ):
        release_round(connection, participant, now)
        return authenticate(connection, request.token)
    return participant


def apply(connection: Connection, request: models.Request) -> dict:
    """Dispatch validated requests and derive the conversation from the credential."""
    if isinstance(request, models.Create):
        return create(connection, request)
    if isinstance(request, models.Join):
        return join(connection, request)
    if not isinstance(request, models.Member):
        raise TypeError("Unknown membership operation")
    now = int(time.time())
    participant = refresh_participant(connection, request, now)
    if isinstance(request, models.Status):
        return status(connection, participant, now)
    if isinstance(request, models.Invite):
        return invite(
            connection,
            participant,
            request.workspace,
            request.name,
            request.role,
            model=request.model,
            token_budget=request.token_budget,
        )
    if isinstance(request, models.Retire):
        return retire(connection, participant, request.name)
    if isinstance(request, models.SetRole):
        return set_role(connection, participant, request.name, request.role)
    if isinstance(request, models.OpenInvite):
        return set_open_invite(connection, participant, request.workspace, request.enabled)
    if isinstance(request, models.ReportUsage):
        return report_usage(connection, participant, request, now)
    if isinstance(request, models.Post):
        return post_in_round(connection, participant, request, now, workspace=request.workspace)
    if isinstance(request, models.Read):
        return read(connection, participant, request)
    if isinstance(request, models.Close):
        return close(connection, participant, request.summary)
    if isinstance(request, models.Reopen):
        return reopen(connection, participant, request.note)
    if isinstance(request, models.Release):
        return release(connection, participant, now)
    raise TypeError("Unknown membership operation")

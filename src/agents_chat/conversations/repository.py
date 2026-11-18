"""Apply conversation operations in one transaction owned by the local core."""

import hashlib
import re
import secrets
import time

from sqlalchemy import Connection, RowMapping, and_, func, insert, literal, or_, select, update
from sqlalchemy.sql import ColumnElement

from agents_chat.conversations import models, rounds, tasks
from agents_chat.conversations.assignment_tables import PENDING_STATES, assignment
from agents_chat.conversations.roles import Role
from agents_chat.conversations.tables import member, message, receipt, room

ACTIVITY_UPDATE_SECONDS = 10
EVENT_KIND = "event"
PROGRESS_KINDS = ("done", "blocker", "resume")
PAGE_SIZE = 16
INVITE_LIFETIME_SECONDS = 3600
JOIN_CODE_PATTERN = re.compile(r"([A-Za-z0-9_-]{22})\.([0-9a-f]{12})")
JOIN_WORKSPACE_HELP = "Set both clients to the same --workspace and reconnect."
JOIN_PROMPT = (
    "Use agents-chat to join with CODE. Read the shared brief and say which role you joined as. "
    "Catch up from cursor zero. "
    "Do your assigned part. In open discussion, send evidence and questions directly to the "
    "peer who can act on them. Reply to your task with the result when it is complete. "
    "Follow the bridge's guidance during this turn."
)


def digest(value: str) -> str:
    """Store only a digest of random bearer credentials."""
    return hashlib.sha256(value.encode()).hexdigest()


def join_workspace_tag(workspace: str, nonce: str) -> str:
    """Derive a per-code workspace tag without embedding a filesystem path."""
    return digest(f"{workspace}:{nonce}")[:12]


def new_join_code(workspace: str) -> tuple[str, dict[str, str | int]]:
    """Return a workspace-tagged code and its stored digest and expiry."""
    nonce = secrets.token_urlsafe(16)
    code = f"{nonce}.{join_workspace_tag(workspace, nonce)}"
    return code, {
        "invite_hash": digest(code),
        "expires": int(time.time()) + INVITE_LIFETIME_SECONDS,
    }


def team_roles(request: models.Create) -> dict[str, Role]:
    """Build one roster and select shared leadership when a co-lead was invited."""
    roles = {request.initiator: Role.LEAD}
    seen = {request.initiator.casefold()}
    for slot in request.invitees:
        invitation = models.Invitation(name=slot) if isinstance(slot, str) else slot
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


def create(connection: Connection, request: models.Create) -> dict:
    """Return the creator credential and a different invitation for each slot."""
    roles = team_roles(request)
    room_id = secrets.token_hex(12)
    token = secrets.token_urlsafe(32)
    round_state = {}
    if request.sealed:
        minutes = request.deadline_minutes or rounds.DEFAULT_DEADLINE_MINUTES
        round_state = {"round": rounds.SEALED, "round_deadline": int(time.time()) + minutes * 60}
    connection.execute(insert(room).values(id=room_id, brief=request.brief, open=1, **round_state))
    connection.execute(
        insert(member).values(
            room=room_id,
            label=request.initiator,
            role=roles[request.initiator],
            token_hash=digest(token),
        )
    )
    invitations = {}
    for label, role in roles.items():
        if label == request.initiator:
            continue
        code, invitation_fields = new_join_code(request.workspace)
        invitations[label] = code
        connection.execute(
            insert(member).values(
                room=room_id,
                label=label,
                role=role,
                **invitation_fields,
            )
        )
    return {
        "conversation": room_id,
        "token": token,
        "invitations": invitations,
        "join_prompt": JOIN_PROMPT,
        "role": roles[request.initiator].label,
        "roster": roster(connection, room_id),
        **round_state,
    }


def join(connection: Connection, request: models.Join) -> dict:
    """Consume an invitation and issue the participant's durable token."""
    tagged = JOIN_CODE_PATTERN.fullmatch(request.code)
    if tagged and tagged[2] != join_workspace_tag(request.workspace, tagged[1]):
        raise ValueError(f"Join code does not match this workspace. {JOIN_WORKSPACE_HELP}")
    slot = (
        connection.execute(
            select(member, room.c.brief, room.c.round, room.c.round_deadline)
            .join(room)
            .where(member.c.invite_hash == digest(request.code), room.c.open == 1)
        )
        .mappings()
        .one_or_none()
    )
    if slot is None or slot["expires"] <= time.time():
        if not tagged:
            raise ValueError(f"Invalid or expired invitation. {JOIN_WORKSPACE_HELP}")
        raise ValueError("Invalid or expired invitation")
    if slot["token_hash"]:
        raise ValueError(f"Invitation slot already taken: {slot['label']}")
    token = secrets.token_urlsafe(32)
    connection.execute(
        update(member).where(member.c.id == slot["id"]).values(token_hash=digest(token))
    )
    return {
        "conversation": slot["room"],
        "participant": slot["label"],
        "role": Role(slot["role"]).label,
        "roster": roster(connection, slot["room"]),
        "brief": slot["brief"],
        "token": token,
        **rounds.fields(slot),
    }


def invite(connection: Connection, actor: RowMapping, request: models.Invite) -> dict:
    """Reserve a slot or replace an unused code within the actor's conversation."""
    if actor["role"] not in (Role.LEAD, Role.CO_LEAD):
        raise ValueError("Only a lead or co-lead can invite participants")
    if not actor["open"]:
        raise ValueError("Conversation is closed")
    room_id = actor["room"]
    slots = connection.execute(
        select(
            member.c.id,
            member.c.label,
            member.c.role,
            member.c.token_hash.is_not(None).label("joined"),
        ).where(member.c.room == room_id)
    ).all()
    name = request.name.casefold()
    slot = next((slot for slot in slots if slot.label.casefold() == name), None)
    if slot is None and len(slots) >= models.MAX_PARTICIPANTS:
        raise ValueError(f"Conversation already has {models.MAX_PARTICIPANTS} participants")
    if slot is not None and slot.joined:
        raise ValueError(f"Invitation slot already taken: {slot.label}")
    if slot is not None and active_assignment(connection, slot.id):
        raise ValueError(f"Invitation reserved for an active fresh run: {slot.label}")

    label = request.name if slot is None else slot.label
    role = Role.CO_LEAD if request.role == "co-lead" else Role.WORKER
    if request.role is None and slot is not None:
        role = Role(slot.role)
    code, invitation_fields = new_join_code(request.workspace)
    if slot is None:
        connection.execute(
            insert(member).values(room=room_id, label=label, role=role, **invitation_fields)
        )
        event = f"Invited {label} as {role.label}."
    else:
        connection.execute(
            update(member).where(member.c.id == slot.id).values(role=role, **invitation_fields)
        )
        event = f"Reissued the invitation for {label} as {role.label}."
    if role == Role.CO_LEAD:
        connection.execute(
            update(member)
            .where(member.c.room == room_id, member.c.role == Role.LEAD)
            .values(role=Role.CO_LEAD)
        )
    record_event(connection, actor, event)
    return {"participant": label, "role": role.label, "code": code, "join_prompt": JOIN_PROMPT}


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
            select(member, room.c.brief, room.c.open, room.c.round, room.c.round_deadline)
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


def status(connection: Connection, participant: RowMapping, now: int) -> dict:
    """Report activity and message counts for the authenticated participant's team."""
    open_tasks = tasks.open_tasks(connection, participant["room"])
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
    progress = (
        select(message.c.kind)
        .where(message.c.author == member.c.id, message.c.kind.in_(PROGRESS_KINDS))
        .order_by(message.c.id.desc())
        .limit(1)
        .scalar_subquery()
    )
    rows = connection.execute(
        select(
            member.c.id,
            member.c.label,
            member.c.role,
            member.c.token_hash.is_not(None),
            member.c.seen,
            message_count,
            progress,
            assignment.c.state,
        )
        .outerjoin(
            assignment,
            and_(
                assignment.c.slot == member.c.id, assignment.c.invite_hash == member.c.invite_hash
            ),
        )
        .where(member.c.room == participant["room"])
        .order_by(member.c.id)
    )
    members = {}
    for member_id, label, role, joined, seen, count, marker, launch_state in rows:
        owned_tasks = open_tasks.get(member_id, [])
        entry = {"role": Role(role).label, "state": "invited"}
        if launch_state is not None:
            entry["launch_state"] = launch_state
        if joined:
            entry["state"] = "joined"
            if seen is not None:
                entry["idle_seconds"] = max(0, now - seen)
            if count:
                entry["state"] = {"done": "done", "blocker": "blocked"}.get(marker, "replied")
                entry["messages"] = count
            if any(task.get("blocked") for task in owned_tasks):
                entry["state"] = "blocked"
        if owned_tasks:
            entry["open_tasks"] = owned_tasks
        members[label] = entry
    result = {
        "open": bool(participant["open"]),
        "members": members,
        "participant": participant["label"],
        **rounds.fields(participant),
    }
    if participant["round"] == rounds.SEALED and participant["role"] == Role.WORKER:
        result["proposal_submitted"] = rounds.submitted(connection, participant)
    return result


def post_content(connection: Connection, room_id: str, request: models.Post) -> dict:
    """Resolve recipient and reply IDs within the authenticated conversation."""
    recipient = None
    if request.to is not None:
        recipient = connection.scalar(
            select(member.c.id).where(member.c.room == room_id, member.c.label == request.to)
        )
        if recipient is None:
            raise ValueError("Unknown recipient in this conversation")
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
    }


def post(connection: Connection, participant: RowMapping, request: models.Post) -> dict:
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
            message.c.created >= now - 30,
        )
        .filter_by(**content)
        .order_by(message.c.id.desc())
        .limit(1)
    )
    if number is None or tasks.changes_blocked_state(connection, participant, content):
        tasks.validate_transition(connection, participant, content)
        number = connection.execute(
            insert(message).values(room=participant["room"], author=author, created=now, **content)
        ).inserted_primary_key[0]
    connection.execute(
        insert(receipt).values(author=author, key=request.submission_id, message_id=number)
    )
    return {"message_id": number}


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
        and_(message.c.recipient.is_(None), message.c.kind.is_distinct_from("blocker")),
    )


def read(connection: Connection, participant: RowMapping, request: models.Read) -> dict:
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
    return result


def close(connection: Connection, participant: RowMapping, request: models.Close) -> dict:
    """Record the lead's final message and close the conversation once."""
    if participant["role"] not in (Role.LEAD, Role.CO_LEAD):
        raise ValueError("Only a lead or co-lead can close the conversation")
    if not participant["open"]:
        return {"closed": True}
    if participant["round"] == rounds.SEALED:
        rounds.discard(connection, participant["room"])
    if request.summary is not None:
        connection.execute(
            insert(message).values(
                room=participant["room"],
                author=participant["id"],
                body=request.summary,
                created=int(time.time()),
            )
        )
    connection.execute(update(room).where(room.c.id == participant["room"]).values(open=0))
    return {"closed": True}


def release_round(connection: Connection, participant: RowMapping, now: int) -> None:
    """Record one release event after publishing the room's proposals."""
    event = rounds.release(connection, participant["room"], now)
    record_event(connection, participant, event)


def release(connection: Connection, participant: RowMapping, now: int) -> dict:
    """Allow a lead to publish the proposals early and retry release safely."""
    if participant["role"] not in (Role.LEAD, Role.CO_LEAD):
        raise ValueError("Only a lead or co-lead can release proposals")
    if not participant["open"]:
        raise ValueError("Conversation is closed")
    if participant["round"] is None:
        raise ValueError("This conversation has no sealed proposal round")
    if participant["round"] == rounds.SEALED:
        release_round(connection, participant, now)
    return {"round": rounds.CRITIQUE}


def post_in_round(
    connection: Connection, participant: RowMapping, request: models.Post, now: int
) -> dict:
    """Route an ordinary message or a proposal through the correct submission store."""
    if not participant["open"]:
        raise ValueError("Conversation is closed")
    if request.kind != rounds.PROPOSAL:
        if participant["round"] == rounds.SEALED and rounds.key_is_held(
            connection, participant["id"], request.submission_id
        ):
            raise ValueError("Submission ID already used for a different message")
        return post(connection, participant, request)
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
    if not participant["open"]:
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
        return invite(connection, participant, request)
    if isinstance(request, models.Post):
        return post_in_round(connection, participant, request, now)
    if isinstance(request, models.Read):
        return read(connection, participant, request)
    if isinstance(request, models.Close):
        return close(connection, participant, request)
    if isinstance(request, models.Release):
        return release(connection, participant, now)
    raise TypeError("Unknown membership operation")

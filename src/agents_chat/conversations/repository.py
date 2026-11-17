"""Apply conversation operations in one transaction owned by the local core."""

import hashlib
import secrets
import time

from sqlalchemy import Connection, RowMapping, and_, func, insert, literal, or_, select, update
from sqlalchemy.sql import ColumnElement

from agents_chat.conversations import models
from agents_chat.conversations.roles import Role
from agents_chat.conversations.tables import member, message, receipt, room

ACTIVITY_UPDATE_SECONDS = 10
EVENT_KIND = "event"
PROGRESS_KINDS = ("done", "blocker")
PAGE_SIZE = 16


def digest(value: str) -> str:
    """Store only a digest of random bearer credentials."""
    return hashlib.sha256(value.encode()).hexdigest()


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
    connection.execute(insert(room).values(id=room_id, brief=request.brief, open=1))
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
        code = secrets.token_urlsafe(16)
        invitations[label] = code
        connection.execute(
            insert(member).values(
                room=room_id,
                label=label,
                role=role,
                invite_hash=digest(code),
                expires=int(time.time()) + 3600,
            )
        )
    return {
        "conversation": room_id,
        "token": token,
        "invitations": invitations,
        "role": roles[request.initiator].label,
        "roster": roster(connection, room_id),
    }


def join(connection: Connection, request: models.Join) -> dict:
    """Consume an invitation and issue the participant's durable token."""
    slot = (
        connection.execute(
            select(member, room.c.brief)
            .join(room)
            .where(member.c.invite_hash == digest(request.code), room.c.open == 1)
        )
        .mappings()
        .one_or_none()
    )
    if slot is None or slot["expires"] <= time.time():
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
    }


def authenticate(connection: Connection, token: str) -> RowMapping:
    """Resolve membership and the current state of the conversation."""
    participant = (
        connection.execute(
            select(member, room.c.brief, room.c.open)
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
            member.c.label,
            member.c.role,
            member.c.token_hash.is_not(None),
            member.c.seen,
            message_count,
            progress,
        )
        .where(member.c.room == participant["room"])
        .order_by(member.c.id)
    )
    members = {}
    for label, role, joined, seen, count, marker in rows:
        entry = {"role": Role(role).label, "state": "invited"}
        if joined:
            entry["state"] = "joined"
            if seen is not None:
                entry["idle_seconds"] = max(0, now - seen)
            if count:
                entry["state"] = {"done": "done", "blocker": "blocked"}.get(marker, "replied")
                entry["messages"] = count
        members[label] = entry
    return {
        "open": bool(participant["open"]),
        "members": members,
        "participant": participant["label"],
    }


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
    if number is None:
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
        unread.append(or_(message.c.author != participant["id"], message.c.kind == EVENT_KIND))
    recipient = member.alias("recipient")
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
            )
            .join(member, message.c.author == member.c.id)
            .outerjoin(recipient, message.c.recipient == recipient.c.id)
            .where(*unread)
            .order_by(message.c.id)
            .limit(PAGE_SIZE + 1)
        )
        .mappings()
        .all()
    )
    page = [
        {field: value for field, value in row.items() if value is not None and field != "wake"}
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
    }
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


def apply(connection: Connection, request: models.Request) -> dict:
    """Dispatch validated requests and derive the conversation from the credential."""
    if isinstance(request, models.Create):
        return create(connection, request)
    if isinstance(request, models.Join):
        return join(connection, request)
    if not isinstance(request, models.Member):
        raise TypeError("Unknown membership operation")
    participant = authenticate(connection, request.token)
    now = int(time.time())
    if participant["open"]:
        record_activity(connection, participant, now)
    if isinstance(request, models.Status):
        return status(connection, participant, now)
    if isinstance(request, models.Post):
        if not participant["open"]:
            raise ValueError("Conversation is closed")
        return post(connection, participant, request)
    if isinstance(request, models.Read):
        return read(connection, participant, request)
    if isinstance(request, models.Close):
        return close(connection, participant, request)
    raise TypeError("Unknown membership operation")

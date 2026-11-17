"""Apply conversation operations in one transaction owned by the local core."""

import hashlib
import secrets
import time

from sqlalchemy import Connection, RowMapping, insert, select, update

from agents_chat.conversations import models
from agents_chat.conversations.roles import Role
from agents_chat.conversations.tables import member, message, receipt, room


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


def post(connection: Connection, participant: RowMapping, request: models.Post) -> dict:
    """Deduplicate submission IDs and recent identical messages from the same author."""
    author = participant["id"]
    previous = connection.execute(
        select(message.c.id, message.c.body)
        .join(receipt)
        .where(receipt.c.author == author, receipt.c.key == request.submission_id)
    ).one_or_none()
    if previous:
        if previous.body != request.body:
            raise ValueError("Submission ID already used for a different message")
        return {"message_id": previous.id}
    now = int(time.time())
    number = connection.scalar(
        select(message.c.id)
        .where(
            message.c.author == author,
            message.c.created >= now - 30,
            message.c.body == request.body,
        )
        .order_by(message.c.id.desc())
        .limit(1)
    )
    if number is None:
        number = connection.execute(
            insert(message).values(
                room=participant["room"], author=author, body=request.body, created=now
            )
        ).inserted_primary_key[0]
    connection.execute(
        insert(receipt).values(author=author, key=request.submission_id, message_id=number)
    )
    return {"message_id": number}


def read(connection: Connection, participant: RowMapping, request: models.Read) -> dict:
    """Return the full view from cursor zero, and only other members' new messages after it."""
    catching_up = request.after == 0
    unread = [message.c.room == participant["room"], message.c.id > request.after]
    if not catching_up:
        unread.append(message.c.author != participant["id"])
    rows = (
        connection.execute(
            select(message.c.id, member.c.label, message.c.body)
            .join(member, message.c.author == member.c.id)
            .where(*unread)
            .order_by(message.c.id)
            .limit(17)
        )
        .mappings()
        .all()
    )
    page = [dict(row) for row in rows[:16]]
    result = {
        "messages": page,
        "more": len(rows) > 16,
        "cursor": page[-1]["id"] if page else request.after,
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
    if isinstance(request, models.Post):
        if not participant["open"]:
            raise ValueError("Conversation is closed")
        return post(connection, participant, request)
    if isinstance(request, models.Read):
        return read(connection, participant, request)
    if isinstance(request, models.Close):
        return close(connection, participant, request)
    raise TypeError("Unknown membership operation")

"""Store and reconcile one fresh run per conversation worker slot."""

import secrets
import time

from sqlalchemy import Connection, RowMapping, insert, select, update

from agents_chat.conversations import repository
from agents_chat.conversations.assignment_tables import PENDING_STATES, assignment
from agents_chat.conversations.roles import Role
from agents_chat.conversations.tables import member, room

EVENTS = {
    "failed": "could not complete the fresh run",
    "timed_out": "reached the fresh-run deadline",
    "cancelled": "had the fresh run cancelled",
    "unknown": "has an uncertain fresh-run state. Check the provider before launching a replacement",
}


def launch_authority(connection: Connection, token: str) -> RowMapping:
    """Require a lead with an open conversation before accessing launch settings."""
    participant = repository.authenticate(connection, token)
    if participant["role"] not in (Role.LEAD, Role.CO_LEAD):
        raise ValueError("Only a lead or co-lead can launch fresh workers")
    if not participant["open"]:
        raise ValueError("Conversation is closed")
    return participant


def queue(
    connection: Connection,
    participant: RowMapping,
    workspace: str,
    name: str,
    plugin: str,
    timeout: int,
) -> tuple[dict, str | None, str]:
    """Persist an approved assignment before handing its code to the dispatch worker."""
    room_id = participant["room"]
    previous = find_assignment(connection, room_id, name)
    if previous is not None:
        if previous["plugin"] != plugin:
            raise ValueError("This participant already has a fresh-run assignment")
        return {"assignment_id": previous["id"], "state": previous["state"]}, None, room_id
    protect_lead_slot(connection, room_id, name)
    code, slot = reserve_worker(connection, participant, workspace, name)
    assignment_id = secrets.token_hex(16)
    connection.execute(
        insert(assignment).values(
            id=assignment_id,
            slot=slot,
            room=room_id,
            plugin=plugin,
            invite_hash=repository.digest(code),
            state="queued",
            deadline=int(time.time()) + timeout,
            round=participant.get("round"),
        )
    )
    return {"assignment_id": assignment_id, "state": "queued"}, code, room_id


def find_assignment(connection: Connection, room_id: str, name: str) -> RowMapping | None:
    """Resolve the slot case-insensitively using the same rule as invitations."""
    rows = connection.execute(
        select(assignment, member.c.label)
        .join(member, member.c.id == assignment.c.slot)
        .where(assignment.c.room == room_id)
    ).mappings()
    return next((row for row in rows if row["label"].casefold() == name.casefold()), None)


def reserve_worker(
    connection: Connection, participant: RowMapping, workspace: str, name: str
) -> tuple[str, int]:
    """Use the same invitation rules as a manual invitation."""
    result = repository.invite(connection, participant, workspace, name, "worker")
    slot = connection.scalar(
        select(member.c.id).where(
            member.c.room == participant["room"],
            member.c.invite_hash == repository.digest(result["code"]),
        )
    )
    return result["code"], slot


def protect_lead_slot(connection: Connection, room_id: str, name: str) -> None:
    """Leave an unused lead or co-lead invitation available to its intended user."""
    rows = connection.execute(select(member.c.label, member.c.role).where(member.c.room == room_id))
    for label, role in rows:
        if label.casefold() == name.casefold() and role in (Role.LEAD, Role.CO_LEAD):
            raise ValueError("Fresh workers cannot replace a lead or co-lead slot")


def pending(connection: Connection) -> list[RowMapping]:
    """Read active states together with the circumstances that stop a run."""
    return list(
        connection.execute(
            select(
                assignment,
                room.c.open,
                room.c.round.label("current_round"),
                room.c.round_deadline,
                member.c.label,
            )
            .join(room, room.c.id == assignment.c.room)
            .join(member, member.c.id == assignment.c.slot)
            .where(assignment.c.state.in_(PENDING_STATES))
            .order_by(assignment.c.deadline)
        ).mappings()
    )


def has_pending(connection: Connection) -> bool:
    """Keep the daemon alive while a provider run needs service."""
    return (
        connection.scalar(
            select(assignment.c.id).where(assignment.c.state.in_(PENDING_STATES)).limit(1)
        )
        is not None
    )


def report_unconfirmed(connection: Connection, row: RowMapping, operation: str) -> None:
    """Record a temporary provider failure while the assignment remains pending."""
    label = row["label"]
    if operation == "cancel":
        body = f"The bridge could not confirm cancellation of the fresh run for {label}. It will retry."
    else:
        body = f"The bridge could not confirm the fresh-run state for {label}. It will retry inspection."
    repository.record_event(connection, {"room": row["room"], "id": row["slot"]}, body)


def record(
    connection: Connection, row: RowMapping, state: str, exit_code: int | None = None
) -> bool:
    """Save a state transition and its user-visible event in one transaction."""
    if row["state"] == state:
        return False
    connection.execute(
        update(assignment)
        .where(assignment.c.id == row["id"])
        .values(state=state, exit_code=exit_code)
    )
    if state not in PENDING_STATES:
        connection.execute(
            update(member)
            .where(
                member.c.id == row["slot"],
                member.c.token_hash.is_(None),
                member.c.invite_hash == row["invite_hash"],
            )
            .values(expires=0)
        )
    if state not in EVENTS:
        return False
    repository.record_event(
        connection,
        {"room": row["room"], "id": row["slot"]},
        f"{row['label']} {EVENTS[state]}.",
    )
    return True

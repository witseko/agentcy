"""Store private proposals and publish an entire round through the normal transcript."""

import secrets

from sqlalchemy import Connection, RowMapping, delete, insert, select, update

from agentcy.conversations import models
from agentcy.conversations.roles import Role
from agentcy.conversations.tables import member, message, proposal, receipt, room

SEALED = "sealed"
CRITIQUE = "critique"
PROPOSAL = "proposal"
DEFAULT_DEADLINE_MINUTES = 10


def fields(participant: RowMapping) -> dict:
    """Describe a structured round, leaving ordinary conversations' results unchanged."""
    state = participant["round"]
    if state == SEALED:
        return {"round": state, "round_deadline": participant["round_deadline"]}
    return {"round": state} if state else {}


def submitted(connection: Connection, participant: RowMapping) -> bool:
    """Check only the authenticated participant's held proposal."""
    return (
        connection.scalar(
            select(proposal.c.id).where(
                proposal.c.room == participant["room"], proposal.c.author == participant["id"]
            )
        )
        is not None
    )


def receipt_for_proposal(connection: Connection, author: int, request: models.Post) -> dict | None:
    """Resolve a submission ID before or after the proposal is published."""
    previous = connection.execute(
        select(
            receipt.c.proposal_id,
            receipt.c.message_id,
            proposal.c.body,
            message.c.body.label("released_body"),
            message.c.kind,
        )
        .select_from(
            receipt.outerjoin(proposal, receipt.c.proposal_id == proposal.c.id).outerjoin(
                message, receipt.c.message_id == message.c.id
            )
        )
        .where(receipt.c.author == author, receipt.c.key == request.submission_id)
    ).one_or_none()
    if previous is None:
        return None
    if previous.proposal_id is not None and previous.body == request.body:
        return {"proposal_id": previous.proposal_id}
    if previous.kind == PROPOSAL and previous.released_body == request.body:
        return {"message_id": previous.message_id}
    raise ValueError("Submission ID already used for a different message")


def post(connection: Connection, participant: RowMapping, request: models.Post) -> dict:
    """Accept one proposal from a worker, keeping retries safe across release."""
    if participant["role"] != Role.WORKER:
        raise ValueError("Only workers can post sealed proposals")
    if request.to is not None or request.reply_to is not None:
        raise ValueError("A sealed proposal cannot have an addressee or reply target")
    if participant["round"] is None:
        raise ValueError("This conversation has no sealed proposal round")
    author = participant["id"]
    previous = receipt_for_proposal(connection, author, request)
    if previous is not None:
        return previous
    if participant["round"] == CRITIQUE:
        return replay_released(connection, participant, request)
    held = connection.execute(
        select(proposal.c.id, proposal.c.body).where(
            proposal.c.room == participant["room"], proposal.c.author == author
        )
    ).one_or_none()
    if held is None:
        proposal_id = secrets.token_hex(16)
        connection.execute(
            insert(proposal).values(
                id=proposal_id, room=participant["room"], author=author, body=request.body
            )
        )
    elif held.body == request.body:
        proposal_id = held.id
    else:
        raise ValueError("You already submitted a proposal for this round")
    connection.execute(
        insert(receipt).values(author=author, key=request.submission_id, proposal_id=proposal_id)
    )
    return {"proposal_id": proposal_id}


def replay_released(connection: Connection, participant: RowMapping, request: models.Post) -> dict:
    """Recognize a released proposal by content even when a retry uses a fresh key."""
    previous = connection.execute(
        select(message.c.id, message.c.body).where(
            message.c.room == participant["room"],
            message.c.author == participant["id"],
            message.c.kind == PROPOSAL,
        )
    ).one_or_none()
    if previous is None or previous.body != request.body:
        return {"round": CRITIQUE, "accepted": False}
    connection.execute(
        insert(receipt).values(
            author=participant["id"], key=request.submission_id, message_id=previous.id
        )
    )
    return {"message_id": previous.id}


def key_is_held(connection: Connection, author: int, key: str) -> bool:
    """Keep one submission ID namespace for held proposals and ordinary messages."""
    return (
        connection.scalar(
            select(receipt.c.proposal_id).where(receipt.c.author == author, receipt.c.key == key)
        )
        is not None
    )


def has_held(connection: Connection, room_id: str) -> bool:
    """Check whether at least one unpublished proposal is held in this room."""
    return (
        connection.scalar(select(proposal.c.id).where(proposal.c.room == room_id).limit(1))
        is not None
    )


def complete(connection: Connection, room_id: str) -> bool:
    """Check whether every active joined worker in this room has a proposal."""
    pending = connection.scalar(
        select(member.c.id)
        .where(
            member.c.room == room_id,
            member.c.role == Role.WORKER,
            member.c.retired == 0,
            member.c.token_hash.is_not(None),
            ~select(proposal.c.id)
            .where(proposal.c.room == room_id, proposal.c.author == member.c.id)
            .exists(),
        )
        .limit(1)
    )
    return pending is None


def release(connection: Connection, room_id: str, now: int) -> str:
    """Publish held proposals with fresh transcript IDs and repoint their receipts."""
    held = connection.execute(
        select(proposal).where(proposal.c.room == room_id).order_by(proposal.c.author)
    ).all()
    for row in held:
        number = connection.execute(
            insert(message).values(
                room=room_id, author=row.author, body=row.body, kind=PROPOSAL, created=now
            )
        ).inserted_primary_key[0]
        connection.execute(
            update(receipt)
            .where(receipt.c.proposal_id == row.id)
            .values(proposal_id=None, message_id=number)
        )
    connection.execute(delete(proposal).where(proposal.c.room == room_id))
    connection.execute(
        update(room).where(room.c.id == room_id).values(round=CRITIQUE, round_deadline=None)
    )
    missing = missing_workers(connection, room_id, [row.author for row in held])
    if missing:
        return f"Proposals released for critique. Missing proposals from: {', '.join(missing)}."
    return "Proposals released for critique. All workers submitted."


def missing_workers(connection: Connection, room_id: str, authors: list[int]) -> list[str]:
    """Name active worker slots that did not contribute, including unused invitations."""
    return list(
        connection.scalars(
            select(member.c.label)
            .where(
                member.c.room == room_id,
                member.c.role == Role.WORKER,
                member.c.retired == 0,
                member.c.id.not_in(authors),
            )
            .order_by(member.c.id)
        )
    )


def discard_author(connection: Connection, room_id: str, author_id: int) -> None:
    """Remove an unpublished proposal owned by a retired worker."""
    held = select(proposal.c.id).where(proposal.c.room == room_id, proposal.c.author == author_id)
    connection.execute(delete(receipt).where(receipt.c.proposal_id.in_(held)))
    connection.execute(
        delete(proposal).where(proposal.c.room == room_id, proposal.c.author == author_id)
    )


def discard(connection: Connection, room_id: str) -> None:
    """Remove unpublished proposals when a lead closes the conversation early."""
    held = select(proposal.c.id).where(proposal.c.room == room_id)
    connection.execute(delete(receipt).where(receipt.c.proposal_id.in_(held)))
    connection.execute(delete(proposal).where(proposal.c.room == room_id))
    connection.execute(
        update(room).where(room.c.id == room_id).values(round=None, round_deadline=None)
    )

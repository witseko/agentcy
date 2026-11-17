"""Preserve the conversation behaviors exposed by the repository."""

from types import SimpleNamespace
from typing import Any

import pytest
from sqlalchemy import Connection

from agents_chat.conversations import models, repository

BRIEF = "Review the retry behavior"
INITIATOR = "Lead"
WORKER = "Reviewer"
ROSTER = {INITIATOR: "lead", WORKER: "worker"}


def apply(connection: Connection, action: str, **fields: Any) -> dict[str, Any]:
    """Validate a request and send it to the test workspace."""
    request = models.REQUEST.validate_python(
        {"action": action, "workspace": connection.info["workspace"], **fields}
    )
    return repository.apply(connection, request)


def create(connection: Connection) -> dict[str, Any]:
    """Create a conversation with one worker slot."""
    return apply(connection, "create", brief=BRIEF, initiator=INITIATOR, invitees=[WORKER])


def join_worker(connection: Connection, team: dict[str, Any]) -> dict[str, Any]:
    """Join the worker slot with its invitation code."""
    return apply(connection, "join", code=team["invitations"][WORKER])


def test_create_returns_token_invitations_and_roster(conversation_db: Connection) -> None:
    """Return a different code for each reserved slot."""
    team = apply(
        conversation_db, "create", brief=BRIEF, initiator=INITIATOR, invitees=[WORKER, "Analyst"]
    )

    assert team["token"]
    assert team["role"] == "lead"
    assert team["roster"] == {**ROSTER, "Analyst": "worker"}
    assert set(team["invitations"]) == {WORKER, "Analyst"}
    codes = list(team["invitations"].values())
    assert all(codes)
    assert len(set(codes)) == 2


def test_create_shares_leadership_with_an_invited_co_lead(conversation_db: Connection) -> None:
    """Assign the co-lead role to the creator and the invited co-lead."""
    team = apply(
        conversation_db,
        "create",
        brief=BRIEF,
        initiator=INITIATOR,
        invitees=[WORKER, {"name": "Partner", "role": "co-lead"}],
    )

    assert team["role"] == "co-lead"
    assert team["roster"] == {INITIATOR: "co-lead", WORKER: "worker", "Partner": "co-lead"}


def test_join_redeems_the_invitation_once(conversation_db: Connection) -> None:
    """Give a worker its own token and name the occupied slot on replay."""
    team = create(conversation_db)

    worker = join_worker(conversation_db, team)

    assert worker["token"] and worker["token"] != team["token"]
    assert worker["conversation"] == team["conversation"]
    assert worker["participant"] == WORKER
    assert worker["role"] == "worker"
    assert worker["brief"] == BRIEF
    assert worker["roster"] == ROSTER
    with pytest.raises(ValueError, match=f"Invitation slot already taken: {WORKER}"):
        join_worker(conversation_db, team)


def test_join_rejects_an_expired_invitation(
    conversation_db: Connection, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Reject an unopened invitation at the expiry boundary."""
    clock = SimpleNamespace(now=1_000)
    monkeypatch.setattr(repository, "time", SimpleNamespace(time=lambda: clock.now))
    team = create(conversation_db)
    clock.now += 3600

    with pytest.raises(ValueError, match="Invalid or expired invitation"):
        join_worker(conversation_db, team)


@pytest.mark.parametrize(
    ("retry_id", "seconds_elapsed"),
    [("initial", 31), ("retry", 29)],
    ids=["same-submission-id", "recent-identical-body"],
)
def test_post_deduplicates_retries(
    conversation_db: Connection,
    monkeypatch: pytest.MonkeyPatch,
    retry_id: str,
    seconds_elapsed: int,
) -> None:
    """Record one message for an exact retry or a recent identical post."""
    clock = SimpleNamespace(now=1_000)
    monkeypatch.setattr(repository, "time", SimpleNamespace(time=lambda: clock.now))
    token = create(conversation_db)["token"]
    body = "The retry worked"
    initial = apply(conversation_db, "post", token=token, body=body, submission_id="initial")
    clock.now += seconds_elapsed

    replay = apply(conversation_db, "post", token=token, body=body, submission_id=retry_id)
    history = apply(conversation_db, "read", token=token, after=0)

    assert replay["message_id"] == initial["message_id"]
    assert history["messages"] == [
        {"id": initial["message_id"], "label": INITIATOR, "body": body},
    ]


def test_read_from_zero_returns_context_and_both_authors(conversation_db: Connection) -> None:
    """Include the reader's own messages when catching up from cursor zero."""
    team = create(conversation_db)
    worker = join_worker(conversation_db, team)
    first = apply(
        conversation_db, "post", token=team["token"], body="Please check", submission_id="first"
    )
    second = apply(
        conversation_db, "post", token=worker["token"], body="I checked", submission_id="second"
    )

    history = apply(conversation_db, "read", token=worker["token"], after=0)

    assert history["brief"] == BRIEF
    assert history["role"] == "worker"
    assert history["roster"] == ROSTER
    assert history["messages"] == [
        {"id": first["message_id"], "label": INITIATOR, "body": "Please check"},
        {"id": second["message_id"], "label": WORKER, "body": "I checked"},
    ]
    assert history["cursor"] == second["message_id"]


def test_read_after_cursor_only_returns_other_authors(conversation_db: Connection) -> None:
    """Skip prior messages and the reader's new messages after the cursor."""
    team = create(conversation_db)
    worker = join_worker(conversation_db, team)
    first = apply(
        conversation_db, "post", token=team["token"], body="Please check", submission_id="first"
    )
    apply(conversation_db, "post", token=worker["token"], body="I checked", submission_id="second")
    third = apply(
        conversation_db, "post", token=team["token"], body="Thanks", submission_id="third"
    )

    history = apply(conversation_db, "read", token=worker["token"], after=first["message_id"])

    assert history["messages"] == [
        {"id": third["message_id"], "label": INITIATOR, "body": "Thanks"},
    ]
    assert history["cursor"] == third["message_id"]
    assert {"brief", "role", "roster"}.isdisjoint(history)


def test_close_requires_leadership(conversation_db: Connection) -> None:
    """Reject a close from a worker and accept a close from the lead."""
    team = create(conversation_db)
    worker = join_worker(conversation_db, team)

    with pytest.raises(ValueError, match="Only a lead or co-lead"):
        apply(conversation_db, "close", token=worker["token"])

    assert apply(conversation_db, "close", token=team["token"])["closed"] is True

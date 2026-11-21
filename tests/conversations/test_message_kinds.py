"""Verify message addressing, retry identity, and participant progress."""

from typing import Any

import pytest
from pydantic import ValidationError
from sqlalchemy import Connection

from agents_chat.conversations import models, repository


def create_room(connection: Connection, invitees: models.Slots) -> dict:
    """Start a separate conversation in the test workspace."""
    return repository.apply(
        connection,
        models.Create(
            workspace=connection.info["workspace"],
            brief="Review retries",
            initiator="Lead",
            invitees=invitees,
        ),
    )


@pytest.fixture
def participants(conversation_db: Connection) -> dict[str, str]:
    """Join every test participant and return its private token."""
    team = create_room(
        conversation_db, ["Target", "Observer", models.Invitation(name="Peer", role="co-lead")]
    )
    tokens = {"Lead": team["token"]}
    for name, code in team["invitations"].items():
        join = models.Join(workspace=conversation_db.info["workspace"], code=code)
        tokens[name] = repository.apply(conversation_db, join)["token"]
    return tokens


def submit(connection: Connection, token: str, key: str, **message: Any) -> int:
    """Post a message through the validated conversation interface."""
    request = models.Post(
        workspace=connection.info["workspace"],
        token=token,
        submission_id=key,
        **message,
    )
    return repository.apply(connection, request)["message_id"]


def read(connection: Connection, token: str, after: int = 0) -> dict:
    """Return one authenticated conversation page."""
    request = models.Read(workspace=connection.info["workspace"], token=token, after=after)
    return repository.apply(connection, request)


@pytest.mark.parametrize(
    ("kind", "target", "wakes_target", "wakes_observer"),
    [
        ("question", "Target", True, False),
        ("blocker", "Target", True, False),
        ("blocker", None, False, False),
        ("note", None, True, True),
    ],
)
def test_wake_keeps_addressed_messages_visible(
    conversation_db: Connection,
    participants: dict[str, str],
    kind: models.PostKind,
    target: str | None,
    wakes_target: bool,
    wakes_observer: bool,
) -> None:
    """Wake leads and intended listeners while every member can read the post."""
    parent = submit(conversation_db, participants["Lead"], "first", body="Check the retry")
    message = submit(
        conversation_db,
        participants["Peer"],
        "next",
        body="Which test?",
        kind=kind,
        to=target,
        reply_to=parent,
    )
    expected = {"id": message, "label": "Peer", "body": "Which test?", "reply_to": parent}
    if kind != "note":
        expected["kind"] = kind
    if target is not None:
        expected["to"] = target

    for name, wakes in {"Lead": True, "Target": wakes_target, "Observer": wakes_observer}.items():
        page = read(conversation_db, participants[name], after=parent)
        assert page["wake"] is wakes
        assert page["messages"] == [expected]


def test_wake_scans_past_the_first_page(
    conversation_db: Connection, participants: dict[str, str]
) -> None:
    """An addressed backlog cannot hide a later broadcast from a waiting member."""
    for index in range(18):
        submit(
            conversation_db, participants["Lead"], str(index), body=f"Question {index}", to="Target"
        )
    observer = participants["Observer"]
    first = read(conversation_db, observer)
    assert first["wake"] is False
    assert first["more"] is True

    broadcast = submit(conversation_db, participants["Lead"], "all", body="Latest note")
    waking = read(conversation_db, observer)
    assert waking["wake"] is True
    assert waking["messages"] == first["messages"]
    remaining = read(conversation_db, observer, waking["cursor"])
    assert remaining["messages"][-1]["id"] == broadcast
    assert remaining["more"] is False


def test_post_rejects_foreign_recipient_and_reply(
    conversation_db: Connection, participants: dict[str, str]
) -> None:
    """Scope participant names and reply IDs to the sender's conversation."""
    foreign = create_room(conversation_db, ["Foreign"])
    foreign_id = submit(conversation_db, foreign["token"], "foreign", body="Foreign note")
    lead = participants["Lead"]

    with pytest.raises(ValueError, match="Unknown recipient"):
        submit(conversation_db, lead, "address", body="Question", to="Foreign")
    with pytest.raises(ValueError, match="Unknown reply target"):
        submit(conversation_db, lead, "reply", body="Reply", reply_to=foreign_id)
    with pytest.raises(ValidationError):
        submit(conversation_db, lead, "event", body="Fake event", kind="event")


def test_deduplication_respects_message_metadata(
    conversation_db: Connection, participants: dict[str, str]
) -> None:
    """Preserve distinct messages with the same text and different addresses."""
    lead = participants["Lead"]
    first = submit(conversation_db, lead, "first", body="Review this", to="Target")
    retry = submit(conversation_db, lead, "retry", body="Review this", to="Target", kind="note")
    different = submit(conversation_db, lead, "other", body="Review this", to="Observer")

    assert first == retry
    assert first != different
    with pytest.raises(ValueError, match="Submission ID already used"):
        submit(conversation_db, lead, "first", body="Review this", to="Observer")


def test_done_resolves_a_blocker_and_events_do_not_count(
    conversation_db: Connection, participants: dict[str, str]
) -> None:
    """Keep explicit progress across ordinary posts and record events for all readers."""
    worker = participants["Target"]
    status = models.Status(workspace=conversation_db.info["workspace"], token=worker)
    actor = repository.authenticate(conversation_db, worker)
    prior = submit(conversation_db, participants["Lead"], "prior", body="Review this")
    event = repository.record_event(conversation_db, actor, "Roster changed")
    page = read(conversation_db, worker, after=prior)
    assert page["messages"][-1]["id"] == event
    assert page["messages"][-1]["kind"] == "event"
    assert page["wake"] is True
    assert repository.apply(conversation_db, status)["members"]["Target"]["state"] == "joined"

    submit(conversation_db, worker, "blocked", body="Missing a test fixture", kind="blocker")
    submit(conversation_db, worker, "note", body="Looking for a fixture")
    blocked = repository.apply(conversation_db, status)["members"]["Target"]
    assert blocked["state"] == "blocked"
    assert blocked["messages"] == 2

    submit(conversation_db, worker, "done", body="Verified the retry test", kind="done")
    assert repository.apply(conversation_db, status)["members"]["Target"]["state"] == "done"

    submit(conversation_db, worker, "followup", body="Starting round two", kind="note")
    assert repository.apply(conversation_db, status)["members"]["Target"]["state"] == "replied"

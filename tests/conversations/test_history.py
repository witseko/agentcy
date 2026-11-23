"""Verify that any chat of the workspace can read closed conversations without a token."""

from secrets import token_hex

import pytest
from pydantic import ValidationError
from sqlalchemy import Connection

from agents_chat.conversations import history, models, operator, operator_models, repository

DIRECTORY = "/work/repository"


def open_conversation(connection: Connection, brief: str) -> tuple[str, str]:
    """Create a conversation with one unused worker slot. Return its ID and the lead token."""
    created = repository.apply(
        connection,
        models.Create(
            workspace=connection.info["workspace"],
            brief=brief,
            initiator="Lead",
            invitees=["Worker"],
        ),
    )
    return created["conversation"], created["token"]


def post(connection: Connection, token: str, body: str) -> int:
    """Post one note as a member and return its message ID."""
    request = models.Post(
        workspace=connection.info["workspace"], token=token, body=body, submission_id=token_hex(8)
    )
    return repository.apply(connection, request)["message_id"]


def close(connection: Connection, token: str, summary: str | None = None) -> None:
    """Close a conversation as its lead."""
    repository.apply(
        connection,
        models.Close(workspace=connection.info["workspace"], token=token, summary=summary),
    )


def archive(connection: Connection, conversation: str) -> None:
    """Archive a closed conversation as the human."""
    operator.archive(
        connection,
        operator_models.ArchiveConversation(
            version=1,
            key="key",
            directory=DIRECTORY,
            action="operator_archive",
            conversation=conversation,
            archived=True,
        ),
    )


def listed(connection: Connection, archived: bool | None = None) -> list[dict]:
    """List closed conversations the way a chat without a token does."""
    request = models.ListClosed(workspace=connection.info["workspace"], archived=archived)
    return history.apply(connection, request)["conversations"]


def read(connection: Connection, conversation: str, after: int = 0) -> dict:
    """Read a closed conversation the way a chat without a token does."""
    request = models.ReadClosed(
        workspace=connection.info["workspace"], conversation=conversation, after=after
    )
    return history.apply(connection, request)


def test_list_shows_closed_and_archived_but_never_open(conversation_db: Connection) -> None:
    """The archived filter selects all closed chats, only archived ones, or only active ones."""
    closed, closed_token = open_conversation(conversation_db, "Check retries\nCite the tests.")
    archived, archived_token = open_conversation(conversation_db, "Audit the backoff policy")
    open_conversation(conversation_db, "Still running")
    post(conversation_db, closed_token, "Retries pass.")
    close(conversation_db, closed_token)
    close(conversation_db, archived_token)
    archive(conversation_db, archived)

    everything = listed(conversation_db)

    assert {entry["conversation"] for entry in everything} == {closed, archived}
    by_id = {entry["conversation"]: entry for entry in everything}
    assert by_id[closed]["title"] == "Check retries"
    assert by_id[closed]["archived"] is False
    assert by_id[closed]["messages"] == read(conversation_db, closed)["total_messages"]
    assert by_id[archived]["archived"] is True
    assert [entry["conversation"] for entry in listed(conversation_db, archived=True)] == [archived]
    assert [entry["conversation"] for entry in listed(conversation_db, archived=False)] == [closed]


def test_read_returns_the_record_to_a_chat_that_never_joined(conversation_db: Connection) -> None:
    """A read from cursor zero has the brief, roster and messages, also for an archived chat."""
    conversation, token = open_conversation(conversation_db, "Check retries")
    finding = post(conversation_db, token, "Retries pass.")
    close(conversation_db, token, "The retry check passed.")
    archive(conversation_db, conversation)

    page = read(conversation_db, conversation)

    assert page["brief"] == "Check retries"
    assert page["roster"] == {"Lead": "lead", "Worker": "worker"}
    assert page["closed"] is True
    assert page["archived"] is True
    bodies = [message["body"] for message in page["messages"]]
    assert "Retries pass." in bodies
    assert bodies[-1] == "The retry check passed."
    assert page["more"] is False

    later = read(conversation_db, conversation, after=finding)

    assert "brief" not in later
    assert "roster" not in later
    newer = bodies[bodies.index("Retries pass.") + 1 :]
    assert [message["body"] for message in later["messages"]] == newer


def test_read_pages_with_the_member_page_size(conversation_db: Connection) -> None:
    """A long record comes in pages that the returned cursor continues."""
    conversation, token = open_conversation(conversation_db, "Check retries")
    for number in range(history.PAGE_SIZE + 2):
        post(conversation_db, token, f"Finding {number}")
    close(conversation_db, token)

    first = read(conversation_db, conversation)
    rest = read(conversation_db, conversation, after=first["cursor"])

    assert len(first["messages"]) == history.PAGE_SIZE
    assert first["more"] is True
    assert rest["more"] is False
    assert len(first["messages"]) + len(rest["messages"]) == first["total_messages"]


def test_open_and_unknown_conversations_are_rejected_alike(conversation_db: Connection) -> None:
    """An open conversation stays private to its members, and reopening hides it again."""
    conversation, token = open_conversation(conversation_db, "Check retries")
    post(conversation_db, token, "Private while open.")

    with pytest.raises(ValueError, match=history.UNKNOWN_CLOSED_CONVERSATION):
        read(conversation_db, conversation)
    with pytest.raises(ValueError, match=history.UNKNOWN_CLOSED_CONVERSATION):
        read(conversation_db, token_hex(12))

    close(conversation_db, token)
    assert read(conversation_db, conversation)["closed"] is True
    repository.apply(
        conversation_db, models.Reopen(workspace=conversation_db.info["workspace"], token=token)
    )

    with pytest.raises(ValueError, match=history.UNKNOWN_CLOSED_CONVERSATION):
        read(conversation_db, conversation)
    assert listed(conversation_db) == []


def test_held_sealed_proposals_stay_out_of_the_closed_record(conversation_db: Connection) -> None:
    """A proposal that was never released is not readable after close."""
    workspace = conversation_db.info["workspace"]
    created = repository.apply(
        conversation_db,
        models.Create(
            workspace=workspace,
            brief="Pick a design",
            initiator="Lead",
            invitees=["Worker", "Silent"],
            sealed=True,
        ),
    )
    worker, _silent = (
        repository.apply(
            conversation_db, models.Join(workspace=workspace, code=created["invitations"][name])
        )
        for name in ("Worker", "Silent")
    )
    repository.apply(
        conversation_db,
        models.Post(
            workspace=workspace,
            token=worker["token"],
            body="Held secret proposal",
            kind="proposal",
            submission_id="proposal",
        ),
    )
    close(conversation_db, created["token"])

    page = read(conversation_db, created["conversation"])

    assert all("Held secret proposal" not in message["body"] for message in page["messages"])


@pytest.mark.parametrize("conversation", ["", "abc", "Z" * 24, "a" * 25, "../" + "a" * 21])
def test_malformed_conversation_ids_fail_validation(conversation: str) -> None:
    """Only a 24-character lowercase hex ID reaches the store."""
    with pytest.raises(ValidationError):
        models.ReadClosed(workspace="a" * 64, conversation=conversation)

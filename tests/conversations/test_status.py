"""Test the activity visible to authenticated conversation members."""

from secrets import token_hex

import pytest
from pydantic import ValidationError
from sqlalchemy import Connection

from agents_chat.conversations import models, repository
from agents_chat.storage.database import database

INVITEES = ["Worker", "Missing"]


def create_team(connection: Connection) -> dict:
    """Start a team with two worker invitations."""
    return repository.apply(
        connection,
        models.Create(
            workspace=connection.info["workspace"],
            brief="Check the retry behavior",
            initiator="Lead",
            invitees=INVITEES,
        ),
    )


def test_worker_sees_invited_joined_and_replied_members(
    conversation_db: Connection, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Count posted messages and expose activity to workers as well as leads."""
    monkeypatch.setattr(repository.time, "time", lambda: 1_700_000_000)
    workspace = conversation_db.info["workspace"]
    team = create_team(conversation_db)
    worker = repository.apply(
        conversation_db, models.Join(workspace=workspace, code=team["invitations"]["Worker"])
    )
    for body in ("The retry succeeded", "The original attempt timed out"):
        repository.apply(
            conversation_db,
            models.Post(workspace=workspace, token=team["token"], submission_id=body, body=body),
        )

    result = repository.apply(
        conversation_db, models.Status(workspace=workspace, token=worker["token"])
    )
    assert result == {
        "open": True,
        "members": {
            "Lead": {"role": "lead", "state": "replied", "idle_seconds": 0, "messages": 2},
            "Worker": {"role": "worker", "state": "joined", "idle_seconds": 0},
            "Missing": {"role": "worker", "state": "invited"},
        },
    }


def test_activity_is_updated_only_after_ten_seconds(
    conversation_db: Connection, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Rapid polls show elapsed time since the previous recorded call."""
    workspace = conversation_db.info["workspace"]
    team = create_team(conversation_db)
    request = models.Status(workspace=workspace, token=team["token"])
    now = 1_700_000_000
    monkeypatch.setattr(repository.time, "time", lambda: now)

    assert repository.apply(conversation_db, request)["members"]["Lead"]["idle_seconds"] == 0
    now += 9
    assert repository.apply(conversation_db, request)["members"]["Lead"]["idle_seconds"] == 9
    now += 2
    assert repository.apply(conversation_db, request)["members"]["Lead"]["idle_seconds"] == 0


def test_status_rejects_token_from_another_workspace(conversation_db: Connection) -> None:
    """A token issued by another database cannot access this workspace's status."""
    create_team(conversation_db)
    foreign_workspace = token_hex(32)
    engine = database(foreign_workspace)
    try:
        with engine.begin() as connection:
            connection.info["workspace"] = foreign_workspace
            foreign_team = create_team(connection)
    finally:
        engine.dispose()
    request = models.Status(
        workspace=conversation_db.info["workspace"], token=foreign_team["token"]
    )

    with pytest.raises(ValueError, match="Invalid participant token"):
        repository.apply(conversation_db, request)


def test_status_requires_a_token(conversation_db: Connection) -> None:
    """Reject requests without credentials at the core validation boundary."""
    with pytest.raises(ValidationError, match="token"):
        models.REQUEST.validate_python(
            {"action": "status", "workspace": conversation_db.info["workspace"]}
        )

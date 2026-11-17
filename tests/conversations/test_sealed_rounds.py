"""Exercise sealed rounds through conversation operations."""

import pytest
from pydantic import ValidationError
from sqlalchemy import Connection

from agents_chat.conversations import models, repository
from agents_chat.mcp.server import guidance


def create_round(connection: Connection, invitees: list[str]) -> dict:
    """Open a sealed round with a one-minute deadline."""
    return repository.apply(
        connection,
        models.Create(
            workspace=connection.info["workspace"],
            initiator="Lead",
            brief="Propose a test independently",
            invitees=invitees,
            sealed=True,
            deadline_minutes=1,
        ),
    )


def join(connection: Connection, team: dict, label: str) -> dict:
    """Redeem the named worker's invitation."""
    return repository.apply(
        connection,
        models.Join(workspace=connection.info["workspace"], code=team["invitations"][label]),
    )


def post(connection: Connection, token: str, key: str, body: str, **fields: object) -> dict:
    """Submit a validated post from the named token holder."""
    return repository.apply(
        connection,
        models.Post(
            workspace=connection.info["workspace"],
            token=token,
            submission_id=key,
            body=body,
            **fields,
        ),
    )


def read(connection: Connection, token: str, after: int = 0) -> dict:
    """Return the published transcript after a cursor."""
    return repository.apply(
        connection, models.Read(workspace=connection.info["workspace"], token=token, after=after)
    )


def test_last_joined_worker_releases_to_advanced_and_late_readers(
    conversation_db: Connection,
) -> None:
    """Hold proposals outside the transcript, then publish them with fresh message IDs."""
    team = create_round(conversation_db, ["First", "Second", "Late"])
    lead = team["token"]
    first = join(conversation_db, team, "First")["token"]
    second = join(conversation_db, team, "Second")["token"]
    held = post(conversation_db, first, "first", "Private first proposal", kind="proposal")
    assert "proposal_id" in held and "message_id" not in held
    advance = post(conversation_db, lead, "note", "Please prepare the critique.")
    assert [item["body"] for item in read(conversation_db, lead)["messages"]] == [
        "Please prepare the critique."
    ]
    cursor = read(conversation_db, second)["cursor"]
    assert cursor == advance["message_id"]
    assert read(conversation_db, second, cursor)["messages"] == []
    posted = post(conversation_db, second, "second", "Private second proposal", kind="proposal")
    result = read(conversation_db, second, cursor)
    assert result["round"] == "critique"
    first_message, second_message, event = result["messages"]
    assert first_message["id"] > cursor
    assert first_message["body"] == "Private first proposal"
    assert second_message["body"] == "Private second proposal"
    assert second_message["id"] == posted["message_id"]
    assert event["kind"] == "event" and "Late" in event["body"]
    late = join(conversation_db, team, "Late")
    assert late["round"] == "critique"
    assert read(conversation_db, late["token"])["messages"][-3:] == result["messages"]


def test_manual_release_permissions_and_proposal_retries(conversation_db: Connection) -> None:
    """Deduplicate private and public receipts and allow only leads to publish."""
    team = create_round(conversation_db, ["First", "Second"])
    lead = team["token"]
    first = join(conversation_db, team, "First")["token"]
    join(conversation_db, team, "Second")
    held = post(conversation_db, first, "first", "My proposal", kind="proposal")
    assert held == post(conversation_db, first, "first", "My proposal", kind="proposal")
    assert held == post(conversation_db, first, "new-key", "My proposal", kind="proposal")
    with pytest.raises(ValueError, match="different message"):
        post(conversation_db, first, "first", "My proposal")
    with pytest.raises(ValueError, match="already submitted"):
        post(conversation_db, first, "changed", "A changed proposal", kind="proposal")

    request = models.Release(workspace=conversation_db.info["workspace"], token=first)
    with pytest.raises(ValueError, match="Only a lead or co-lead"):
        repository.apply(conversation_db, request)
    with pytest.raises(ValueError, match="Invalid participant token"):
        repository.apply(conversation_db, request.model_copy(update={"token": "foreign-token"}))
    request = models.Release(workspace=conversation_db.info["workspace"], token=lead)
    assert repository.apply(conversation_db, request) == {"round": "critique"}
    assert repository.apply(conversation_db, request) == {"round": "critique"}
    visible = read(conversation_db, lead)["messages"]
    assert [item["kind"] for item in visible] == ["proposal", "event"]
    assert "Second" in visible[1]["body"]
    for key in ("first", "new-key", "fresh-retry"):
        assert post(conversation_db, first, key, "My proposal", kind="proposal") == {
            "message_id": visible[0]["id"]
        }
    assert read(conversation_db, lead)["messages"] == visible


@pytest.mark.parametrize("operation", ["read", "post"])
def test_deadline_releases_and_excludes_late_proposal(
    conversation_db: Connection, monkeypatch: pytest.MonkeyPatch, operation: str
) -> None:
    """Release pending work on the first read or post at the deadline."""
    current = 1000
    monkeypatch.setattr(repository.time, "time", lambda: current)
    team = create_round(conversation_db, ["First", "Late"])
    first = join(conversation_db, team, "First")["token"]
    late = join(conversation_db, team, "Late")["token"]
    assert team["round_deadline"] == 1060
    post(conversation_db, first, "first", "In time", kind="proposal")
    current += 59
    assert read(conversation_db, late)["messages"] == []
    current += 1
    if operation == "post":
        assert post(conversation_db, late, "late", "Too late", kind="proposal") == {
            "round": "critique",
            "accepted": False,
        }
    result = read(conversation_db, late)
    assert result["round"] == "critique"
    assert result["messages"][0]["body"] == "In time"
    assert "Late" in result["messages"][1]["body"]
    assert len(result["messages"]) == 2


def test_status_and_guidance_keep_submission_state_private(conversation_db: Connection) -> None:
    """Let workers check their own progress without revealing others' held work."""
    team = create_round(conversation_db, ["First", "Second"])
    first = join(conversation_db, team, "First")["token"]
    second = join(conversation_db, team, "Second")["token"]
    post(conversation_db, first, "first", "Held proposal", kind="proposal")
    workspace = conversation_db.info["workspace"]
    lead_status = repository.apply(
        conversation_db, models.Status(workspace=workspace, token=team["token"])
    )
    assert "proposal_submitted" not in lead_status
    assert lead_status["members"]["First"]["state"] == "joined"
    for name, token, expected in [("First", first, True), ("Second", second, False)]:
        status = repository.apply(conversation_db, models.Status(workspace=workspace, token=token))
        assert status["proposal_submitted"] is expected
        next_step = guidance(status, name, has_messages=False, silent_waits=3)
        expected_guidance = "wait for release" if expected else "kind='proposal'"
        assert expected_guidance in next_step
        assert "Held proposal" not in str(status)


def test_close_keeps_unreleased_proposals_out_of_final_history(conversation_db: Connection) -> None:
    """Close with an outcome without publishing a round that did not finish."""
    team = create_round(conversation_db, ["First", "Second"])
    first = join(conversation_db, team, "First")["token"]
    join(conversation_db, team, "Second")
    post(conversation_db, first, "first", "Unreleased work", kind="proposal")
    repository.apply(
        conversation_db,
        models.Close(
            workspace=conversation_db.info["workspace"],
            token=team["token"],
            summary="Stopped before critique",
        ),
    )
    result = read(conversation_db, first)
    assert result["closed"]
    assert [item["body"] for item in result["messages"]] == ["Stopped before critique"]


def test_proposals_require_worker_and_no_address(conversation_db: Connection) -> None:
    """Reject attempts to send a private proposal to a particular reader."""
    team = create_round(conversation_db, ["First", "Second"])
    first = join(conversation_db, team, "First")["token"]
    with pytest.raises(ValueError, match="Only workers"):
        post(conversation_db, team["token"], "key", "A lead proposal", kind="proposal")
    with pytest.raises(ValueError, match="cannot have an addressee"):
        post(conversation_db, first, "key", "An addressed proposal", kind="proposal", to="Lead")


@pytest.mark.parametrize("sealed, minutes", [(False, 5), (True, 0), (True, 61)])
def test_create_validates_sealed_deadline(sealed: bool, minutes: int) -> None:
    """Accept only bounded deadlines on sealed conversations."""
    with pytest.raises(ValidationError):
        models.Create(
            workspace="0" * 64,
            initiator="Lead",
            brief="Test",
            invitees=["Worker"],
            sealed=sealed,
            deadline_minutes=minutes,
        )

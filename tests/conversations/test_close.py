"""Verify the final outcome and access rules of closed conversations."""

import asyncio
import json
from secrets import token_urlsafe
from typing import Any

import pytest
from pydantic import ValidationError
from sqlalchemy import Connection

from agents_chat.conversations import models, repository
from agents_chat.mcp import server

SUMMARY = "The retry check passed."


@pytest.fixture
def team(conversation_db: Connection) -> tuple[str, str, str]:
    """Open one lead token, one worker token and one unused invitation."""
    workspace = conversation_db.info["workspace"]
    created = repository.apply(
        conversation_db,
        models.Create(
            workspace=workspace,
            brief="Check retries",
            initiator="Lead",
            invitees=["Worker", "Late"],
        ),
    )
    worker = repository.apply(
        conversation_db, models.Join(workspace=workspace, code=created["invitations"]["Worker"])
    )
    return created["token"], worker["token"], created["invitations"]["Late"]


def test_close_records_the_last_message_once(
    conversation_db: Connection, team: tuple[str, str, str]
) -> None:
    """Read a distinct summary after the last post and preserve it on a close retry."""
    workspace = conversation_db.info["workspace"]
    lead_token, worker_token, _ = team
    prior = repository.apply(
        conversation_db,
        models.Post(workspace=workspace, token=lead_token, body=SUMMARY, submission_id="prior"),
    )
    assert repository.apply(
        conversation_db, models.Close(workspace=workspace, token=lead_token, summary=SUMMARY)
    ) == {"closed": True}
    assert repository.apply(
        conversation_db,
        models.Close(workspace=workspace, token=lead_token, summary="Retry outcome"),
    ) == {"closed": True}

    page = repository.apply(
        conversation_db,
        models.Read(workspace=workspace, token=worker_token, after=prior["message_id"]),
    )

    assert page["closed"] is True
    assert page["messages"] == [{"id": page["cursor"], "label": "Lead", "body": SUMMARY}]


def test_closed_conversation_rejects_writes_and_join(
    conversation_db: Connection, team: tuple[str, str, str]
) -> None:
    """Reject new posts, worker close calls, and unused invitation codes after close."""
    workspace = conversation_db.info["workspace"]
    lead_token, worker_token, unused_code = team
    repository.apply(conversation_db, models.Close(workspace=workspace, token=lead_token))

    with pytest.raises(ValueError, match="Conversation is closed"):
        repository.apply(
            conversation_db,
            models.Post(
                workspace=workspace, token=lead_token, body="Late post", submission_id="late"
            ),
        )
    with pytest.raises(ValueError, match="Only a lead or co-lead"):
        repository.apply(conversation_db, models.Close(workspace=workspace, token=worker_token))
    with pytest.raises(ValueError, match="Invalid or expired invitation"):
        repository.apply(conversation_db, models.Join(workspace=workspace, code=unused_code))


def test_closed_conversation_requires_a_valid_token(
    conversation_db: Connection, team: tuple[str, str, str]
) -> None:
    """Preserve authentication for closed records and validate close inputs."""
    workspace = conversation_db.info["workspace"]
    lead_token, _, _ = team
    repository.apply(conversation_db, models.Close(workspace=workspace, token=lead_token))

    with pytest.raises(ValueError, match="Invalid participant token"):
        repository.apply(conversation_db, models.Read(workspace=workspace, token=token_urlsafe(32)))
    with pytest.raises(ValidationError):
        models.REQUEST.validate_python({"action": "close", "workspace": workspace})
    with pytest.raises(ValidationError):
        models.Close(workspace=workspace, token=lead_token, summary=" ")


@pytest.mark.parametrize("summary", [None, SUMMARY])
def test_waiting_read_stops_on_close(
    conversation_db: Connection,
    team: tuple[str, str, str],
    monkeypatch: pytest.MonkeyPatch,
    summary: str | None,
) -> None:
    """Poll until close and return the outcome without another wait or a settle delay."""
    workspace = conversation_db.info["workspace"]
    lead_token, worker_token, _ = team
    poll_count = 0

    def exchange(request: models.Request) -> dict[str, Any]:
        """Close between the first and second reads on the same test connection."""
        nonlocal poll_count
        poll_count += 1
        assert poll_count <= 2
        if poll_count == 2:
            repository.apply(
                conversation_db,
                models.Close(workspace=workspace, token=lead_token, summary=summary),
            )
        return repository.apply(conversation_db, request)

    def invoke(app: server.FastMCP, transport: str) -> None:
        """Exercise the registered tool with the real FastMCP argument and result handling."""
        assert transport == "stdio"
        content = asyncio.run(
            app.call_tool("read_messages", {"token": worker_token, "wait_seconds": 15})
        )
        page = json.loads(content[0].text)
        assert page["closed"] is True
        assert [message["body"] for message in page["messages"]] == ([summary] if summary else [])
        assert "Stop listening and report the outcome" in page["next"]

    monkeypatch.setattr(server, "exchange", exchange)
    monkeypatch.setattr(server.FastMCP, "run", invoke)
    server.serve(workspace)
    assert poll_count == 2

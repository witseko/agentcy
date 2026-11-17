"""Verify task state, authorization, handoffs, and lead guidance."""

from typing import Any

import pytest
from sqlalchemy import Connection

from agents_chat.conversations import models, repository
from agents_chat.mcp import server


@pytest.fixture
def task_team(conversation_db: Connection) -> dict[str, str]:
    """Start a conversation and join two workers."""
    workspace = conversation_db.info["workspace"]
    team = repository.apply(
        conversation_db,
        models.Create(
            workspace=workspace,
            initiator="Lead",
            brief="Review retries",
            invitees=["Alpha", "Bravo"],
        ),
    )
    tokens = {"Lead": team["token"]}
    for name, code in team["invitations"].items():
        member = repository.apply(conversation_db, models.Join(workspace=workspace, code=code))
        tokens[name] = member["token"]
    return tokens


def post(
    connection: Connection, token: str, body: str, kind: models.PostKind, **fields: Any
) -> int:
    """Post through the validated request and return the recorded ID."""
    return repository.apply(
        connection,
        models.Post(
            workspace=connection.info["workspace"],
            token=token,
            body=body,
            submission_id=body,
            kind=kind,
            **fields,
        ),
    )["message_id"]


def team_status(connection: Connection, token: str) -> dict:
    """Read the status response for the current conversation."""
    return repository.apply(
        connection, models.Status(workspace=connection.info["workspace"], token=token)
    )


def test_only_owner_can_finish_task(conversation_db: Connection, task_team: dict[str, str]) -> None:
    """An unrelated member's completion cannot close the owner's task."""
    lead, alpha, bravo = (task_team[name] for name in ("Lead", "Alpha", "Bravo"))
    task = post(conversation_db, lead, "Review retry limits", "task", to="Alpha")
    post(conversation_db, bravo, "Done with my review", "done", reply_to=task)
    assert team_status(conversation_db, lead)["members"]["Alpha"]["open_tasks"] == [{"id": task}]

    post(conversation_db, alpha, "Retry limits checked", "done", reply_to=task)
    assert "open_tasks" not in team_status(conversation_db, lead)["members"]["Alpha"]


def test_task_requires_lead_and_addressee(
    conversation_db: Connection, task_team: dict[str, str]
) -> None:
    """Reject unauthorized or unaddressed assignments."""
    with pytest.raises(ValueError, match="Only a lead or co-lead"):
        post(conversation_db, task_team["Alpha"], "Unauthorized task", "task", to="Bravo")
    with pytest.raises(ValueError, match="requires an addressee"):
        post(conversation_db, task_team["Lead"], "No recipient", "task")


def test_guidance_names_only_open_tasks(
    conversation_db: Connection, task_team: dict[str, str]
) -> None:
    """List outstanding work even if the owner previously reported completion."""
    lead, alpha, bravo = (task_team[name] for name in ("Lead", "Alpha", "Bravo"))
    first = post(conversation_db, lead, "Review retry limits", "task", to="Alpha")
    second = post(conversation_db, lead, "Review retry timing", "task", to="Bravo")
    post(conversation_db, bravo, "Finished triage", "done")
    post(conversation_db, alpha, "Retry limits checked", "done", reply_to=first)

    status = team_status(conversation_db, lead)
    assert status["members"]["Bravo"]["state"] == "done"
    page = server.finish_wait({"messages": []}, status, {}, "lead")
    assert page["waiting_on"] == ["Bravo"]
    assert f"#{second} for Bravo" in page["next"]
    assert f"#{first} for Alpha" not in page["next"]
    assert "keep listening" in page["next"]

    post(conversation_db, bravo, "Timing data missing", "blocker", reply_to=second)
    blocked = team_status(conversation_db, lead)
    assert blocked["members"]["Bravo"]["open_tasks"] == [{"id": second, "blocked": True}]
    next_step = server.guidance(blocked, "Lead", has_messages=False, silent_waits=1)
    assert f"#{second} for Bravo (blocked)" in next_step
    assert "Stop listening" in next_step


def test_accept_transfers_task_and_preserves_retries(
    conversation_db: Connection, task_team: dict[str, str]
) -> None:
    """Transfer ownership only on the designated receiver's acceptance."""
    lead, alpha, bravo = (task_team[name] for name in ("Lead", "Alpha", "Bravo"))
    task = post(conversation_db, lead, "Review retry limits", "task", to="Alpha")
    post(conversation_db, alpha, "I need a fixture", "blocker", reply_to=task)
    with pytest.raises(ValueError, match="Only the task owner"):
        post(conversation_db, lead, "Try a handoff", "handoff", to="Bravo", reply_to=task)
    offer = post(conversation_db, alpha, "Please finish", "handoff", to="Bravo", reply_to=task)
    with pytest.raises(ValueError, match="already has a pending handoff"):
        post(conversation_db, alpha, "Another offer", "handoff", to="Bravo", reply_to=task)
    with pytest.raises(ValueError, match="addressed to you"):
        post(conversation_db, lead, "I accept", "accept", reply_to=offer)

    accepted = post(conversation_db, bravo, "I can finish", "accept", reply_to=offer)
    assert post(conversation_db, bravo, "I can finish", "accept", reply_to=offer) == accepted
    assert (
        post(conversation_db, alpha, "Please finish", "handoff", to="Bravo", reply_to=task) == offer
    )
    post(conversation_db, alpha, "I finished anyway", "done", reply_to=task)
    members = team_status(conversation_db, lead)["members"]
    assert "open_tasks" not in members["Alpha"]
    assert members["Bravo"]["open_tasks"] == [{"id": task}]

    post(conversation_db, bravo, "Retry limits checked", "done", reply_to=task)
    assert "open_tasks" not in team_status(conversation_db, lead)["members"]["Bravo"]


def test_decline_leaves_owner_and_close_expires_offer(
    conversation_db: Connection, task_team: dict[str, str]
) -> None:
    """A declined or stale offer cannot transfer the task."""
    lead, alpha, bravo = (task_team[name] for name in ("Lead", "Alpha", "Bravo"))
    task = post(conversation_db, lead, "Review retry limits", "task", to="Alpha")
    offer = post(conversation_db, alpha, "Please review", "handoff", to="Bravo", reply_to=task)
    post(conversation_db, bravo, "I cannot take it", "decline", reply_to=offer)
    assert team_status(conversation_db, lead)["members"]["Alpha"]["open_tasks"] == [{"id": task}]
    with pytest.raises(ValueError, match="no longer pending"):
        post(conversation_db, bravo, "An old acceptance", "accept", reply_to=offer)

    new_offer = post(
        conversation_db, alpha, "Can you take over?", "handoff", to="Bravo", reply_to=task
    )
    post(conversation_db, alpha, "Retry limits checked", "done", reply_to=task)
    with pytest.raises(ValueError, match="no longer pending"):
        post(conversation_db, bravo, "A late acceptance", "accept", reply_to=new_offer)

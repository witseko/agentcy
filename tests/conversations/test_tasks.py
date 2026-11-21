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


def test_resume_clears_only_its_task_and_deduplicates_retries(
    conversation_db: Connection, task_team: dict[str, str]
) -> None:
    """Keep other blockers and expose the owner's renewed activity after the last clears."""
    lead, alpha, bravo = (task_team[name] for name in ("Lead", "Alpha", "Bravo"))
    first = post(conversation_db, lead, "Review retry limits", "task", to="Alpha")
    second = post(conversation_db, lead, "Review retry timing", "task", to="Alpha")
    blocker = post(conversation_db, alpha, "I need a fixture", "blocker", reply_to=first)
    post(conversation_db, alpha, "I need timing data", "blocker", reply_to=second)
    post(conversation_db, bravo, "Here is the fixture", "finding", to="Alpha", reply_to=blocker)

    resume = models.Post(
        workspace=conversation_db.info["workspace"],
        token=alpha,
        body="The fixture lets me continue",
        submission_id="resume-first",
        kind="resume",
        reply_to=first,
    )
    result = repository.apply(conversation_db, resume)
    assert repository.apply(conversation_db, resume) == result
    assert (
        repository.apply(
            conversation_db, resume.model_copy(update={"submission_id": "retry-first"})
        )
        == result
    )
    owner = team_status(conversation_db, lead)["members"]["Alpha"]
    assert owner["open_tasks"] == [{"id": first}, {"id": second, "blocked": True}]
    assert owner["state"] == "blocked"
    assert owner["messages"] == 3

    post(conversation_db, alpha, "I recovered the timing data", "resume", reply_to=second)
    owner = team_status(conversation_db, lead)["members"]["Alpha"]
    assert owner["open_tasks"] == [{"id": first}, {"id": second}]
    assert owner["state"] == "replied"


def test_task_can_block_and_resume_again_with_the_same_text(
    conversation_db: Connection, task_team: dict[str, str]
) -> None:
    """Repeat both state changes without treating a new cycle as an earlier submission."""
    lead, owner = (task_team[name] for name in ("Lead", "Alpha"))
    task = post(conversation_db, lead, "Review retry limits", "task", to="Alpha")
    state_changes = (
        ("blocker", "Need a fixture", True),
        ("resume", "Proceeding with the fixture", False),
    )
    previous_ids = {}
    for cycle in (1, 2):
        for kind, body, blocked in state_changes:
            request = models.Post(
                workspace=conversation_db.info["workspace"],
                token=owner,
                submission_id=f"{kind}-{cycle}",
                body=body,
                kind=kind,
                reply_to=task,
            )
            result = repository.apply(conversation_db, request)
            assert result["message_id"] != previous_ids.get(kind)
            assert repository.apply(conversation_db, request) == result
            previous_ids[kind] = result["message_id"]
            status = team_status(conversation_db, lead)["members"]["Alpha"]
            assert status["open_tasks"][0].get("blocked", False) is blocked


def test_resume_requires_current_owner_and_original_blocked_task(
    conversation_db: Connection, task_team: dict[str, str]
) -> None:
    """Reject a resume on an invalid target or a blocked task owned by someone else."""
    lead, alpha, bravo = (task_team[name] for name in ("Lead", "Alpha", "Bravo"))
    task = post(conversation_db, lead, "Review retry limits", "task", to="Alpha")
    with pytest.raises(ValueError, match="original open task"):
        post(conversation_db, alpha, "No target", "resume")
    with pytest.raises(ValueError, match="task is not blocked"):
        post(conversation_db, alpha, "Already open", "resume", reply_to=task)

    blocker = post(conversation_db, alpha, "I need a fixture", "blocker", reply_to=task)
    with pytest.raises(ValueError, match="original open task"):
        post(conversation_db, alpha, "Wrong reply target", "resume", reply_to=blocker)
    with pytest.raises(ValueError, match="Only the task owner"):
        post(conversation_db, bravo, "I resolved it for Alpha", "resume", reply_to=task)

    post(conversation_db, alpha, "Retry limits checked", "done", reply_to=task)
    with pytest.raises(ValueError, match="original open task"):
        post(conversation_db, alpha, "Already closed", "resume", reply_to=task)


def test_resume_leaves_pending_handoff_in_place(
    conversation_db: Connection, task_team: dict[str, str]
) -> None:
    """Keep the existing offer so its receiver can still take over the task."""
    lead, alpha, bravo = (task_team[name] for name in ("Lead", "Alpha", "Bravo"))
    task = post(conversation_db, lead, "Review retry limits", "task", to="Alpha")
    post(conversation_db, alpha, "I need a fixture", "blocker", reply_to=task)
    offer = post(conversation_db, alpha, "Please finish", "handoff", to="Bravo", reply_to=task)
    post(conversation_db, alpha, "I found the fixture", "resume", reply_to=task)

    accepted = post(conversation_db, bravo, "I can finish", "accept", reply_to=offer)
    with pytest.raises(ValueError, match="original open task"):
        post(conversation_db, bravo, "Wrong reply target", "resume", reply_to=accepted)
    post(conversation_db, bravo, "I need the test command", "blocker", reply_to=task)
    with pytest.raises(ValueError, match="Only the task owner"):
        post(conversation_db, alpha, "Late update", "resume", reply_to=task)
    post(conversation_db, bravo, "I found the test command", "resume", reply_to=task)
    assert team_status(conversation_db, lead)["members"]["Bravo"]["open_tasks"] == [{"id": task}]


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
    assert status["members"]["Bravo"]["state"] == "replied"
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
    assert "If a teammate is checking a blocker" in next_step
    expired = server.guidance(
        blocked, "Lead", has_messages=False, silent_waits=server.LEAD_IDLE_WAITS
    )
    assert "Stop listening" in expired


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


def test_followup_task_clears_done_state(
    conversation_db: Connection, task_team: dict[str, str]
) -> None:
    """Assigning a new task to a done worker marks them active until that task finishes."""
    lead, alpha = task_team["Lead"], task_team["Alpha"]
    first = post(conversation_db, lead, "Check retries", "task", to="Alpha")
    post(conversation_db, alpha, "Retries checked", "done", reply_to=first)
    assert team_status(conversation_db, lead)["members"]["Alpha"]["state"] == "done"

    second = post(conversation_db, lead, "Check timeouts", "task", to="Alpha")
    reassigned = team_status(conversation_db, lead)["members"]["Alpha"]
    assert reassigned["state"] == "replied"
    assert reassigned["open_tasks"] == [{"id": second}]

    post(conversation_db, alpha, "Timeouts checked", "done", reply_to=second)
    finished = team_status(conversation_db, lead)["members"]["Alpha"]
    assert finished["state"] == "done"
    assert "open_tasks" not in finished

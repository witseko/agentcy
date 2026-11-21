"""Test late invitations, private join prompts, and the roster limit."""

from pathlib import Path
from secrets import token_urlsafe
from types import SimpleNamespace
from typing import Any

import pytest
from pydantic import ValidationError
from sqlalchemy import Connection, update

from agents_chat.conversations import models, repository
from agents_chat.conversations.tables import member
from agents_chat.storage.database import database, workspace_key


def apply(connection: Connection, action: str, **fields: Any) -> dict:
    """Send a validated conversation request."""
    request = models.REQUEST.validate_python(
        {"workspace": connection.info["workspace"], "action": action, **fields}
    )
    return repository.apply(connection, request)


def create(connection: Connection, invitees: list | None = None) -> dict:
    """Create a room with one worker unless more slots are requested."""
    return apply(
        connection, "create", initiator="Lead", brief="Review", invitees=invitees or ["Worker"]
    )


@pytest.mark.parametrize("late_invite", [False, True], ids=["create", "late-invite"])
def test_join_reports_when_clients_use_different_workspaces(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, late_invite: bool
) -> None:
    """Identify a mismatched workspace so the worker can join with the same code."""
    bridge = tmp_path / "bridge"
    bridge.mkdir(mode=0o700)
    monkeypatch.setenv("AGENTS_CHAT_HOME", str(bridge))
    workspaces = [tmp_path / "lead", tmp_path / "worker"]
    for directory in workspaces:
        directory.mkdir()
    lead_workspace, worker_workspace = map(workspace_key, workspaces)
    lead_db = database(lead_workspace)
    worker_db = database(worker_workspace)
    try:
        with lead_db.begin() as lead, worker_db.begin() as worker:
            lead.info["workspace"] = lead_workspace
            worker.info["workspace"] = worker_workspace
            team = create(lead)
            code = team["invitations"]["Worker"]
            name = "Worker"
            if late_invite:
                name = "Analyst"
                code = apply(lead, "invite", token=team["token"], name=name)["code"]

            with pytest.raises(
                ValueError, match="Join code does not match this workspace"
            ) as error:
                apply(worker, "join", code=code)
            assert code not in str(error.value)
            assert apply(lead, "join", code=code)["participant"] == name
    finally:
        lead_db.dispose()
        worker_db.dispose()


def test_existing_unprefixed_join_code_still_works(conversation_db: Connection) -> None:
    """Redeem an invitation that an earlier core version issued."""
    create(conversation_db)
    code = token_urlsafe(16)
    conversation_db.execute(
        update(member).where(member.c.label == "Worker").values(invite_hash=repository.digest(code))
    )
    assert apply(conversation_db, "join", code=code)["participant"] == "Worker"


def test_late_invite_returns_a_prompt_and_the_joiner_reads_full_history(
    conversation_db: Connection,
) -> None:
    """A new worker can join from the template and catch up on prior messages."""
    team = create(conversation_db)
    lead = team["token"]
    original = apply(conversation_db, "post", token=lead, body="First finding", submission_id="1")
    invitation = apply(conversation_db, "invite", token=lead, name="Analyst")

    assert invitation["participant"] == "Analyst"
    assert invitation["role"] == "worker"
    template = team["join_prompt"]
    assert template == f"Review: {repository.JOIN_PROMPT}"
    assert invitation["join_prompt"] == f"Review (Analyst): {repository.JOIN_PROMPT}"
    assert template.count("CODE") == 1
    assert invitation["join_prompt"].count("CODE") == 1
    assert team["invitations"]["Worker"] not in template
    assert invitation["code"] not in template
    assert invitation["code"] not in invitation["join_prompt"]

    joined = apply(conversation_db, "join", code=invitation["code"])
    read = apply(conversation_db, "read", token=joined["token"])
    first, event = read["messages"]
    assert first["id"] == original["message_id"]
    assert event["kind"] == "event"
    assert "Analyst" in event["body"]
    assert invitation["code"] not in event["body"]
    assert read["roster"] == {"Lead": "lead", "Worker": "worker", "Analyst": "worker"}


@pytest.mark.parametrize("elapsed", [60, 3600], ids=["live-code", "expired-code"])
def test_reissue_replaces_the_old_code_and_renews_expiry(
    conversation_db: Connection, monkeypatch: pytest.MonkeyPatch, elapsed: int
) -> None:
    """An unused slot can have its code replaced before or after expiry."""
    clock = SimpleNamespace(now=10_000)
    monkeypatch.setattr(repository, "time", SimpleNamespace(time=lambda: clock.now))
    team = create(conversation_db)
    old = team["invitations"]["Worker"]
    clock.now += elapsed
    renewed = apply(conversation_db, "invite", token=team["token"], name="worker")
    assert renewed["participant"] == "Worker"
    assert renewed["code"] != old

    with pytest.raises(ValueError, match="Invalid or expired invitation"):
        apply(conversation_db, "join", code=old)

    clock.now += 3599
    assert apply(conversation_db, "join", code=renewed["code"])["participant"] == "Worker"


def test_joined_slot_cannot_be_reissued_and_workers_cannot_invite(
    conversation_db: Connection,
) -> None:
    """Protect a joined worker's slot and limit invitation changes to leads."""
    team = create(conversation_db)
    worker = apply(conversation_db, "join", code=team["invitations"]["Worker"])
    with pytest.raises(ValueError, match="Only a lead or co-lead"):
        apply(conversation_db, "invite", token=worker["token"], name="More")
    with pytest.raises(ValueError, match="Invitation slot already taken: Worker"):
        apply(conversation_db, "invite", token=team["token"], name="wOrKeR")


def test_late_co_lead_can_invite_and_reissue_preserves_the_role(
    conversation_db: Connection,
) -> None:
    """Share leadership with a new co-lead and preserve it when replacing the code."""
    team = create(conversation_db)
    apply(conversation_db, "invite", token=team["token"], name="Partner", role="co-lead")
    renewed = apply(conversation_db, "invite", token=team["token"], name="PARTNER")
    assert renewed["role"] == "co-lead"
    partner = apply(conversation_db, "join", code=renewed["code"])
    assert partner["role"] == "co-lead"
    assert partner["roster"]["Lead"] == "co-lead"
    delegated = apply(conversation_db, "invite", token=partner["token"], name="Analyst")
    assert delegated["role"] == "worker"


def test_cap_rejects_new_slot_and_allows_reissue(conversation_db: Connection) -> None:
    """Preserve the participant cap while keeping existing slots usable."""
    team = create(conversation_db, [f"Worker {index}" for index in range(16)])
    with pytest.raises(ValueError, match="already has 17 participants"):
        apply(conversation_db, "invite", token=team["token"], name="Another")
    renewed = apply(conversation_db, "invite", token=team["token"], name="Worker 0")
    assert renewed["code"] != team["invitations"]["Worker 0"]


def test_invite_requires_an_open_room_and_a_valid_token(conversation_db: Connection) -> None:
    """Closing prevents reissues, and an invalid token cannot create a slot."""
    team = create(conversation_db)
    with pytest.raises(ValueError, match="Invalid participant token"):
        apply(conversation_db, "invite", token=token_urlsafe(32), name="More")
    apply(conversation_db, "close", token=team["token"])
    with pytest.raises(ValueError, match="Conversation is closed"):
        apply(conversation_db, "invite", token=team["token"], name="Worker")


@pytest.mark.parametrize("fields", [{"name": " "}, {"name": "Valid", "role": "lead"}])
def test_invite_rejects_invalid_name_or_role(conversation_db: Connection, fields: dict) -> None:
    """Validate slot names and invitation roles at the protocol boundary."""
    team = create(conversation_db)
    with pytest.raises(ValidationError):
        apply(conversation_db, "invite", token=team["token"], **fields)


def test_join_prompt_truncates_multiline_briefs_and_keeps_code_placeholder(
    conversation_db: Connection,
) -> None:
    """Derive a concise single-line session name from the first line of the brief."""
    first_line = "x" * (repository.TITLE_LENGTH + 10)
    short = "x" * (repository.TITLE_LENGTH - 3) + "..."
    team = apply(
        conversation_db,
        "create",
        initiator="Lead",
        brief=f"{first_line}\nSecond line stays out of the title.",
        invitees=["Worker"],
    )
    assert team["join_prompt"] == f"{short}: {repository.JOIN_PROMPT}"
    invitation = apply(conversation_db, "invite", token=team["token"], name="Analyst")
    assert invitation["join_prompt"] == f"{short} (Analyst): {repository.JOIN_PROMPT}"

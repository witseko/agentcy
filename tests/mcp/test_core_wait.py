"""Exercise a waiting core request alongside independent SQLite transactions."""

import time
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from secrets import token_hex
from threading import Event

import pytest
from sqlalchemy import update

from agentcy.conversations import models
from agentcy.conversations.tables import room
from agentcy.mcp import concurrency
from agentcy.mcp.core import Core


def test_waiting_read_releases_database_and_collects_a_burst(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    """Posts from another request wake a listener and settle within its wait budget."""
    root = tmp_path / "bridge"
    root.mkdir(mode=0o700)
    monkeypatch.setenv("AGENTCY_HOME", str(root))
    monkeypatch.setattr(concurrency, "SETTLE_SECONDS", 0.08)
    core = Core.__new__(Core)
    core.notifications = concurrency.Notifications()
    workspace = token_hex(32)
    created = core.apply(
        models.Create(workspace=workspace, brief="Review", initiator="Lead", invitees=["Worker"])
    )
    worker = core.apply(models.Join(workspace=workspace, code=created["invitations"]["Worker"]))
    read = concurrency.read_page
    waiting = Event()

    def first_read(request: models.Read) -> dict:
        """Notify the test once the first page was loaded under the room condition."""
        page = read(request)
        waiting.set()
        return page

    monkeypatch.setattr(concurrency, "read_page", first_read)
    with ThreadPoolExecutor(max_workers=1) as executor:
        listener = executor.submit(
            core.apply, models.Read(workspace=workspace, token=worker["token"], wait_seconds=10)
        )
        assert waiting.wait(timeout=1)
        for text in ("One", "Two"):
            core.apply(
                models.Post(
                    workspace=workspace, token=created["token"], body=text, submission_id=text
                )
            )
        page = listener.result(timeout=2)

    assert [entry["body"] for entry in page["messages"]] == ["One", "Two"]


def test_waiting_read_releases_sealed_proposals_on_deadline(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    """Wake a sealed read at its deadline without an incoming bridge request."""
    root = tmp_path / "bridge"
    root.mkdir(mode=0o700)
    monkeypatch.setenv("AGENTCY_HOME", str(root))
    monkeypatch.setattr(concurrency, "SETTLE_SECONDS", 0.05)
    core = Core.__new__(Core)
    core.notifications = concurrency.Notifications()
    workspace = token_hex(32)
    created = core.apply(
        models.Create(
            workspace=workspace,
            brief="Independent review",
            initiator="Lead",
            invitees=["Worker", "Peer"],
            sealed=True,
        )
    )
    worker = core.apply(models.Join(workspace=workspace, code=created["invitations"]["Worker"]))
    core.apply(models.Join(workspace=workspace, code=created["invitations"]["Peer"]))
    core.apply(
        models.Post(
            workspace=workspace,
            token=worker["token"],
            body="Independent idea",
            submission_id="idea",
            kind="proposal",
        )
    )
    with concurrency.transaction(workspace) as connection:
        connection.execute(update(room).values(round_deadline=int(time.time()) + 1))

    with ThreadPoolExecutor(max_workers=1) as executor:
        listener = executor.submit(
            core.apply, models.Read(workspace=workspace, token=created["token"], wait_seconds=8)
        )
        page = listener.result(timeout=2)

    assert page["round"] == "critique"
    assert any(entry["body"] == "Independent idea" for entry in page["messages"])

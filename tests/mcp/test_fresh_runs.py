"""Verify durable launch, reconciliation, cancellation and failure through MCP."""

import json
import sys
import time
from dataclasses import dataclass
from pathlib import Path

import pytest
from sqlalchemy import select, update

from agents_chat.conversations import models
from agents_chat.conversations.assignment_tables import assignment
from agents_chat.mcp.assignment_worker import AssignmentWorker
from agents_chat.mcp.concurrency import Notifications, transaction
from agents_chat.mcp.core import Core
from agents_chat.storage.database import workspace_key

FIXTURE = Path(__file__).parents[1] / "fixtures" / "fake_run_plugin.py"


@dataclass(frozen=True)
class Team:
    """Store local test setup for a joined conversation."""

    core: Core
    directory: Path
    workspace: str
    lead: str
    worker: str


@pytest.fixture
def team(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> Team:
    """Start a conversation and register one fixture executable in private config."""
    root = tmp_path / "bridge"
    root.mkdir(mode=0o700)
    config = root / "plugins.json"
    config.write_text(
        json.dumps(
            {
                "plugins": {
                    "fixture": {
                        "command": [sys.executable, str(FIXTURE)],
                        "model": "test/model",
                        "timeout_seconds": 120,
                        "environment": ["TEST_FRESH_RESULT"],
                    },
                },
            }
        ),
        encoding="utf-8",
    )
    config.chmod(0o600)
    monkeypatch.setenv("AGENTS_CHAT_HOME", str(root))
    directory = tmp_path / "workspace"
    directory.mkdir()
    workspace = workspace_key(directory)
    core = Core.__new__(Core)
    core.notifications = Notifications()
    core.worker = AssignmentWorker(core.notifications)
    core.worker.ready.set()
    created = core.apply(
        models.Create(workspace=workspace, brief="Work", initiator="Lead", invitees=["Worker"])
    )
    worker = core.apply(models.Join(workspace=workspace, code=created["invitations"]["Worker"]))
    return Team(core, directory, workspace, created["token"], worker["token"])


def launch(team: Team, token: str | None = None) -> dict:
    """Request a new worker using the configured fixture plugin."""
    return team.core.apply(
        models.Launch(
            workspace=team.workspace,
            token=token or team.lead,
            name="Fresh",
            plugin="fixture",
            directory=str(team.directory),
        )
    )


def state(team: Team) -> str:
    """Read the state that a restarted process will reconcile."""
    with transaction(team.workspace) as connection:
        return connection.scalars(select(assignment.c.state)).one()


def dispatch(team: Team) -> None:
    """Take one committed launch and send it through the real plugin client."""
    work = team.core.worker.jobs.get_nowait()
    assert work is not None
    team.core.worker.launch(work)


def test_fresh_worker_survives_restart_and_stops_on_close(team: Team) -> None:
    """Inspect the same ID after restart and cancel via a new provider process on close."""
    result = launch(team)
    assert launch(team) == result
    assert state(team) == "queued"
    dispatch(team)
    assert state(team) == "running"

    restarted = AssignmentWorker(team.core.notifications)
    restarted.scan(restart=True)
    assert state(team) == "running"
    team.core.apply(models.Close(workspace=team.workspace, token=team.lead))
    restarted.scan(restart=True)
    assert state(team) == "cancelled"
    page = team.core.apply(models.Read(workspace=team.workspace, token=team.lead))
    assert any("cancelled" in entry["body"] for entry in page["messages"])


@pytest.mark.parametrize(
    ("outcome", "text"),
    [
        ("failed", "could not complete the fresh run"),
        ("unknown", "uncertain fresh-run state"),
    ],
)
def test_dispatch_failure_is_visible_and_reissue_is_safe(
    team: Team, monkeypatch: pytest.MonkeyPatch, outcome: str, text: str
) -> None:
    """Show the failure to the lead and preserve the original ID on retries."""
    monkeypatch.setenv("TEST_FRESH_RESULT", outcome)
    result = launch(team)
    dispatch(team)
    assert state(team) == outcome
    retry = launch(team)
    assert retry == {"assignment_id": result["assignment_id"], "state": outcome}
    assert team.core.worker.jobs.empty()
    page = team.core.apply(models.Read(workspace=team.workspace, token=team.lead))
    assert any(entry.get("kind") == "event" and text in entry["body"] for entry in page["messages"])


def test_expired_running_assignment_cancels_provider(team: Team) -> None:
    """Cancel a provider run after its deadline and retain the timeout outcome."""
    launch(team)
    dispatch(team)
    with transaction(team.workspace) as connection:
        connection.execute(update(assignment).values(deadline=int(time.time()) - 1))

    team.core.worker.scan(restart=True)

    assert state(team) == "timed_out"


def test_launch_is_lead_only(team: Team) -> None:
    """Reject an authenticated worker using the lead-only launch tool."""
    with pytest.raises(ValueError, match="Only a lead or co-lead"):
        launch(team, team.worker)


def test_close_during_capability_check_prevents_dispatch(
    team: Team, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Recheck close after a plugin capability call and never start the worker."""
    from agents_chat.mcp import plugin_client

    def close_before_start(plugin) -> None:
        """Close the room while the worker is checking the provider contract."""
        team.core.apply(models.Close(workspace=team.workspace, token=team.lead))

    def unexpected_start(*args) -> None:
        """Fail if the provider is contacted again after the room closes."""
        pytest.fail("A closed room queued a provider start")

    monkeypatch.setattr(plugin_client, "check_capabilities", close_before_start)
    monkeypatch.setattr(plugin_client, "run_operation", unexpected_start)
    launch(team)
    dispatch(team)

    assert state(team) == "cancelled"


def test_active_launch_protects_its_join_code(team: Team) -> None:
    """Reject a manual reissue while a worker is waiting to consume the code."""
    launch(team)

    with pytest.raises(ValueError, match="reserved for an active fresh run"):
        team.core.apply(models.Invite(workspace=team.workspace, token=team.lead, name="fresh"))


def test_round_release_cancels_round_worker(team: Team) -> None:
    """End a fresh run assigned to a sealed round when the lead starts critique."""
    from agents_chat.conversations import rounds
    from agents_chat.conversations.tables import room

    with transaction(team.workspace) as connection:
        connection.execute(
            update(room).values(round=rounds.SEALED, round_deadline=int(time.time()) + 60)
        )
    launch(team)
    dispatch(team)

    team.core.apply(models.Release(workspace=team.workspace, token=team.lead))
    team.core.worker.scan(restart=True)
    assert state(team) == "cancelled"

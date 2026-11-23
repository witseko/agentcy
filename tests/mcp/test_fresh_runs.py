"""Verify durable launch, reconciliation, cancellation and failure through MCP."""

import asyncio
import json
import sys
import time
from collections.abc import Callable
from dataclasses import dataclass
from functools import partial
from pathlib import Path
from typing import Any

import pytest
from sqlalchemy import select, update

from agents_chat.conversations import models
from agents_chat.conversations.assignment_tables import assignment
from agents_chat.mcp import assignment_worker, fresh_server, plugin_client, server
from agents_chat.mcp.assignment_worker import AssignmentWorker, Dispatch
from agents_chat.mcp.concurrency import Notifications, transaction
from agents_chat.mcp.core import Core
from agents_chat.mcp.plugin_contract import RunResult, Start
from agents_chat.mcp.plugin_settings import Plugin
from agents_chat.storage.database import workspace_key

FIXTURE = Path(__file__).parents[1] / "fixtures" / "fake_run_plugin.py"


@dataclass(frozen=True)
class Bridge:
    """Store a private test core with a workspace and a registered plugin."""

    core: Core
    directory: Path
    workspace: str


@dataclass(frozen=True)
class Team(Bridge):
    """Store the tokens of an existing lead and worker."""

    lead: str
    worker: str


@pytest.fixture
def bridge(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> Bridge:
    """Register one fixture executable in private config and prepare the core."""
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
    return Bridge(core, directory, workspace)


@pytest.fixture
def team(bridge: Bridge) -> Team:
    """Start a conversation with one joined worker."""
    core, directory, workspace = bridge.core, bridge.directory, bridge.workspace
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


def dispatch(bridge: Bridge) -> Dispatch:
    """Take one committed launch and send it through the real plugin client."""
    work = bridge.core.worker.jobs.get_nowait()
    assert work is not None
    bridge.core.worker.launch(work)
    return work


def call_tool(app: server.FastMCP, tool_name: str, **arguments: Any) -> dict:
    """Call a registered MCP tool and decode its response."""
    content = asyncio.run(app.call_tool(tool_name, arguments))
    return json.loads(content[0].text)


def post_as(
    member: Callable[..., dict],
    kind: models.PostKind,
    body: str,
    *,
    to: str | None = None,
    reply_to: int | None = None,
) -> int:
    """Post through a bound client in the workflow, once per kind and recipient."""
    return member(
        "post_message",
        submission_id=f"{kind}-{to}",
        kind=kind,
        body=body,
        to=to,
        reply_to=reply_to,
        due_minutes=30 if kind == "task" else None,
    )["message_id"]


def test_two_fresh_workers_resolve_a_blocker_and_close_with_evidence(
    bridge: Bridge, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Follow a shared retry review across two fresh worker shims and the lead shim."""
    apps: list[server.FastMCP] = []

    def capture(app: server.FastMCP, transport: str) -> None:
        """Capture the registered tool surface and keep the test in process."""
        assert transport == "stdio"
        apps.append(app)

    monkeypatch.setattr(server.FastMCP, "run", capture)
    monkeypatch.setattr(server, "exchange", bridge.core.apply)
    monkeypatch.setattr(fresh_server, "exchange", bridge.core.apply)
    server.serve(bridge.workspace, bridge.directory)
    lead_app = apps.pop()
    fixture = bridge.directory / "outcomes.txt"
    fixture.write_text("transient\ntransient\ntransient\nsuccess\n", encoding="utf-8")
    brief = "Calculate the retry delay for outcomes.txt. Double the delay from 2 s up to 8 s."
    names = ["Schedule", "Fixture"]
    created = call_tool(
        lead_app, "create_conversation", brief=brief, your_name="Lead", invitees=names
    )
    lead = partial(call_tool, lead_app, token=created["token"])
    workers = {}
    for name in names:
        launched = lead("launch_participant", name=name, plugin="fixture")
        work = dispatch(bridge)
        assert launched["assignment_id"] == work.start.assignment_id
        fresh_server.serve(bridge.workspace, work.start.join_code)
        workers[name] = partial(call_tool, apps.pop())
    schedule, evidence = workers["Schedule"], workers["Fixture"]
    schedule_task = post_as(
        lead, "task", "Calculate the delays after the failure count comes in.", to="Schedule"
    )
    evidence_task = post_as(
        lead, "task", "Verify the failure count from outcomes.txt.", to="Fixture"
    )

    first = schedule("join_conversation")
    assert first["brief"] == brief
    assert first["conversation"] == created["conversation"]
    blocker = post_as(
        schedule,
        "blocker",
        "Send the fixture's failure count.",
        to="Fixture",
        reply_to=schedule_task,
    )
    pending = lead("conversation_status")["members"]
    assert pending["Fixture"]["state"] == "invited"
    assert pending["Fixture"]["launch_state"] == "running"
    [blocked_task] = pending["Schedule"]["open_tasks"]
    assert (blocked_task["id"], blocked_task["blocked"]) == (schedule_task, True)
    lead_read = lead("read_messages", after=evidence_task, wait_seconds=1)
    assert "Ask a teammate who can clear the blocker" in lead_read["next"]
    question = post_as(
        lead, "question", "Please send Schedule the failure count.", to="Fixture", reply_to=blocker
    )

    second = evidence("join_conversation")
    assert second["conversation"] == created["conversation"]
    delayed_read = evidence("read_messages")
    assert delayed_read["brief"] == brief
    assert [entry["id"] for entry in delayed_read["messages"] if entry.get("to") == "Fixture"] == [
        evidence_task,
        blocker,
        question,
    ]
    failures = fixture.read_text(encoding="utf-8").splitlines().count("transient")
    source = json.dumps({"file": fixture.name, "failures": failures})
    finding = post_as(evidence, "finding", source, to="Schedule", reply_to=question)
    owner_read = schedule("read_messages", after=question, wait_seconds=1)
    assert [entry["id"] for entry in owner_read["messages"]] == [finding]
    assert "kind='resume'" in owner_read["next"]

    recovered = json.loads(owner_read["messages"][0]["body"])
    post_as(
        schedule, "resume", f"I have evidence from {recovered['file']}.", reply_to=schedule_task
    )
    [resumed_task] = lead("conversation_status")["members"]["Schedule"]["open_tasks"]
    assert resumed_task["id"] == schedule_task
    assert "blocked" not in resumed_task
    delays = [min(2 * 2**index, 8) for index in range(recovered["failures"])]
    post_as(schedule, "done", json.dumps(delays), reply_to=schedule_task)
    post_as(evidence, "done", source, reply_to=evidence_task)

    collected = lead("read_messages", after=blocker)
    results = {
        entry["label"]: entry for entry in collected["messages"] if entry.get("kind") == "done"
    }
    assert set(results) == {"Schedule", "Fixture"}
    verified_source = json.loads(results["Fixture"]["body"])
    verified_delays = json.loads(results["Schedule"]["body"])
    assert verified_source == {"file": "outcomes.txt", "failures": 3}
    assert verified_delays == [2, 4, 8]
    members = lead("conversation_status")["members"]
    for name in workers:
        assert members[name]["state"] == "done"
        assert "open_tasks" not in members[name]
    summary = (
        f"Allow {sum(verified_delays)} seconds for the {verified_source['failures']} retries. "
        f"Fixture #{results['Fixture']['id']} and schedule #{results['Schedule']['id']} agree."
    )
    assert lead("close_conversation", summary=summary) == {"closed": True}
    closed = schedule("read_messages", after=collected["cursor"])
    assert closed["closed"] is True
    assert [entry["body"] for entry in closed["messages"]] == [summary]
    assert summary.startswith("Allow 14 seconds for the 3 retries.")
    bridge.core.worker.scan()
    members = lead("conversation_status")["members"]
    assert [members[name]["launch_state"] for name in names] == ["cancelled", "cancelled"]


def test_fresh_status_and_wait_distinguish_manual_invitation(
    team: Team, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Keep waiting for fresh startup while a manual join code remains unused."""
    team.core.apply(
        models.Post(
            workspace=team.workspace,
            token=team.worker,
            submission_id="done",
            body="Finished my part",
            kind="done",
        )
    )
    team.core.apply(models.Invite(workspace=team.workspace, token=team.lead, name="Manual"))
    launch(team)

    def check_wait(launch_state: str) -> None:
        """Verify the authenticated status and guidance for an unjoined fresh run."""
        status = team.core.apply(models.Status(workspace=team.workspace, token=team.lead))
        assert status["members"]["Fresh"] == {
            "role": "worker",
            "state": "invited",
            "launch_state": launch_state,
        }
        assert status["members"]["Manual"] == {"role": "worker", "state": "invited"}
        page = server.finish_wait({"messages": []}, status, {}, "test-lead")
        assert page["waiting_on"] == ["Fresh"]
        assert f"Fresh (fresh run {launch_state})" in page["next"]
        assert "keep listening" in page["next"]
        assert "join codes" not in page["next"]

    check_wait("queued")
    provider = plugin_client.run_operation

    def inspect_during_start(plugin: Plugin, tool: str, request: Start | str) -> RunResult:
        """Check observable state while the provider starts the worker."""
        if tool == "start":
            check_wait("starting")
        return provider(plugin, tool, request)

    monkeypatch.setattr(plugin_client, "run_operation", inspect_during_start)
    dispatch(team)
    check_wait("running")

    task = team.core.apply(
        models.Post(
            workspace=team.workspace,
            token=team.lead,
            submission_id="task",
            body="Check the remaining evidence",
            kind="task",
            due_minutes=30,
            to="Fresh",
        )
    )
    status = team.core.apply(models.Status(workspace=team.workspace, token=team.lead))
    page = server.finish_wait({"messages": []}, status, {}, "test-lead")
    assert page["waiting_on"] == ["Fresh"]
    assert f"#{task['message_id']} for Fresh (fresh run running)" in page["next"]
    assert "keep listening" in page["next"]
    assert "invitation unused" not in page["next"]


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


@pytest.mark.parametrize(("stop", "outcome"), [("close", "cancelled"), ("deadline", "timed_out")])
def test_transient_provider_failure_reconciles_same_id_after_restart(
    team: Team, monkeypatch: pytest.MonkeyPatch, stop: str, outcome: str
) -> None:
    """Keep a known run pending through inspection and cancellation failures."""
    result = launch(team)
    dispatch(team)
    provider = plugin_client.run_operation
    attempts = []

    def fail_each_operation_once(plugin: Plugin, tool: str, request: Start | str) -> RunResult:
        """Simulate a temporary transport problem on each operation's first call."""
        assert request == result["assignment_id"]
        if tool not in attempts:
            attempts.append(tool)
            raise plugin_client.PluginError("Provider is temporarily unavailable")
        attempts.append(tool)
        return provider(plugin, tool, request)

    monkeypatch.setattr(plugin_client, "run_operation", fail_each_operation_once)
    team.core.worker.scan()
    assert state(team) == "running"
    assert launch(team) == {"assignment_id": result["assignment_id"], "state": "running"}
    assert team.core.worker.jobs.empty()
    if stop == "close":
        team.core.apply(models.Close(workspace=team.workspace, token=team.lead))
    else:
        with transaction(team.workspace) as connection:
            connection.execute(update(assignment).values(deadline=int(time.time()) - 1))
    team.core.worker.scan()
    assert state(team) == "running"

    restarted = AssignmentWorker(team.core.notifications)
    restarted.scan(restart=True)

    assert state(team) == outcome
    assert attempts == ["inspect", "cancel", "cancel"]
    page = team.core.apply(models.Read(workspace=team.workspace, token=team.lead))
    assert any(
        "could not confirm the fresh-run state" in entry["body"] for entry in page["messages"]
    )
    assert any("could not confirm cancellation" in entry["body"] for entry in page["messages"])
    assert not any("uncertain fresh-run state" in entry["body"] for entry in page["messages"])


def test_inspection_retries_when_provider_cannot_confirm_running_assignment(
    team: Team, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Retain the stable ID when a known running assignment cannot be inspected once."""
    result = launch(team)
    dispatch(team)
    attempts = []
    now = 100.0

    def recover(plugin: Plugin, tool: str, request: Start | str) -> RunResult:
        """Return unknown briefly and then confirm that the original run finished."""
        attempts.append((tool, request))
        return RunResult(state="unknown" if len(attempts) == 1 else "finished")

    monkeypatch.setattr(assignment_worker, "monotonic", lambda: now)
    monkeypatch.setattr(plugin_client, "run_operation", recover)
    team.core.worker.scan()
    team.core.worker.scan()
    assert state(team) == "running"
    assert attempts == [("inspect", result["assignment_id"])]

    now += 10
    team.core.worker.scan()

    assert state(team) == "finished"
    assert attempts == [("inspect", result["assignment_id"])] * 2


@pytest.mark.parametrize("unknown_inspect", [False, True], ids=["lost-inspect", "unknown-inspect"])
def test_lost_start_response_is_reconciled_without_second_start(
    team: Team, monkeypatch: pytest.MonkeyPatch, unknown_inspect: bool
) -> None:
    """Recover a run when both start and the first inspection are unconfirmed."""
    result = launch(team)
    provider = plugin_client.run_operation
    attempts = []

    def lose_responses(plugin: Plugin, tool: str, request: Start | str) -> RunResult:
        """Run each operation through the real fixture and drop its first responses."""
        attempts.append(tool)
        assignment_id = request.assignment_id if isinstance(request, Start) else request
        assert assignment_id == result["assignment_id"]
        response = provider(plugin, tool, request)
        if len(attempts) == 2 and unknown_inspect:
            return RunResult(state="unknown")
        if len(attempts) <= 2:
            raise plugin_client.PluginError("Provider response was lost")
        return response

    monkeypatch.setattr(plugin_client, "run_operation", lose_responses)
    dispatch(team)
    assert state(team) == "starting"
    assert launch(team) == {"assignment_id": result["assignment_id"], "state": "starting"}
    assert team.core.worker.jobs.empty()

    restarted = AssignmentWorker(team.core.notifications)
    restarted.scan(restart=True)

    assert state(team) == "running"
    assert attempts == ["start", "inspect", "inspect"]


def test_cancel_retries_are_bounded_when_provider_stays_unavailable(
    team: Team, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Report uncertainty once and stop polling after persistent cancel failures."""
    result = launch(team)
    dispatch(team)
    team.core.apply(models.Close(workspace=team.workspace, token=team.lead))
    attempts = []
    now = 100.0

    def unavailable(plugin: Plugin, tool: str, request: Start | str) -> RunResult:
        """Track idempotent cancel calls while the provider remains unavailable."""
        attempts.append((tool, request))
        raise plugin_client.PluginError("Provider is unavailable")

    monkeypatch.setattr(assignment_worker, "monotonic", lambda: now)
    monkeypatch.setattr(plugin_client, "run_operation", unavailable)
    for _ in range(10):
        team.core.worker.scan()
        now += 10

    assert state(team) == "unknown"
    assert 1 < len(attempts) < 10
    assert all(call == ("cancel", result["assignment_id"]) for call in attempts)
    page = team.core.apply(models.Read(workspace=team.workspace, token=team.lead))
    assert sum("could not confirm cancellation" in entry["body"] for entry in page["messages"]) == 1
    assert sum("uncertain fresh-run state" in entry["body"] for entry in page["messages"]) == 1


@pytest.mark.parametrize("outcome", ["cancelled", "unknown"])
def test_active_cancel_acknowledgments_remain_pending_until_stop_or_deadline(
    team: Team, monkeypatch: pytest.MonkeyPatch, outcome: str
) -> None:
    """Keep polling a stopping provider past the limit for failed checks."""
    result = launch(team)
    dispatch(team)
    team.core.apply(models.Close(workspace=team.workspace, token=team.lead))
    attempts = []
    elapsed = 0.0
    started = time.time()
    stopped = False

    def cancel(plugin: Plugin, tool: str, request: Start | str) -> RunResult:
        """Acknowledge cancellation even while the provider process remains active."""
        attempts.append((tool, request))
        return RunResult(state="cancelled" if stopped else "running")

    monkeypatch.setattr(assignment_worker, "monotonic", lambda: elapsed)
    monkeypatch.setattr(assignment_worker, "time", lambda: started + elapsed)
    monkeypatch.setattr(plugin_client, "run_operation", cancel)
    for poll in range(7):
        team.core.worker.scan()
        team.core.worker.scan()
        assert len(attempts) == poll + 1
        assert state(team) == "running"
        elapsed += 10

    if outcome == "cancelled":
        stopped = True
    else:
        elapsed += 200
    team.core.worker.scan()
    elapsed += 10
    team.core.worker.scan()

    assert state(team) == outcome
    assert attempts == [("cancel", result["assignment_id"])] * 8


@pytest.mark.parametrize(
    ("outcome", "expected_state", "text"),
    [
        ("failed", "failed", "could not complete the fresh run"),
        ("unknown", "starting", "could not confirm the fresh-run state"),
    ],
)
def test_dispatch_failure_is_visible_and_reissue_is_safe(
    team: Team, monkeypatch: pytest.MonkeyPatch, outcome: str, expected_state: str, text: str
) -> None:
    """Show the failure to the lead and preserve the original ID on retries."""
    monkeypatch.setenv("TEST_FRESH_RESULT", outcome)
    result = launch(team)
    dispatch(team)
    assert state(team) == expected_state
    retry = launch(team)
    assert retry == {"assignment_id": result["assignment_id"], "state": expected_state}
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

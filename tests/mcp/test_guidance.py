"""Verify that waiting reads use participant progress and count quiet waits."""

import asyncio
import json
from collections import Counter
from typing import Any

import pytest
from sqlalchemy import Connection

from agents_chat.conversations import models, repository
from agents_chat.mcp import server


@pytest.mark.parametrize(
    ("caller", "peer_state", "has_messages", "waits", "expected"),
    [
        ("lead", "invited", True, 8, "Handle these messages"),
        ("lead", "done", False, 1, "All other members are done. Stop listening"),
        ("lead", "replied", False, 1, "Still working: Peer. To keep listening"),
        ("lead", "blocked", False, 1, "tell your user who is blocked: Peer"),
        ("lead", "joined", False, 7, "Still working: Peer. To keep listening"),
        ("co-lead", "joined", False, 8, "Stop listening and tell your user"),
        ("lead", "invited", False, 1, "join codes are unused: Peer"),
        ("worker", "joined", False, 3, "You have not posted your part"),
        ("worker", "replied", False, 2, "No new messages. To keep listening"),
        ("worker", "replied", False, 3, "Stop listening and tell your user"),
    ],
)
def test_guidance_follows_member_progress(
    caller: str, peer_state: str, has_messages: bool, waits: int, expected: str
) -> None:
    """Lead and worker guidance uses the correct state and idle limit."""
    status = {
        "open": True,
        "members": {
            "Me": {"role": caller, "state": peer_state},
            "Peer": {"role": "worker", "state": peer_state},
        },
    }

    assert expected in server.guidance(status, "Me", has_messages, waits)


@pytest.mark.parametrize(
    ("launch_state", "open_task"),
    [
        ("failed", {"id": 17}),
        ("cancelled", None),
        ("timed_out", {"id": 17, "blocked": True}),
        ("finished", None),
        ("unknown", {"id": 17}),
    ],
)
def test_lead_reports_ended_fresh_worker_and_tracks_available_peer(
    launch_state: str, open_task: dict | None
) -> None:
    """Distinguish unfinished terminal runs from active and completed peers."""
    fresh = {"role": "worker", "state": "replied", "launch_state": launch_state}
    if open_task is not None:
        fresh["open_tasks"] = [open_task]
    peer = {"role": "worker", "state": "joined"}
    status = {
        "open": True,
        "participant": "Lead",
        "members": {
            "Lead": {"role": "lead", "state": "replied"},
            "Fresh": fresh,
            "Peer": peer,
            "Done": {"role": "worker", "state": "done", "launch_state": "finished"},
        },
    }
    mixed = server.finish_wait({"messages": []}, status, {}, "lead")
    assert mixed["waiting_on"] == ["Peer"]
    assert f"Fresh (fresh run {launch_state})" in mixed["next"]
    if open_task is not None:
        assert "#17 for Fresh" in mixed["next"]
        peer["open_tasks"] = [{"id": 23}]
        tracked = server.finish_wait({"messages": []}, status, {}, "lead")
        assert tracked["waiting_on"] == ["Peer"]
        assert "#23 for Peer" in tracked["next"]
        assert "keep listening" in tracked["next"]
        peer.pop("open_tasks")
    assert "keep listening" in mixed["next"]
    assert "Done (fresh run" not in mixed["next"]

    peer["state"] = "done"
    stranded = server.finish_wait({"messages": []}, status, {}, "lead")
    assert "waiting_on" not in stranded
    assert f"Fresh (fresh run {launch_state})" in stranded["next"]
    assert "Stop listening" in stranded["next"]
    assert "All other members are done" not in stranded["next"]
    assert "join code" not in stranded["next"]

    fresh.pop("open_tasks", None)
    fresh["state"] = "done"
    completed = server.finish_wait({"messages": []}, status, {}, "lead")
    assert "All other members are done" in completed["next"]


def test_lead_waits_for_starting_fresh_worker_when_a_manual_task_cannot_start() -> None:
    """Count a starting worker even when only a manual invitee owns an open task."""
    status = {
        "open": True,
        "participant": "Lead",
        "members": {
            "Lead": {"role": "lead", "state": "replied"},
            "Manual": {"role": "worker", "state": "invited", "open_tasks": [{"id": 8}]},
            "Fresh": {"role": "worker", "state": "invited", "launch_state": "starting"},
        },
    }
    page = server.finish_wait({"messages": []}, status, {}, "lead")
    assert page["waiting_on"] == ["Fresh"]
    assert "keep listening" in page["next"]
    assert "#8 for Manual (invitation unused)" in page["next"]


@pytest.fixture
def shim(
    conversation_db: Connection, monkeypatch: pytest.MonkeyPatch
) -> tuple[server.FastMCP, Counter[str]]:
    """Route registered tool calls to the test database and skip real waiting time."""
    calls: Counter[str] = Counter()
    registered: list[server.FastMCP] = []

    def exchange(request: models.Request) -> dict:
        """Count local requests before executing them through the repository."""
        calls[request.action] += 1
        return repository.apply(conversation_db, request)

    def capture(app: server.FastMCP, transport: str) -> None:
        """Capture the configured shim without starting its stdio loop."""
        assert transport == "stdio"
        registered.append(app)

    monkeypatch.setattr(server, "exchange", exchange)
    monkeypatch.setattr(server.FastMCP, "run", capture)
    server.serve(conversation_db.info["workspace"])
    return registered[0], calls


def tool(app: server.FastMCP, name: str, /, **arguments: Any) -> dict:
    """Call one registered tool and decode its JSON response."""
    content = asyncio.run(app.call_tool(name, arguments))
    return json.loads(content[0].text)


def test_waiting_on_matches_names_in_guidance(shim: tuple[server.FastMCP, Counter[str]]) -> None:
    """A lead keeps listening to joined workers and names unused codes when they finish."""
    app, calls = shim
    team = tool(
        app,
        "create_conversation",
        brief="Review retries",
        your_name="Lead",
        invitees=["Worker", "Missing"],
    )
    worker = tool(app, "join_conversation", code=team["invitations"]["Worker"])

    waiting = tool(app, "read_messages", token=team["token"], wait_seconds=1)
    assert waiting["waiting_on"] == ["Worker"]
    assert "Still working: Worker" in waiting["next"]

    post = tool(
        app, "post_message", token=worker["token"], body="Done", submission_id="result", kind="done"
    )
    done = tool(app, "read_messages", token=team["token"], after=post["message_id"], wait_seconds=1)
    assert done["waiting_on"] == ["Missing"]
    assert "join codes are unused: Missing" in done["next"]
    assert calls["status"] == 2
    assert set(tool(app, "conversation_status", token=team["token"])) == {"open", "members"}


def test_waiting_read_retains_non_waking_messages(
    shim: tuple[server.FastMCP, Counter[str]],
) -> None:
    """Leave an unrelated message unread and return it with the next relevant message."""
    app, _ = shim
    team = tool(
        app,
        "create_conversation",
        brief="Review retries",
        your_name="Lead",
        invitees=["Target", "Observer"],
    )
    observer = tool(app, "join_conversation", code=team["invitations"]["Observer"])["token"]
    question = tool(
        app,
        "post_message",
        token=team["token"],
        body="Which test?",
        submission_id="question",
        kind="question",
        to="Target",
    )["message_id"]

    quiet = tool(app, "read_messages", token=observer, wait_seconds=1)
    assert quiet["wake"] is False
    assert quiet["messages"] == []
    assert quiet["cursor"] == 0
    history = tool(app, "read_messages", token=observer)
    assert [message["id"] for message in history["messages"]] == [question]

    broadcast = tool(
        app, "post_message", token=team["token"], body="Continue", submission_id="continue"
    )["message_id"]
    waking = tool(app, "read_messages", token=observer, after=quiet["cursor"], wait_seconds=1)
    assert waking["wake"] is True
    assert [message["id"] for message in waking["messages"]] == [question, broadcast]


def test_lead_can_route_blocker_help_to_a_peer(shim: tuple[server.FastMCP, Counter[str]]) -> None:
    """Use named wakeups to ask for help and deliver the answer to the task owner."""
    app, _ = shim
    team = tool(
        app,
        "create_conversation",
        brief="Review retries",
        your_name="Lead",
        invitees=["Owner", "Helper"],
    )
    lead = team["token"]
    owner = tool(app, "join_conversation", code=team["invitations"]["Owner"])["token"]
    helper = tool(app, "join_conversation", code=team["invitations"]["Helper"])["token"]
    task = tool(
        app,
        "post_message",
        token=lead,
        submission_id="task",
        body="Test retries",
        kind="task",
        to="Owner",
    )["message_id"]
    blocker = tool(
        app,
        "post_message",
        token=owner,
        submission_id="blocker",
        body="Fixture missing",
        kind="blocker",
        reply_to=task,
    )["message_id"]
    quiet = tool(app, "read_messages", token=helper, after=task, wait_seconds=1)
    assert quiet["messages"] == []

    lead_page = tool(app, "read_messages", token=lead, after=task, wait_seconds=1)
    assert "Ask a teammate who can clear the blocker" in lead_page["next"]
    assert "kind='question'" in lead_page["next"]
    assert "the blocker message ID in 'reply_to'" in lead_page["next"]

    question = tool(
        app,
        "post_message",
        token=lead,
        submission_id="question",
        body="Send Owner the fixture",
        kind="question",
        to="Helper",
        reply_to=blocker,
    )["message_id"]
    helper_page = tool(app, "read_messages", token=helper, after=task, wait_seconds=1)
    assert [message["id"] for message in helper_page["messages"]] == [blocker, question]

    answer = tool(
        app,
        "post_message",
        token=helper,
        submission_id="answer",
        body="Use tests/fixtures/retry",
        kind="finding",
        to="Owner",
        reply_to=question,
    )["message_id"]
    owner_page = tool(app, "read_messages", token=owner, after=question, wait_seconds=1)
    assert [message["id"] for message in owner_page["messages"]] == [answer]
    assert "reply to its original task ID with kind='resume'" in owner_page["next"]

    tool(
        app,
        "post_message",
        token=owner,
        submission_id="resume",
        body="I can run the test now",
        kind="resume",
        reply_to=task,
    )
    member = tool(app, "conversation_status", token=lead)["members"]["Owner"]
    assert member["open_tasks"] == [{"id": task}]
    assert member["state"] == "replied"


@pytest.mark.parametrize("resolution", ["resume", "done", "handoff"])
def test_lead_catch_up_skips_a_resolved_task_blocker(
    shim: tuple[server.FastMCP, Counter[str]], resolution: str
) -> None:
    """Read a block and resolution together without asking for help on the old block."""
    app, _ = shim
    team = tool(
        app,
        "create_conversation",
        brief="Review retries",
        your_name="Lead",
        invitees=["Owner", "Helper"],
    )
    lead = team["token"]
    owner = tool(app, "join_conversation", code=team["invitations"]["Owner"])["token"]
    task = tool(
        app,
        "post_message",
        token=lead,
        submission_id="task",
        body="Test retries",
        kind="task",
        to="Owner",
    )["message_id"]
    blocker = tool(
        app,
        "post_message",
        token=owner,
        submission_id="blocker",
        body="Fixture missing",
        kind="blocker",
        reply_to=task,
    )["message_id"]
    resolution_id = tool(
        app,
        "post_message",
        token=owner,
        submission_id="resolution",
        body="I have a way forward",
        kind=resolution,
        to="Helper" if resolution == "handoff" else None,
        reply_to=task,
    )["message_id"]
    if resolution == "handoff":
        helper = tool(app, "join_conversation", code=team["invitations"]["Helper"])["token"]
        tool(
            app,
            "post_message",
            token=helper,
            submission_id="accept",
            body="I can finish this task",
            kind="accept",
            reply_to=resolution_id,
        )

    page = tool(app, "read_messages", token=lead, after=task, wait_seconds=1)

    assert [message["id"] for message in page["messages"]][:2] == [blocker, resolution_id]
    assert "Ask a teammate who can clear the blocker" not in page["next"]


@pytest.mark.parametrize("linked", [False, True], ids=["standalone", "question-reply"])
def test_lead_uses_member_state_for_a_standalone_blocker(
    shim: tuple[server.FastMCP, Counter[str]],
    linked: bool,
) -> None:
    """Offer help for a blocker outside a task until the member reports completion."""
    app, _ = shim
    team = tool(app, "create_conversation", brief="Review", your_name="Lead", invitees=["Worker"])
    lead = team["token"]
    worker = tool(app, "join_conversation", code=team["invitations"]["Worker"])["token"]
    after = 0
    if linked:
        after = tool(
            app,
            "post_message",
            token=lead,
            submission_id="question",
            body="Can you check retries?",
            kind="question",
            to="Worker",
        )["message_id"]
    tool(
        app,
        "post_message",
        token=worker,
        submission_id="block",
        body="Need help",
        kind="blocker",
        reply_to=after or None,
    )
    blocked_page = tool(app, "read_messages", token=lead, after=after, wait_seconds=1)
    assert "Ask a teammate who can clear the blocker" in blocked_page["next"]

    tool(app, "post_message", token=worker, submission_id="done", body="Finished", kind="done")
    complete_page = tool(app, "read_messages", token=lead, after=after, wait_seconds=1)
    assert [message["kind"] for message in complete_page["messages"]] == ["blocker", "done"]
    assert "Ask a teammate who can clear the blocker" not in complete_page["next"]


@pytest.mark.parametrize("reset_by", ["waiting read", "one-time read", "post"])
def test_silent_waits_reset_after_a_message_or_post(
    shim: tuple[server.FastMCP, Counter[str]], reset_by: str
) -> None:
    """A worker starts a new series of quiet waits after any message or own post."""
    app, calls = shim
    team = tool(
        app, "create_conversation", brief="Review retries", your_name="Lead", invitees=["W"]
    )
    worker = tool(app, "join_conversation", code=team["invitations"]["W"])
    worker_token = worker["token"]
    first = tool(app, "post_message", token=worker_token, body="Findings", submission_id="first")
    cursor = first["message_id"]

    for _ in range(server.IDLE_WAITS - 1):
        page = tool(app, "read_messages", token=worker_token, after=cursor, wait_seconds=1)
        assert "keep listening" in page["next"]
    quiet = tool(app, "read_messages", token=worker_token, after=cursor, wait_seconds=1)
    assert "Stop listening" in quiet["next"]
    assert calls["status"] == server.IDLE_WAITS

    if reset_by == "post":
        tool(app, "post_message", token=worker_token, body="More findings", submission_id="second")
    else:
        tool(app, "post_message", token=team["token"], body="Thanks", submission_id="thanks")
        page = tool(
            app,
            "read_messages",
            token=worker_token,
            after=cursor,
            wait_seconds=int(reset_by == "waiting read"),
        )
        assert [message["body"] for message in page["messages"]] == ["Thanks"]
        cursor = page["cursor"]
    assert calls["status"] == server.IDLE_WAITS + int(reset_by == "waiting read")

    resumed = tool(app, "read_messages", token=worker_token, after=cursor, wait_seconds=1)
    assert resumed["messages"] == []
    assert "keep listening" in resumed["next"]


def test_waiting_read_catches_close_between_page_and_status(
    shim: tuple[server.FastMCP, Counter[str]], monkeypatch: pytest.MonkeyPatch
) -> None:
    """Reload the page to include a final summary posted after the last poll."""
    app, calls = shim
    team = tool(
        app, "create_conversation", brief="Review retries", your_name="Lead", invitees=["W"]
    )
    worker = tool(app, "join_conversation", code=team["invitations"]["W"])
    exchange = server.exchange
    summary = "The retry check passed."

    def close_before_status(request: models.Request) -> dict:
        """Close after the empty read and before the waiting status check."""
        if isinstance(request, models.Status):
            exchange(
                models.Close(workspace=request.workspace, token=team["token"], summary=summary)
            )
        return exchange(request)

    monkeypatch.setattr(server, "exchange", close_before_status)
    page = tool(app, "read_messages", token=worker["token"], wait_seconds=1)

    assert page["closed"] is True
    assert [message["body"] for message in page["messages"]] == [summary]
    assert "Stop listening and report the outcome" in page["next"]
    assert calls["status"] == 1
    assert calls["read"] == 2


def test_lead_sees_team_status_and_free_workers_waiting_for_next_tasks(
    shim: tuple[server.FastMCP, Counter[str]],
) -> None:
    """Surface team state on waiting reads and prompt the lead when workers finish."""
    app, _ = shim
    team = tool(
        app,
        "create_conversation",
        brief="Ship feature",
        your_name="Lead",
        invitees=["Alice", "Bob"],
    )
    lead = team["token"]
    alice = tool(app, "join_conversation", code=team["invitations"]["Alice"])["token"]
    bob = tool(app, "join_conversation", code=team["invitations"]["Bob"])["token"]
    task_a = tool(
        app,
        "post_message",
        token=lead,
        submission_id="task-a",
        body="Build API",
        kind="task",
        to="Alice",
    )["message_id"]
    task_b = tool(
        app,
        "post_message",
        token=lead,
        submission_id="task-b",
        body="Write tests",
        kind="task",
        to="Bob",
    )["message_id"]

    # Workers do not receive the lead's team status snapshot.
    worker_page = tool(app, "read_messages", token=alice, after=task_b, wait_seconds=1)
    assert "team" not in worker_page

    # Alice finishes while Bob still has an open task.
    done_a = tool(
        app,
        "post_message",
        token=alice,
        submission_id="done-a",
        body="API ready",
        kind="done",
        reply_to=task_a,
    )["message_id"]
    lead_after_a = tool(app, "read_messages", token=lead, after=task_b, wait_seconds=1)
    assert lead_after_a["team"]["Alice"]["state"] == "done"
    assert lead_after_a["team"]["Bob"]["open_tasks"] == [{"id": task_b}]
    assert "Free teammates waiting for next steps: Alice" in lead_after_a["next"]

    # On repeated quiet waits while Bob works, the lead is reminded to check in.
    first_quiet = tool(app, "read_messages", token=lead, after=done_a, wait_seconds=1)
    assert server.LEAD_CHECKIN_HELP not in first_quiet["next"]
    second_quiet = tool(app, "read_messages", token=lead, after=done_a, wait_seconds=1)
    assert server.LEAD_CHECKIN_HELP in second_quiet["next"]
    assert "Free teammates waiting for next steps: Alice" in second_quiet["next"]

    # Once Bob also finishes, the lead is told immediately without another blind wait.
    tool(
        app,
        "post_message",
        token=bob,
        submission_id="done-b",
        body="Tests green",
        kind="done",
        reply_to=task_b,
    )
    lead_after_b = tool(app, "read_messages", token=lead, after=done_a, wait_seconds=1)
    assert lead_after_b["team"]["Alice"]["state"] == "done"
    assert lead_after_b["team"]["Bob"]["state"] == "done"
    assert "All other members are done" in lead_after_b["next"]
    assert "assign any follow-up tasks" in lead_after_b["next"]


def test_retired_member_gets_stop_guidance_and_lead_does_not_wait_on_retired() -> None:
    """A retired member is told to stop listening, and the lead stops waiting on retired peers."""
    status = {
        "open": True,
        "participant": "Worker",
        "members": {
            "Lead": {"role": "lead", "state": "replied"},
            "Worker": {"role": "worker", "state": "retired"},
        },
    }
    retired_page = server.finish_wait({"messages": [], "retired": True}, status, {"w": 2}, "w")
    assert retired_page["next"] == server.NEXT_AFTER_RETIRE
    assert "team" not in retired_page

    lead_status = {**status, "participant": "Lead"}
    lead_page = server.finish_wait({"messages": []}, lead_status, {}, "l")
    assert "waiting_on" not in lead_page
    assert "All other members are done" in lead_page["next"]

    lead_status["members"]["Worker"]["open_tasks"] = [{"id": 9}]
    stranded = server.finish_wait({"messages": []}, lead_status, {}, "l")
    assert "#9 for Worker (retired)" in stranded["next"]
    assert "Stop listening" in stranded["next"]


def test_assignment_worker_cancels_retired_participant_run() -> None:
    """Retiring a slot with an active fresh run marks it for cancellation."""
    from agents_chat.mcp.assignment_worker import AssignmentWorker

    row = {
        "open": 1,
        "retired": 1,
        "round": None,
        "current_round": None,
        "round_deadline": None,
        "deadline": 9_999_999_999,
    }
    assert AssignmentWorker.stop_reason(row) == "cancelled"


def test_lead_waits_for_open_invite_joiners_instead_of_claiming_done() -> None:
    """An empty open-invite room keeps the lead waiting up to the idle limit."""
    status = {
        "open": True,
        "open_invite": True,
        "participant": "Lead",
        "members": {"Lead": {"role": "lead", "state": "replied"}},
    }
    waits: dict[str, int] = {}
    first = server.finish_wait({"messages": []}, status, waits, "lead")
    assert "Waiting for teammates to join via the open invite" in first["next"]
    waits["lead"] = server.LEAD_IDLE_WAITS - 1
    final = server.finish_wait({"messages": []}, status, waits, "lead")
    assert "No teammates have joined yet via the open invite" in final["next"]
    assert "Stop listening" in final["next"]


def test_mcp_lead_can_open_invite_and_retire_participants(
    shim: tuple[server.FastMCP, Counter[str]],
) -> None:
    """MCP tools let a lead enable open invites, accept named joiners, and retire them."""
    app, _ = shim
    created = tool(
        app,
        "create_conversation",
        brief="Team management",
        your_name="Lead",
        open_invite=True,
    )
    lead = created["token"]
    open_code = created["open_invite"]["code"]
    joined = tool(app, "join_conversation", code=open_code, name="Scout")
    assert joined["participant"] == "Scout"
    retired = tool(app, "retire_participant", token=lead, name="Scout")
    assert retired == {"participant": "Scout", "retired": True}
    disabled = tool(app, "open_invite", token=lead, enabled=False)
    assert disabled == {"open_invite": False}


def test_mcp_lead_can_set_participant_role(
    shim: tuple[server.FastMCP, Counter[str]],
) -> None:
    """MCP set_participant_role transfers lead and keeps the previous lead active as worker."""
    app, _ = shim
    created = tool(
        app,
        "create_conversation",
        brief="Lead transfer",
        your_name="Lead",
        invitees=["Scout"],
    )
    lead = created["token"]
    scout = tool(app, "join_conversation", code=created["invitations"]["Scout"])["token"]
    changed = tool(app, "set_participant_role", token=lead, name="Scout", role="lead")
    assert changed == {
        "participant": "Scout",
        "role": "lead",
        "roster": {"Lead": "worker", "Scout": "lead"},
    }
    status = tool(app, "conversation_status", token=scout)
    assert status["members"]["Scout"]["role"] == "lead"
    assert status["members"]["Lead"]["role"] == "worker"
    assert status["members"]["Lead"]["state"] == "joined"


def test_lead_guidance_surfaces_token_balance_and_compact_team_usage(
    shim: tuple[server.FastMCP, Counter[str]],
) -> None:
    """Expose compact teammate usage in team snapshot and rebalancing advice in lead next."""
    app, _ = shim
    team = tool(
        app,
        "create_conversation",
        brief="Compare cache invalidation strategies",
        your_name="Claude",
        your_model="claude-sonnet-4-6",
        your_token_budget=100_000,
        invitees=[{"name": "Gemini", "model": "gemini-2.5-pro", "token_budget": 1_000_000}],
    )
    lead = team["token"]
    gemini = tool(app, "join_conversation", code=team["invitations"]["Gemini"])["token"]
    tool(
        app,
        "report_usage",
        token=lead,
        input_tokens=50_000,
        output_tokens=20_000,
    )
    tool(
        app,
        "report_usage",
        token=gemini,
        input_tokens=40_000,
        output_tokens=10_000,
    )
    tool(
        app,
        "post_message",
        token=gemini,
        submission_id="g1",
        body="Initial cache scan complete.",
    )

    page = tool(app, "read_messages", token=lead, after=0, wait_seconds=1)
    assert page["team"]["Gemini"]["usage"]["model"] == "gemini-2.5-pro"
    assert page["team"]["Gemini"]["usage"]["used_percent"] == 5.0
    assert page["token_balance"]["balanced"] is False
    assert page["token_balance"]["preferred"] == ["Gemini"]
    assert "recommendation" not in page["token_balance"]
    assert "Token balance:" in page["next"]
    assert "Prefer assigning new tasks or handoffs to Gemini." in page["next"]


def test_silent_waits_omit_unchanged_team_and_balanced_token_balance(
    shim: tuple[server.FastMCP, Counter[str]],
) -> None:
    """Omit redundant team snapshots on unchanged silent waits and omit balanced token_balance."""
    app, _ = shim
    team = tool(
        app,
        "create_conversation",
        brief="Check delta team snapshots",
        your_name="Lead",
        your_model="claude-sonnet-4-6",
        your_token_budget=100_000,
        invitees=[{"name": "Worker", "model": "gemini-2.5-pro", "token_budget": 100_000}],
    )
    lead = team["token"]
    tool(app, "join_conversation", code=team["invitations"]["Worker"])

    first = tool(app, "read_messages", token=lead, after=0, wait_seconds=1)
    assert "team" in first
    assert "team_unchanged" not in first
    assert "token_balance" not in first

    second = tool(app, "read_messages", token=lead, after=first["cursor"], wait_seconds=1)
    assert "team" not in second
    assert second["team_unchanged"] is True


def test_incoming_action_hints_for_task_handoff_and_question(
    shim: tuple[server.FastMCP, Counter[str]],
) -> None:
    """Tailor next guidance when a worker receives a direct task, handoff, or question."""
    app, _ = shim
    team = tool(
        app,
        "create_conversation",
        brief="Action hints",
        your_name="Lead",
        invitees=["Alpha", "Bravo"],
    )
    lead = team["token"]
    alpha = tool(app, "join_conversation", code=team["invitations"]["Alpha"])["token"]
    bravo = tool(app, "join_conversation", code=team["invitations"]["Bravo"])["token"]

    task_id = tool(
        app,
        "post_message",
        token=lead,
        submission_id="t1",
        body="Check retries",
        kind="task",
        to="Alpha",
    )["message_id"]
    alpha_task = tool(app, "read_messages", token=alpha, after=0, wait_seconds=1)
    assert f"New task #{task_id} assigned to you" in alpha_task["next"]

    handoff_id = tool(
        app,
        "post_message",
        token=alpha,
        submission_id="h1",
        body="Take over retries",
        kind="handoff",
        to="Bravo",
        reply_to=task_id,
    )["message_id"]
    bravo_handoff = tool(app, "read_messages", token=bravo, after=0, wait_seconds=1)
    assert f"Handoff #{handoff_id} offered to you" in bravo_handoff["next"]

    q_id = tool(
        app,
        "post_message",
        token=lead,
        submission_id="q1",
        body="Which fixture?",
        kind="question",
        to="Alpha",
    )["message_id"]
    alpha_q = tool(app, "read_messages", token=alpha, after=task_id, wait_seconds=1)
    assert f"Direct question #{q_id} from Lead" in alpha_q["next"]


def test_lead_checkin_names_stale_worker_and_worker_sees_peer_blockers() -> None:
    """Name a stale peer after STALE_IDLE_SECONDS and surface peer_blockers to idle workers."""
    lead_status = {
        "open": True,
        "participant": "Lead",
        "members": {
            "Lead": {"role": "lead", "state": "replied"},
            "Slow": {
                "role": "worker",
                "state": "replied",
                "idle_seconds": server.STALE_IDLE_SECONDS + 30,
                "open_tasks": [{"id": 7}],
            },
        },
    }
    waits = {"lead": server.LEAD_CHECKIN_WAITS - 1}
    page = server.finish_wait({"messages": []}, lead_status, waits, "lead")
    assert "Slow has been quiet for 150s on #7" in page["next"]

    worker_status = {
        "open": True,
        "participant": "Helper",
        "members": {
            "Lead": {"role": "lead", "state": "done"},
            "Helper": {"role": "worker", "state": "done"},
            "Stuck": {
                "role": "worker",
                "state": "blocked",
                "open_tasks": [{"id": 9, "blocked": True}],
            },
        },
    }
    # Done worker with no active peers stops after WORKER_IDLE_DONE_WAITS (1).
    done_page = server.finish_wait({"messages": []}, worker_status, {}, "helper", unread_other=2)
    assert done_page["unread_other_messages"] == 2
    assert done_page["peer_blockers"] == [{"name": "Stuck", "tasks": [9]}]
    assert "Stop listening" in done_page["next"]

    # Done worker with an active peer stays listening up to WORKER_ACTIVE_IDLE_WAITS (5).
    worker_status["members"]["Lead"]["state"] = "replied"
    active_page = server.finish_wait({"messages": []}, worker_status, {"helper": 3}, "helper")
    assert "Stuck is blocked" in active_page["next"]
    assert "read_messages" in active_page["next"]

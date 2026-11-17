"""Verify that waiting reads use participant progress and count quiet waits."""

import asyncio
import json
from collections import Counter
from itertools import count
from types import SimpleNamespace
from typing import Any

import pytest
from sqlalchemy import Connection

from agents_chat.conversations import models, repository
from agents_chat.mcp import server


@pytest.mark.parametrize(
    ("caller", "peer_state", "has_messages", "waits", "expected"),
    [
        ("lead", "invited", True, 8, "Handle these messages"),
        ("lead", "replied", False, 1, "All members replied. Stop listening"),
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
    monkeypatch.setattr(server, "time", SimpleNamespace(monotonic=count(0, 2).__next__))
    monkeypatch.setattr(server.FastMCP, "run", capture)
    server.serve(conversation_db.info["workspace"])
    return registered[0], calls


def tool(app: server.FastMCP, name: str, **arguments: Any) -> dict:
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

    post = tool(app, "post_message", token=worker["token"], body="Done", submission_id="result")
    done = tool(app, "read_messages", token=team["token"], after=post["message_id"], wait_seconds=1)
    assert done["waiting_on"] == ["Missing"]
    assert "join codes are unused: Missing" in done["next"]
    assert calls["status"] == 2
    assert set(tool(app, "conversation_status", token=team["token"])) == {"open", "members"}


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

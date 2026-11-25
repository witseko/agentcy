"""Exercise the installed stdio bridge with independent clients and a real socket core."""

import asyncio
import json
import os
import stat
import subprocess
import sys
import sysconfig
import time
from collections.abc import AsyncIterator, Iterator
from contextlib import asynccontextmanager
from pathlib import Path
from tempfile import TemporaryDirectory
from typing import Any

import pytest
from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client
from mcp.types import TextContent

TIMEOUT = 10
BRIEF = "Check the retry behavior"
SUMMARY = "The worker verified the retry behavior."
FOLLOW_UP = "The lead confirmed the result against the brief."


@pytest.fixture
def bridge_home() -> Iterator[Path]:
    """Run and reap a test-owned core in a short, private Unix socket directory."""
    # A pytest base temp directory can exceed the Unix socket path limit on macOS.
    with TemporaryDirectory(prefix="agentcy-e2e-", dir="/tmp") as directory:
        home = Path(directory)
        socket_path = home / "core.sock"
        log_path = home / "core.log"
        with log_path.open("w") as log:
            core = subprocess.Popen(
                [sys.executable, "-I", "-m", "agentcy.mcp.core"],
                cwd=home,
                env={"AGENTCY_HOME": directory, "PATH": os.defpath},
                stdin=subprocess.DEVNULL,
                stdout=log,
                stderr=subprocess.STDOUT,
            )
        try:
            deadline = time.monotonic() + TIMEOUT
            while not socket_path.exists():
                if core.poll() is not None or time.monotonic() >= deadline:
                    pytest.fail(f"Test core did not start:\n{log_path.read_text()}")
                time.sleep(0.02)
            yield home
        finally:
            core.terminate()
            try:
                core.wait(timeout=2)
            except subprocess.TimeoutExpired:
                core.kill()
                core.wait(timeout=2)


@asynccontextmanager
async def connect(home: Path, workspace: Path) -> AsyncIterator[ClientSession]:
    """Start and initialize one installed CLI shim through the MCP SDK."""
    command = Path(sysconfig.get_path("scripts")) / "agentcy"
    parameters = StdioServerParameters(
        command=str(command),
        args=["--workspace", str(workspace)],
        env={"AGENTCY_HOME": str(home), "PATH": os.defpath},
        cwd=workspace,
    )
    async with (
        stdio_client(parameters) as (reader, writer),
        ClientSession(reader, writer, read_timeout_seconds=TIMEOUT) as client,
    ):
        await client.initialize()
        yield client


async def call(client: ClientSession, tool: str, **arguments: Any) -> dict[str, Any]:
    """Decode the bridge's JSON result from MCP text content."""
    result = await client.call_tool(tool, arguments)
    assert not result.is_error, f"{tool} failed: {result.content}"
    assert len(result.content) == 1
    content = result.content[0]
    assert isinstance(content, TextContent)
    payload = json.loads(content.text)
    assert isinstance(payload, dict)
    return payload


async def assert_tool_error(
    client: ClientSession, tool: str, expected: str, **arguments: Any
) -> None:
    """Check the MCP error flag and the public rejection message."""
    result = await client.call_tool(tool, arguments)
    assert result.is_error, f"{tool} accepted a call that should fail"
    assert any(
        isinstance(content, TextContent) and expected in content.text for content in result.content
    ), result.content


async def exchange_task(
    lead: ClientSession, worker: ClientSession, lead_token: str, worker_token: str
) -> tuple[int, int]:
    """Assign one task, read it on the worker, and retry the worker's completion."""
    task = await call(
        lead,
        "post_message",
        token=lead_token,
        submission_id="task",
        body="Check retry handling.",
        kind="task",
        due_minutes=30,
        to="Worker",
    )
    task_id = task["message_id"]
    page = await call(worker, "read_messages", token=worker_token, wait_seconds=2)
    assert page["messages"] == [
        {
            "id": task_id,
            "label": "Lead",
            "body": "Check retry handling.",
            "kind": "task",
            "to": "Worker",
            "due_minutes": 30,
        }
    ]
    assert page["cursor"] == task_id

    completion = {
        "token": worker_token,
        "body": "The retry check passed.",
        "kind": "done",
        "reply_to": task_id,
    }
    posted = await call(worker, "post_message", submission_id="done", **completion)
    assert await call(worker, "post_message", submission_id="done", **completion) == posted
    assert await call(worker, "post_message", submission_id="done-retry", **completion) == posted

    page = await call(lead, "read_messages", token=lead_token, after=task_id)
    done_id = posted["message_id"]
    assert page["messages"] == [
        {
            "id": done_id,
            "label": "Worker",
            "body": completion["body"],
            "kind": "done",
            "reply_to": task_id,
        }
    ]
    assert page["cursor"] == done_id
    return task_id, done_id


async def complete_conversation(home: Path, workspace: Path) -> None:
    """Drive the full conversation across shims, including a worker reconnect."""
    async with connect(home, workspace) as lead:
        created = await call(
            lead, "create_conversation", brief=BRIEF, your_name="Lead", invitees=["Worker", "Late"]
        )
        lead_token = created["token"]
        code = created["invitations"]["Worker"]
        async with connect(home, workspace) as worker:
            joined = await call(worker, "join_conversation", code=code)
            assert joined["conversation"] == created["conversation"]
            assert joined["brief"] == BRIEF
            assert joined["role"] == "worker"
            worker_token = joined["token"]
            await assert_tool_error(
                worker, "join_conversation", "Invitation slot already taken: Worker", code=code
            )
            await assert_tool_error(
                worker, "close_conversation", "Only a lead or co-lead", token=worker_token
            )
            worker_cursor, done_id = await exchange_task(lead, worker, lead_token, worker_token)

        follow_up = await call(
            lead, "post_message", token=lead_token, submission_id="follow-up", body=FOLLOW_UP
        )
        follow_up_id = follow_up["message_id"]
        async with connect(home, workspace) as reconnected:
            page = await call(reconnected, "read_messages", token=worker_token, after=worker_cursor)
            assert page["messages"] == [{"id": follow_up_id, "label": "Lead", "body": FOLLOW_UP}]
            assert page["cursor"] == follow_up_id

            assert await call(lead, "close_conversation", token=lead_token, summary=SUMMARY) == {
                "closed": True
            }
            closed = await call(
                reconnected, "read_messages", token=worker_token, after=page["cursor"]
            )
            assert closed["closed"] is True
            assert closed["messages"] == [
                {"id": closed["cursor"], "label": "Lead", "body": SUMMARY}
            ]
            transcript = await call(reconnected, "read_messages", token=worker_token)
            assert transcript["brief"] == BRIEF
            assert transcript["closed"] is True
            assert transcript["more"] is False
            assert [message["id"] for message in transcript["messages"]] == [
                worker_cursor,
                done_id,
                follow_up_id,
                closed["cursor"],
            ]
            await assert_tool_error(
                reconnected,
                "post_message",
                "Conversation is closed",
                token=worker_token,
                submission_id="late",
                body="A late note.",
            )
            await assert_tool_error(
                reconnected,
                "join_conversation",
                "Invalid or expired invitation",
                code=created["invitations"]["Late"],
            )
            await assert_tool_error(
                reconnected,
                "reopen_conversation",
                "Only a lead or co-lead can reopen the conversation",
                token=worker_token,
            )
            assert await call(
                lead,
                "reopen_conversation",
                token=lead_token,
                note="Reopened for one more check.",
            ) == {"reopened": True}
            # Repeating reopen is idempotent and must not duplicate the note.
            assert await call(
                lead,
                "reopen_conversation",
                token=lead_token,
                note="Duplicate note should be ignored.",
            ) == {"reopened": True}
            # Previously expired unused invitations remain expired after reopen.
            await assert_tool_error(
                reconnected,
                "join_conversation",
                "Invalid or expired invitation",
                code=created["invitations"]["Late"],
            )
            resumed = await call(
                reconnected,
                "post_message",
                token=worker_token,
                submission_id="after-reopen",
                body="Back online after reopen.",
            )
            after_reopen = await call(
                lead, "read_messages", token=lead_token, after=closed["cursor"]
            )
            assert [message["body"] for message in after_reopen["messages"]] == [
                "Back online after reopen.",
            ]
            assert after_reopen["cursor"] == resumed["message_id"]
            worker_seen = await call(
                reconnected, "read_messages", token=worker_token, after=closed["cursor"]
            )
            assert [message["body"] for message in worker_seen["messages"]] == [
                "Reopened for one more check.",
            ]
            assert await call(lead, "close_conversation", token=lead_token) == {"closed": True}


def test_conversation_lifecycle(bridge_home: Path) -> None:
    """Exchange, deduplicate, resume and close messages across real processes."""
    workspace = bridge_home / "workspace"
    workspace.mkdir()
    asyncio.run(complete_conversation(bridge_home, workspace))
    (database,) = bridge_home.glob("*.sqlite3")
    socket_path = bridge_home / "core.sock"
    assert stat.S_IMODE(bridge_home.stat().st_mode) == 0o700
    assert socket_path.is_socket()
    assert stat.S_IMODE(socket_path.stat().st_mode) == 0o600
    assert stat.S_IMODE(database.stat().st_mode) == 0o600


def test_workspace_rejects_foreign_participant_token(bridge_home: Path) -> None:
    """A token issued in one workspace cannot read from another workspace."""
    first = bridge_home / "first"
    second = bridge_home / "second"
    first.mkdir()
    second.mkdir()

    async def check_workspace() -> None:
        """Obtain a real token and send it through a shim bound elsewhere."""
        async with connect(bridge_home, first) as lead, connect(bridge_home, second) as foreign:
            created = await call(
                lead, "create_conversation", brief=BRIEF, your_name="Lead", invitees=["Worker"]
            )
            await assert_tool_error(lead, "read_messages", "token")
            await assert_tool_error(
                foreign, "read_messages", "Invalid participant token", token=created["token"]
            )

    asyncio.run(check_workspace())

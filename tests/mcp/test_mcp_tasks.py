"""Check MCP tasks for fresh runs: launch_participant answers with an MCP task, and the
host follows and cancels the run with tasks/get, tasks/cancel, and tasks/update."""

import asyncio
import hashlib
import json
import logging
import re
import sqlite3
from collections.abc import Awaitable, Callable
from pathlib import Path
from typing import Any

import pytest
from mcp import Client
from mcp.client.extension import advertise
from mcp.shared.exceptions import MCPError
from mcp.types import INVALID_PARAMS, METHOD_NOT_FOUND, Request
from pydantic import TypeAdapter
from sqlalchemy import select

# pytest registers the imported fixture for this module.
from test_fresh_runs import Bridge, bridge, dispatch  # noqa: F401

from agentcy.conversations.assignment_tables import assignment
from agentcy.conversations.assignments import UNKNOWN_MCP_TASK
from agentcy.mcp import server, tasks
from agentcy.mcp.concurrency import transaction
from agentcy.mcp.core import REQUEST
from agentcy.storage.database import data_dir, workspace_key

RAW_RESULT = TypeAdapter(dict[str, Any])
ISO_UTC = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$")
# Claude Code accepts at most 128 printable ASCII characters without spaces.
TASK_ID = re.compile(r"^[!-~]{1,128}$")
MISSING_CAPABILITY = -32021
OPERATOR_KEY = "test-operator-key"


def build_app(
    test_bridge: Bridge, monkeypatch: pytest.MonkeyPatch, directory: Path | None = None
) -> server.BridgeServer:
    """Build the main server for a checkout, forwarding to the in-process test core."""
    built: list[server.BridgeServer] = []
    monkeypatch.setattr(server.BridgeServer, "run", lambda app, transport: built.append(app))
    monkeypatch.setattr(server, "exchange", test_bridge.core.apply)
    directory = directory or test_bridge.directory
    server.serve(workspace_key(directory), directory)
    return built[0]


def run_client(
    app: server.BridgeServer,
    action: Callable[[Client], Awaitable[Any]],
    *,
    mode: str = "auto",
    declare: bool = True,
) -> Any:
    """Connect an in-process client, optionally declaring the MCP Tasks extension."""

    async def connect() -> Any:
        extensions = [advertise(tasks.EXTENSION_ID, {})] if declare else []
        async with Client(app, mode=mode, extensions=extensions) as client:
            return await action(client)

    return asyncio.run(connect())


async def send(client: Client, method: str, params: dict[str, Any]) -> dict[str, Any]:
    """Send a request and return the raw result, which may be an MCP task."""
    request = Request[dict[str, Any], str](method=method, params=params)
    return await client.session.send_request(request, RAW_RESULT)


async def call(client: Client, tool: str, **arguments: Any) -> dict[str, Any]:
    """Call a tool and return its raw result."""
    return await send(client, "tools/call", {"name": tool, "arguments": arguments})


def decoded(result: dict[str, Any]) -> dict[str, Any]:
    """Decode the JSON text block of a normal tool result."""
    assert not result.get("isError"), result
    (content,) = result["content"]
    return json.loads(content["text"])


async def start_conversation(client: Client) -> str:
    """Create a conversation as lead and return the lead token."""
    created = await call(
        client, "create_conversation", brief="Work", your_name="Lead", invitees=["Worker"]
    )
    return decoded(created)["token"]


async def launch_task(client: Client) -> tuple[str, dict[str, Any]]:
    """Create a conversation, launch a fresh worker, and return the lead token and result."""
    token = await start_conversation(client)
    return token, await call(
        client, "launch_participant", token=token, name="Fresh", plugin="fixture"
    )


def task_get(client: Client, task_id: str) -> Awaitable[dict[str, Any]]:
    """Poll one MCP task."""
    return send(client, tasks.METHOD_GET, {"taskId": task_id})


def assignment_id(test_bridge: Bridge) -> str:
    """Read the one assignment ID of the test store."""
    with transaction(test_bridge.workspace) as connection:
        return connection.scalars(select(assignment.c.id)).one()


def stored_task_hashes(test_bridge: Bridge) -> list[str | None]:
    """Read the stored MCP task digests."""
    with transaction(test_bridge.workspace) as connection:
        return list(connection.scalars(select(assignment.c.mcp_task_hash)))


def finish_run(test_bridge: Bridge) -> None:
    """Let the fixture provider report the run as finished and reconcile it."""
    (data_dir() / assignment_id(test_bridge)).write_text("finished", encoding="utf-8")
    test_bridge.core.worker.scan()


def assert_task_fields(task: dict[str, Any], status: str) -> None:
    """Check the fields that every MCP task carries."""
    assert TASK_ID.fullmatch(task["taskId"])
    assert task["status"] == status
    assert ISO_UTC.fullmatch(task["createdAt"])
    assert ISO_UTC.fullmatch(task["lastUpdatedAt"])
    assert task["lastUpdatedAt"] >= task["createdAt"]
    assert task["ttlMs"] is None
    assert task["pollIntervalMs"] == tasks.POLL_INTERVAL_MS


def test_declaring_client_follows_mcp_task_to_completion(
    bridge: Bridge,  # noqa: F811 - pytest fixture argument
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """A 2026-07-28 client gets an MCP task and polls it from queued to finished."""
    app = build_app(bridge, monkeypatch)

    async def flow(client: Client) -> list[dict[str, Any]]:
        _, created = await launch_task(client)
        queued = await task_get(client, created["taskId"])
        await asyncio.to_thread(dispatch, bridge)
        running = await task_get(client, created["taskId"])
        await asyncio.to_thread(finish_run, bridge)
        completed = await task_get(client, created["taskId"])
        return [created, queued, running, completed]

    created, queued, running, completed = run_client(app, flow)

    assert created["resultType"] == "task"
    assert_task_fields(created, "working")
    assert created["statusMessage"] == "The fresh run for Fresh is queued."
    assert "content" not in created
    assert stored_task_hashes(bridge) == [hashlib.sha256(created["taskId"].encode()).hexdigest()]
    assert queued["resultType"] == "complete"
    assert_task_fields(queued, "working")
    assert queued["taskId"] == created["taskId"]
    assert running["statusMessage"] == "The fresh run for Fresh is running."
    assert_task_fields(completed, "completed")
    result = completed["result"]
    assert result["isError"] is False
    assert result["structuredContent"]["launch_state"] == "finished"
    assert result["structuredContent"]["participant"] == "Fresh"
    assert json.loads(result["content"][0]["text"]) == result["structuredContent"]


def test_failed_run_is_a_completed_mcp_task_with_a_tool_error(
    bridge: Bridge,  # noqa: F811 - pytest fixture argument
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """A plugin failure ends the MCP task with an isError result, not a JSON-RPC error."""
    monkeypatch.setenv("TEST_FRESH_RESULT", "failed")
    app = build_app(bridge, monkeypatch)

    async def flow(client: Client) -> dict[str, Any]:
        _, created = await launch_task(client)
        await asyncio.to_thread(dispatch, bridge)
        return await task_get(client, created["taskId"])

    failed = run_client(app, flow)

    assert_task_fields(failed, "completed")
    assert "error" not in failed
    assert failed["result"]["isError"] is True
    assert failed["result"]["structuredContent"] == {
        "participant": "Fresh",
        "launch_state": "failed",
        "next": tasks.ENDED_MESSAGES["failed"].format(name="Fresh"),
    }


@pytest.mark.parametrize("started", [True, False], ids=["running", "queued"])
def test_cancel_stops_the_run_and_keeps_the_participant(
    bridge: Bridge,  # noqa: F811 - pytest fixture argument
    monkeypatch: pytest.MonkeyPatch,
    started: bool,
) -> None:
    """tasks/cancel cancels the fresh run, and a second cancel after the end changes nothing."""
    app = build_app(bridge, monkeypatch)

    async def flow(client: Client) -> tuple[dict, dict, dict, dict, dict]:
        token, created = await launch_task(client)
        if started:
            await asyncio.to_thread(dispatch, bridge)
        acknowledged = await send(client, tasks.METHOD_CANCEL, {"taskId": created["taskId"]})
        if started:
            await asyncio.to_thread(bridge.core.worker.scan)
        else:
            await asyncio.to_thread(dispatch, bridge)
        cancelled = await task_get(client, created["taskId"])
        repeated = await send(client, tasks.METHOD_CANCEL, {"taskId": created["taskId"]})
        status = decoded(await call(client, "conversation_status", token=token))
        return acknowledged, cancelled, repeated, status, await task_get(client, created["taskId"])

    acknowledged, cancelled, repeated, status, after = run_client(app, flow)

    assert acknowledged["resultType"] == "complete"
    assert set(acknowledged) <= {"resultType", "_meta"}
    assert_task_fields(cancelled, "cancelled")
    assert cancelled["statusMessage"] == "The fresh run for Fresh was cancelled."
    assert repeated["resultType"] == "complete"
    assert after["status"] == "cancelled"
    assert after["lastUpdatedAt"] == cancelled["lastUpdatedAt"]
    assert status["members"]["Fresh"]["launch_state"] == "cancelled"
    assert "retired" not in status["members"]["Fresh"]


def test_cancel_after_completion_changes_nothing(
    bridge: Bridge,  # noqa: F811 - pytest fixture argument
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """A cancel for a finished run is an empty acknowledgement."""
    app = build_app(bridge, monkeypatch)

    async def flow(client: Client) -> tuple[dict, dict]:
        _, created = await launch_task(client)
        await asyncio.to_thread(dispatch, bridge)
        await asyncio.to_thread(finish_run, bridge)
        acknowledged = await send(client, tasks.METHOD_CANCEL, {"taskId": created["taskId"]})
        await asyncio.to_thread(bridge.core.worker.scan)
        return acknowledged, await task_get(client, created["taskId"])

    acknowledged, after = run_client(app, flow)

    assert acknowledged["resultType"] == "complete"
    assert after["status"] == "completed"
    assert after["result"]["structuredContent"]["launch_state"] == "finished"


def test_update_acknowledges_a_known_task(
    bridge: Bridge,  # noqa: F811 - pytest fixture argument
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """tasks/update answers with an empty result, since the bridge never asks for input."""
    app = build_app(bridge, monkeypatch)

    async def flow(client: Client) -> dict:
        _, created = await launch_task(client)
        params = {"taskId": created["taskId"], "inputResponses": {}}
        return await send(client, tasks.METHOD_UPDATE, params)

    acknowledged = run_client(app, flow)

    assert acknowledged["resultType"] == "complete"
    assert set(acknowledged) <= {"resultType", "_meta"}


@pytest.mark.parametrize(("mode", "declare"), [("auto", False), ("legacy", True)])
def test_other_clients_get_the_normal_result_and_no_task_id(
    bridge: Bridge,  # noqa: F811 - pytest fixture argument
    monkeypatch: pytest.MonkeyPatch,
    mode: str,
    declare: bool,
) -> None:
    """A client without the extension, or on a legacy connection, gets today's result."""
    app = build_app(bridge, monkeypatch)

    async def flow(client: Client) -> dict[str, Any]:
        _, launched = await launch_task(client)
        return launched

    launched = run_client(app, flow, mode=mode, declare=declare)

    assert launched.get("resultType", "complete") == "complete"
    assert decoded(launched) == {"assignment_id": assignment_id(bridge), "state": "queued"}
    assert stored_task_hashes(bridge) == [None]


def test_unknown_and_foreign_task_ids_get_one_error(
    bridge: Bridge,  # noqa: F811 - pytest fixture argument
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    """An MCP task ID from another workspace gets the same error as an unknown one."""
    app = build_app(bridge, monkeypatch)
    created = run_client(app, lambda client: launch_task(client))[1]
    other = tmp_path / "other"
    other.mkdir()
    other_app = build_app(bridge, monkeypatch, other)

    async def errors(client: Client, with_store: bool) -> list[MCPError]:
        if with_store:
            await start_conversation(client)
        rejected = []
        for task_id in (created["taskId"], "x" * 43, "not a task id"):
            for method in (tasks.METHOD_GET, tasks.METHOD_CANCEL):
                with pytest.raises(MCPError) as caught:
                    await send(client, method, {"taskId": task_id})
                rejected.append(caught.value)
        return rejected

    rejected = run_client(other_app, lambda client: errors(client, with_store=False))
    rejected += run_client(other_app, lambda client: errors(client, with_store=True))

    assert {(error.code, error.message) for error in rejected} == {
        (INVALID_PARAMS, UNKNOWN_MCP_TASK)
    }


def test_methods_need_the_extension_and_a_modern_connection(
    bridge: Bridge,  # noqa: F811 - pytest fixture argument
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """The server advertises the extension and serves its methods only to declaring clients."""
    app = build_app(bridge, monkeypatch)

    async def capabilities(client: Client) -> dict[str, Any] | None:
        return client.server_capabilities.extensions

    async def rejection(client: Client) -> MCPError:
        with pytest.raises(MCPError) as caught:
            await send(client, tasks.METHOD_GET, {"taskId": "x" * 43})
        return caught.value

    assert tasks.EXTENSION_ID in run_client(app, capabilities)
    undeclared = run_client(app, rejection, declare=False)
    legacy = run_client(app, rejection, mode="legacy")

    assert undeclared.code == MISSING_CAPABILITY
    assert legacy.code == METHOD_NOT_FOUND


def test_task_id_never_leaks(
    bridge: Bridge,  # noqa: F811 - pytest fixture argument
    monkeypatch: pytest.MonkeyPatch,
    caplog: pytest.LogCaptureFixture,
) -> None:
    """The MCP task ID stays out of logs, status, messages, the operator view, and the store."""
    caplog.set_level(logging.DEBUG)
    bridge.core.operator_key = OPERATOR_KEY
    app = build_app(bridge, monkeypatch)

    async def flow(client: Client) -> tuple[str, list[dict]]:
        token, created = await launch_task(client)
        await asyncio.to_thread(dispatch, bridge)
        task_id = created["taskId"]
        await task_get(client, task_id)
        await send(client, tasks.METHOD_CANCEL, {"taskId": task_id})
        await asyncio.to_thread(bridge.core.worker.scan)
        await task_get(client, task_id)
        views = [
            decoded(await call(client, "conversation_status", token=token)),
            decoded(await call(client, "read_messages", token=token)),
        ]
        return task_id, views

    task_id, views = run_client(app, flow)
    with transaction(bridge.workspace) as connection:
        conversation = connection.scalars(select(assignment.c.room)).one()
    envelope = {"version": 1, "key": OPERATOR_KEY, "directory": str(bridge.directory)}
    for action in ("operator_show", "operator_tasks"):
        request = {**envelope, "action": action, "conversation": conversation}
        views.append(bridge.core.apply(REQUEST.validate_python(request)))
    views.append(
        bridge.core.apply(REQUEST.validate_python({**envelope, "action": "operator_list"}))
    )

    assert "fresh run cancelled" in json.dumps(views[1])
    for view in views:
        assert task_id not in json.dumps(view)
    assert task_id not in caplog.text
    with sqlite3.connect(data_dir() / f"{bridge.workspace}.sqlite3") as store:
        assert task_id not in "\n".join(store.iterdump())

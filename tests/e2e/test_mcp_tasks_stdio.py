"""Follow and cancel a fresh run as an MCP task through the installed stdio shim and a real core."""

import asyncio
import json
import os
import sys
import time
from pathlib import Path
from typing import Any

from entry_points import shim_command
from mcp import Client, StdioServerParameters
from mcp.client.extension import advertise
from mcp.types import Request
from pydantic import TypeAdapter

# pytest registers the imported fixture for this module.
from test_stdio_conversation import TIMEOUT, bridge_home  # noqa: F401

FIXTURE = Path(__file__).parents[1] / "fixtures" / "fake_run_plugin.py"
TASKS_EXTENSION = "io.modelcontextprotocol/tasks"
RAW_RESULT = TypeAdapter(dict[str, Any])


def register_fixture_plugin(home: Path) -> None:
    """Register the fake fresh-run plugin in the test core's private configuration."""
    config = home / "plugins.json"
    plugin = {"command": [sys.executable, str(FIXTURE)], "model": "test/model"}
    config.write_text(json.dumps({"plugins": {"fixture": plugin}}), encoding="utf-8")
    config.chmod(0o600)


async def send(client: Client, method: str, params: dict[str, Any]) -> dict[str, Any]:
    """Send a request and return the raw result, which may be an MCP task."""
    request = Request[dict[str, Any], str](method=method, params=params)
    return await client.session.send_request(request, RAW_RESULT)


async def wait_for(client: Client, task_id: str, status: str, message: str) -> dict[str, Any]:
    """Poll tasks/get until the MCP task has the status and the status message."""
    deadline = time.monotonic() + TIMEOUT
    while True:
        task = await send(client, "tasks/get", {"taskId": task_id})
        if task["status"] == status and task.get("statusMessage") == message:
            return task
        assert time.monotonic() < deadline, task
        await asyncio.sleep(0.2)


def test_stdio_mcp_task_runs_and_cancels(
    bridge_home: Path,  # noqa: F811 - pytest fixture argument
) -> None:
    """A 2026-07-28 client gets an MCP task, sees the run start, and cancels it."""
    register_fixture_plugin(bridge_home)

    async def run() -> None:
        workspace = bridge_home / "workspace"
        workspace.mkdir()
        parameters = StdioServerParameters(
            command=shim_command(),
            args=["--workspace", str(workspace)],
            env={"AGENTCY_HOME": str(bridge_home), "PATH": os.defpath},
            cwd=workspace,
        )
        extensions = [advertise(TASKS_EXTENSION, {})]
        async with Client(
            parameters, mode="auto", extensions=extensions, read_timeout_seconds=TIMEOUT
        ) as client:
            assert client.protocol_version == "2026-07-28"
            created = await send(
                client,
                "tools/call",
                {
                    "name": "create_conversation",
                    "arguments": {"brief": "Tasks", "your_name": "Lead", "invitees": ["W"]},
                },
            )
            token = json.loads(created["content"][0]["text"])["token"]
            launch = {"token": token, "name": "Fresh", "plugin": "fixture"}
            task = await send(
                client, "tools/call", {"name": "launch_participant", "arguments": launch}
            )
            assert task["resultType"] == "task"
            assert task["status"] == "working"
            task_id = task["taskId"]
            await wait_for(client, task_id, "working", "The fresh run for Fresh is running.")
            cancel = await send(client, "tasks/cancel", {"taskId": task_id})
            assert cancel["resultType"] == "complete"
            await wait_for(client, task_id, "cancelled", "The fresh run for Fresh was cancelled.")
        assert task_id not in (bridge_home / "core.log").read_text()

    asyncio.run(run())

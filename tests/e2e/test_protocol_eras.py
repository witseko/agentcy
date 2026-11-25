"""Run one conversation round trip with legacy and 2026-07-28 MCP clients on stdio and HTTP."""

import asyncio
import json
import os
import sysconfig
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from pathlib import Path
from typing import Any

import httpx2
import pytest
from mcp import Client, StdioServerParameters
from mcp.client.streamable_http import streamable_http_client
from mcp.types import TextContent

# pytest registers the imported fixtures for this module.
from test_http_bridge import ACCESS_TOKEN, TIMEOUT, http_bridge  # noqa: F401
from test_stdio_conversation import bridge_home  # noqa: F401

LEGACY_VERSION = "2025-11-25"
MODERN_VERSION = "2026-07-28"
EXPECTED_VERSION = {"legacy": LEGACY_VERSION, "auto": MODERN_VERSION}


@asynccontextmanager
async def connect_stdio(home: Path, mode: str) -> AsyncIterator[Client]:
    """Start the installed CLI shim on stdio and connect in the given protocol mode."""
    workspace = home / "workspace"
    workspace.mkdir(exist_ok=True)
    parameters = StdioServerParameters(
        command=str(Path(sysconfig.get_path("scripts")) / "agentcy"),
        args=["--workspace", str(workspace)],
        env={"AGENTCY_HOME": str(home), "PATH": os.defpath},
        cwd=workspace,
    )
    async with Client(parameters, mode=mode, read_timeout_seconds=TIMEOUT) as client:
        yield client


@asynccontextmanager
async def connect_http(url: str, mode: str) -> AsyncIterator[Client]:
    """Connect to the HTTP bridge with the access token in the given protocol mode."""
    headers = {"Authorization": f"Bearer {ACCESS_TOKEN}"}
    async with (
        httpx2.AsyncClient(headers=headers, timeout=TIMEOUT) as http_client,
        Client(
            streamable_http_client(url, http_client=http_client),
            mode=mode,
            read_timeout_seconds=TIMEOUT,
        ) as client,
    ):
        yield client


async def call(client: Client, tool: str, **arguments: Any) -> dict[str, Any]:
    """Decode the bridge's JSON result from MCP text content."""
    result = await client.call_tool(tool, arguments)
    assert not result.is_error, f"{tool} failed: {result.content}"
    (content,) = result.content
    assert isinstance(content, TextContent)
    return json.loads(content.text)


async def round_trip(lead: Client, worker: Client, mode: str) -> None:
    """Create, join, post, and read, then check the negotiated protocol version."""
    assert lead.protocol_version == worker.protocol_version == EXPECTED_VERSION[mode]
    created = await call(
        lead, "create_conversation", brief="Eras", your_name="Lead", invitees=["W"]
    )
    joined = await call(worker, "join_conversation", code=created["invitations"]["W"])
    assert joined["conversation"] == created["conversation"]
    posted = await call(
        worker,
        "post_message",
        token=joined["token"],
        submission_id="hello",
        body="Hi.",
        kind="note",
    )
    page = await call(lead, "read_messages", token=created["token"], wait_seconds=2)
    assert [(message["id"], message["label"]) for message in page["messages"]] == [
        (posted["message_id"], "W")
    ]


async def server_surface(
    client: Client,
) -> tuple[str | None, list[dict[str, Any]], list[dict[str, Any]]]:
    """Return the instructions, the tool list, and the resource list as the client sees them."""
    tools = await client.list_tools()
    resources = await client.list_resources()
    return (
        client.instructions,
        [tool.model_dump(by_alias=True, mode="json") for tool in tools.tools],
        [resource.model_dump(by_alias=True, mode="json") for resource in resources.resources],
    )


@pytest.mark.parametrize("mode", ["legacy", "auto"])
def test_stdio_round_trip_in_both_protocol_eras(
    bridge_home: Path,  # noqa: F811 - pytest fixture argument
    mode: str,
) -> None:
    """Two stdio shims in the same protocol era complete a conversation round trip."""

    async def run() -> None:
        async with (
            connect_stdio(bridge_home, mode) as lead,
            connect_stdio(bridge_home, mode) as worker,
        ):
            await round_trip(lead, worker, mode)

    asyncio.run(run())


@pytest.mark.parametrize("mode", ["legacy", "auto"])
def test_http_round_trip_in_both_protocol_eras(
    http_bridge: str,  # noqa: F811 - pytest fixture argument
    mode: str,
) -> None:
    """Two HTTP clients in the same protocol era complete a conversation round trip."""

    async def run() -> None:
        async with (
            connect_http(http_bridge, mode) as lead,
            connect_http(http_bridge, mode) as worker,
        ):
            await round_trip(lead, worker, mode)

    asyncio.run(run())


def test_both_eras_see_the_same_instructions_tools_and_resources(
    bridge_home: Path,  # noqa: F811 - pytest fixture argument
    http_bridge: str,  # noqa: F811 - pytest fixture argument
) -> None:
    """A 2026-07-28 client gets the same instructions, tools, and resources as a legacy client."""

    async def run() -> None:
        async with (
            connect_stdio(bridge_home, "legacy") as legacy,
            connect_stdio(bridge_home, "auto") as modern,
        ):
            assert await server_surface(modern) == await server_surface(legacy)
        async with (
            connect_http(http_bridge, "legacy") as legacy,
            connect_http(http_bridge, "auto") as modern,
        ):
            assert await server_surface(modern) == await server_surface(legacy)

    asyncio.run(run())

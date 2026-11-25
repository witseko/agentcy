"""Join a local conversation from a remote-style HTTP client through the installed bridge."""

import asyncio
import os
import socket
import subprocess
import sysconfig
import time
from collections.abc import AsyncIterator, Iterator
from contextlib import asynccontextmanager
from pathlib import Path

import httpx2
import pytest
from mcp import ClientSession
from mcp.client.streamable_http import streamable_http_client

# pytest registers the imported bridge_home fixture for this module.
from test_stdio_conversation import bridge_home, call, connect  # noqa: F401

TIMEOUT = 10
ACCESS_TOKEN = "e2e-access-token-" + "0" * 32
PUBLIC_HOST = "bridge.example.com"
BRIEF = "Check the retry behavior"
INITIALIZE = {
    "jsonrpc": "2.0",
    "id": 1,
    "method": "initialize",
    "params": {
        "protocolVersion": "2025-03-26",
        "capabilities": {},
        "clientInfo": {"name": "test", "version": "0"},
    },
}
MCP_HEADERS = {
    "Accept": "application/json, text/event-stream",
    "Authorization": f"Bearer {ACCESS_TOKEN}",
}


def free_port() -> int:
    """Ask the system for a loopback port that is free now."""
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        return probe.getsockname()[1]


@pytest.fixture
def http_bridge(bridge_home: Path) -> Iterator[str]:  # noqa: F811 - pytest fixture argument
    """Run the installed CLI in HTTP mode for one workspace and yield its MCP URL."""
    workspace = bridge_home / "workspace"
    workspace.mkdir()
    port = free_port()
    command = Path(sysconfig.get_path("scripts")) / "agentcy"
    log_path = bridge_home / "http.log"
    with log_path.open("w") as log:
        # The test starts the project's own CLI with a fixed argument list and no shell.
        bridge = subprocess.Popen(  # noqa: S603
            [
                str(command),
                "--workspace",
                str(workspace),
                "--http",
                str(port),
                "--http-public-host",
                PUBLIC_HOST,
            ],
            cwd=workspace,
            env={
                "AGENTCY_HOME": str(bridge_home),
                "AGENTCY_HTTP_TOKEN": ACCESS_TOKEN,
                "PATH": os.defpath,
            },
            stdin=subprocess.DEVNULL,
            stdout=log,
            stderr=subprocess.STDOUT,
        )
    try:
        deadline = time.monotonic() + TIMEOUT
        while True:
            try:
                socket.create_connection(("127.0.0.1", port), timeout=0.2).close()
                break
            except OSError:
                if bridge.poll() is not None or time.monotonic() >= deadline:
                    pytest.fail(f"HTTP bridge did not start:\n{log_path.read_text()}")
                time.sleep(0.05)
        yield f"http://127.0.0.1:{port}/mcp"
    finally:
        bridge.terminate()
        try:
            bridge.wait(timeout=5)
        except subprocess.TimeoutExpired:
            bridge.kill()
            bridge.wait(timeout=2)


@asynccontextmanager
async def connect_http(url: str) -> AsyncIterator[ClientSession]:
    """Initialize an MCP session over HTTP with the access token."""
    headers = {"Authorization": f"Bearer {ACCESS_TOKEN}"}
    async with (
        httpx2.AsyncClient(headers=headers, timeout=TIMEOUT) as http_client,
        streamable_http_client(url, http_client=http_client) as (reader, writer),
        ClientSession(reader, writer, read_timeout_seconds=TIMEOUT) as client,
    ):
        yield client


def test_http_client_joins_a_conversation_created_over_stdio(
    bridge_home: Path,  # noqa: F811 - pytest fixture argument
    http_bridge: str,
) -> None:
    """A remote chat shares the core and the workspace of the local chats."""

    async def join_and_post() -> None:
        workspace = bridge_home / "workspace"
        async with connect(bridge_home, workspace) as lead, connect_http(http_bridge) as remote:
            initialized = await remote.initialize()
            assert initialized.instructions is not None
            assert "This bridge runs on your user's computer" in initialized.instructions
            assert "do not create a conversation" not in initialized.instructions

            created = await call(
                lead, "create_conversation", brief=BRIEF, your_name="Lead", invitees=["Remote"]
            )
            joined = await call(remote, "join_conversation", code=created["invitations"]["Remote"])
            assert joined["conversation"] == created["conversation"]
            assert joined["brief"] == BRIEF

            posted = await call(
                remote,
                "post_message",
                token=joined["token"],
                submission_id="hello",
                body="Joined over HTTP.",
                kind="note",
            )
            page = await call(lead, "read_messages", token=created["token"], wait_seconds=2)
            assert [(message["id"], message["label"]) for message in page["messages"]] == [
                (posted["message_id"], "Remote")
            ]

    asyncio.run(join_and_post())


@pytest.mark.parametrize(
    "authorization", [None, "Bearer wrong-token-" + "0" * 32, f"Basic {ACCESS_TOKEN}"]
)
def test_http_request_without_the_access_token_gets_401(
    http_bridge: str, authorization: str | None
) -> None:
    """A caller without the access token gets no MCP session."""
    headers = {"Accept": MCP_HEADERS["Accept"]}
    if authorization is not None:
        headers["Authorization"] = authorization

    response = httpx2.post(http_bridge, json=INITIALIZE, headers=headers, timeout=TIMEOUT)

    assert response.status_code == 401
    assert response.text == "Unauthorized"
    assert "mcp-session-id" not in response.headers


@pytest.mark.parametrize(("host", "status"), [(PUBLIC_HOST, 200), ("attacker.example.net", 421)])
def test_http_bridge_accepts_only_the_configured_public_host(
    http_bridge: str, host: str, status: int
) -> None:
    """A valid token does not help a request that arrives under an unknown host name."""
    response = httpx2.post(
        http_bridge, json=INITIALIZE, headers={**MCP_HEADERS, "Host": host}, timeout=TIMEOUT
    )

    assert response.status_code == status

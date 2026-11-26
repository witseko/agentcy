"""Drive the Go spike server with the unmodified mcp 2.3.0 Python client and check spike S1.

Run from the repo root after building the server:

    (cd spikes/mcp && CGO_ENABLED=0 go build -trimpath -o "$BIN" .)
    uv run --locked python spikes/mcp/driver.py --server "$BIN"

It starts the server on stdio (through protocol/mcp-transcripts/tee_stdio.py, so the
wire is logged) and on HTTP at 127.0.0.1, connects in both protocol eras, prints one
PASS or FAIL line per check with its evidence, and exits non-zero if any check fails.
"""

import argparse
import asyncio
import json
import os
import socket
import subprocess
import sys
import tempfile
import time
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from pathlib import Path
from typing import Any

import httpx2
from mcp import Client, StdioServerParameters
from mcp.client.extension import advertise
from mcp.client.streamable_http import streamable_http_client
from mcp.shared.exceptions import MCPError
from mcp.types import ElicitResult, Request, TextContent
from pydantic import TypeAdapter

REPO = Path(__file__).resolve().parents[2]
TRANSCRIPTS = REPO / "protocol" / "mcp-transcripts"
TEE = TRANSCRIPTS / "tee_stdio.py"
GOLDEN_TOOLS = TRANSCRIPTS / "tools-list.2026-07-28.json"
TIMEOUT = 10
TASKS_EXTENSION = "io.modelcontextprotocol/tasks"
RAW_RESULT = TypeAdapter(dict[str, Any])
ERAS = {"legacy": "2025-11-25", "auto": "2026-07-28"}
ACCESS_TOKEN = "spike-access-token-" + "0" * 32
# The raw initialize body from tests/e2e/test_http_bridge.py:26-35.
INITIALIZE_2025_03_26 = {
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

results: list[tuple[str, bool, str]] = []


def check(name: str, passed: bool, evidence: str) -> None:
    """Record and print one check."""
    results.append((name, passed, evidence))
    print(f"{'PASS' if passed else 'FAIL'}  {name}: {evidence}", flush=True)


async def send(client: Client, method: str, params: dict[str, Any]) -> dict[str, Any]:
    """Send a request and return the raw result (the helper of test_mcp_tasks_stdio.py:35-38)."""
    request = Request[dict[str, Any], str](method=method, params=params)
    return await client.session.send_request(request, RAW_RESULT)


class Elicitor:
    """Answer form elicitations with a fixed action and count them."""

    def __init__(self) -> None:
        self.action = "accept"
        self.seen: list[str] = []

    async def __call__(self, context: Any, params: Any) -> ElicitResult:
        self.seen.append(params.message)
        if self.action == "accept":
            return ElicitResult(action="accept", content={"confirm": True})
        return ElicitResult(action=self.action)


async def surface_checks(client: Client, label: str, era: str, elicitor: Elicitor) -> None:
    """Run every per-session check on a connected client."""
    check(f"initialize {label}", client.protocol_version == era, f"protocol_version={client.protocol_version}")

    tools = await client.list_tools()
    listed = [tool.model_dump(by_alias=True, mode="json", exclude_none=True) for tool in tools.tools]
    golden = json.loads(GOLDEN_TOOLS.read_text())["tools"]
    by_name = {tool["name"]: tool for tool in listed}
    golden_by_name = {tool["name"]: tool for tool in golden}
    check(
        f"tools/list verbatim {label}",
        by_name == golden_by_name,
        f"{len(listed)} tools, each equal to the Python shim's recorded entry: {by_name == golden_by_name}; "
        f"same order as Python: {[tool['name'] for tool in listed] == [tool['name'] for tool in golden]}",
    )

    called = await client.call_tool("post_message", {"token": "t", "body": "b", "kind": "note"})
    check(f"tools/call {label}", not called.is_error, f"content={called.content[0].text}")

    skills = await send(client, "skills/list", {})
    valid = isinstance(skills.get("skills"), list) and skills["skills"][0]["uri"].startswith("skill://")
    check(f"skills/list {label}", valid, json.dumps(skills))
    skill = await send(client, "skills/get", {"uri": skills["skills"][0]["uri"]})
    check(f"skills/get {label}", skill["skill"]["uri"] == skills["skills"][0]["uri"], json.dumps(skill))

    if era == "2026-07-28":
        task = await send(
            client,
            "tools/call",
            {"name": "launch_participant", "arguments": {"token": "t", "name": "Fresh", "plugin": "fixture"}},
        )
        check(
            f"task-shaped tools/call result {label}",
            task.get("resultType") == "task" and task.get("status") == "working",
            json.dumps(task),
        )
        got = await send(client, "tasks/get", {"taskId": task["taskId"]})
        check(
            f"raw tasks/get {label}",
            got.get("taskId") == task["taskId"] and got.get("status") == "working",
            json.dumps(got),
        )
        cancel = await send(client, "tasks/cancel", {"taskId": task["taskId"]})
        check(f"tasks/cancel {label}", cancel.get("resultType") == "complete", json.dumps(cancel))
    else:
        try:
            await send(client, "tasks/get", {"taskId": "unknown"})
            check(f"tasks/get unbound in legacy era {label}", False, "got a result")
        except MCPError as error:
            check(
                f"tasks/get unbound in legacy era {label}",
                error.code == -32601,
                f"error code={error.code} message={error.message!r} (Python shim: -32601 'Method not found')",
            )

    elicitor.seen.clear()
    elicitor.action = "accept"
    closed = await client.call_tool("close_conversation", {"token": "t"})
    (content,) = closed.content
    assert isinstance(content, TextContent)
    check(
        f"form elicitation accept {label}",
        not closed.is_error and json.loads(content.text) == {"closed": True} and elicitor.seen == ["Close this conversation?"],
        f"elicitations={elicitor.seen} is_error={closed.is_error} text={content.text!r}",
    )
    elicitor.seen.clear()
    elicitor.action = "decline"
    declined = await client.call_tool("close_conversation", {"token": "t"})
    check(
        f"form elicitation decline {label}",
        declined.is_error and len(elicitor.seen) == 1,
        f"elicitations={len(elicitor.seen)} is_error={declined.is_error} text={declined.content[0].text!r}",
    )


async def run_stdio(server: Path, scratch: Path) -> None:
    """Check both eras on stdio, logging the wire through the tee proxy."""
    for mode, era in ERAS.items():
        log_path = scratch / f"go-stdio.{era}.jsonl"
        parameters = StdioServerParameters(
            command=sys.executable,
            args=[str(TEE), str(log_path), "--", str(server), "-tools", str(GOLDEN_TOOLS)],
            env={"PATH": os.defpath},
        )
        elicitor = Elicitor()
        async with Client(
            parameters,
            mode=mode,
            extensions=[advertise(TASKS_EXTENSION, {})],
            elicitation_callback=elicitor,
            read_timeout_seconds=TIMEOUT,
        ) as client:
            await surface_checks(client, f"stdio {era}", era, elicitor)
        print(f"      wire log: {log_path}")


def free_port() -> int:
    """Ask the system for a loopback port that is free now."""
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        return probe.getsockname()[1]


@asynccontextmanager
async def http_server(server: Path, scratch: Path) -> AsyncIterator[str]:
    """Run the Go server in HTTP mode on 127.0.0.1 and yield its MCP URL."""
    port = free_port()
    log = (scratch / "go-http.log").open("w")
    process = subprocess.Popen(  # noqa: S603 - our own spike binary, fixed arguments
        [str(server), "-tools", str(GOLDEN_TOOLS), "-http", str(port)],
        env={"PATH": os.defpath, "SPIKE_HTTP_TOKEN": ACCESS_TOKEN},
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
                assert process.poll() is None and time.monotonic() < deadline, "HTTP server did not start"
                time.sleep(0.05)
        yield f"http://127.0.0.1:{port}/mcp"
    finally:
        process.terminate()
        process.wait(timeout=5)
        log.close()


async def run_http(url: str) -> None:
    """Check both eras over streamable HTTP, then the raw 2025-03-26 initialize and the gate."""
    for mode, era in ERAS.items():
        elicitor = Elicitor()
        async with (
            httpx2.AsyncClient(headers={"Authorization": f"Bearer {ACCESS_TOKEN}"}, timeout=TIMEOUT) as http_client,
            Client(
                streamable_http_client(url, http_client=http_client),
                mode=mode,
                extensions=[advertise(TASKS_EXTENSION, {})],
                elicitation_callback=elicitor,
                read_timeout_seconds=TIMEOUT,
            ) as client,
        ):
            await surface_checks(client, f"http {era}", era, elicitor)

    response = httpx2.post(url, json=INITIALIZE_2025_03_26, headers=MCP_HEADERS, timeout=TIMEOUT)
    body = response.text
    if response.headers.get("content-type", "").startswith("text/event-stream"):
        body = "".join(line.removeprefix("data:").strip() for line in body.splitlines() if line.startswith("data:"))
    message = json.loads(body)
    version = message.get("result", {}).get("protocolVersion")
    check(
        "initialize 2025-03-26 raw POST http",
        response.status_code == 200 and version == "2025-03-26",
        f"status={response.status_code} content-type={response.headers.get('content-type')} "
        f"mcp-session-id present={'mcp-session-id' in response.headers} result.protocolVersion={version}",
    )
    unauthorized = httpx2.post(
        url, json=INITIALIZE_2025_03_26, headers={"Accept": MCP_HEADERS["Accept"]}, timeout=TIMEOUT
    )
    check(
        "bearer gate rejects a missing token http",
        unauthorized.status_code == 401 and unauthorized.text == "Unauthorized",
        f"status={unauthorized.status_code} body={unauthorized.text!r}",
    )


async def main() -> None:
    """Run all checks and exit non-zero on any failure."""
    parser = argparse.ArgumentParser()
    parser.add_argument("--server", type=Path, required=True, help="the built spike-mcp binary")
    parser.add_argument("--scratch", type=Path, default=None, help="where wire logs go")
    arguments = parser.parse_args()
    scratch = arguments.scratch or Path(tempfile.mkdtemp(prefix="spike-mcp-"))
    scratch.mkdir(parents=True, exist_ok=True)
    await run_stdio(arguments.server, scratch)
    async with http_server(arguments.server, scratch) as url:
        await run_http(url)
    failed = [name for name, passed, _ in results if not passed]
    print(f"\n{len(results) - len(failed)} passed, {len(failed)} failed")
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    asyncio.run(main())

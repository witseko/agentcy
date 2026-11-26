"""Record wire transcripts of the Python stdio shim and HTTP bridge in both protocol eras.

Run from the repo root: `uv run --locked python protocol/mcp-transcripts/capture.py`.

The script starts a private core in a fresh temporary AGENTCY_HOME, drives the installed
`agentcy` shim with the mcp SDK `Client` (the same client the e2e tests use), logs every
JSON-RPC message, replaces credentials with placeholders, and writes the JSON files next
to this script. It never touches the user's real data directory.
"""

import asyncio
import json
import os
import re
import shutil
import socket
import subprocess
import sys
import sysconfig
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
from mcp.types import ElicitResult, Request
from pydantic import TypeAdapter

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[1]
TEE = HERE / "tee_stdio.py"
FIXTURE = REPO / "tests" / "fixtures" / "fake_run_plugin.py"
SHIM = Path(sysconfig.get_path("scripts")) / "agentcy"
TIMEOUT = 15
TASKS_EXTENSION = "io.modelcontextprotocol/tasks"
RAW_RESULT = TypeAdapter(dict[str, Any])
ERAS = {"legacy": "2025-11-25", "auto": "2026-07-28"}
# A fixed test bearer value; it is replaced by a placeholder in the saved files.
ACCESS_TOKEN = "capture-access-token-" + "0" * 32
LOGGED_HEADERS = {
    "content-type",
    "accept",
    "mcp-session-id",
    "mcp-protocol-version",
    "mcp-method",
    "mcp-name",
    "authorization",
    "last-event-id",
}
SECRET_KEYS = {
    "token": "TOKEN",
    "code": "JOIN_CODE",
    "taskId": "TASK_ID",
    "task_id": "TASK_ID",
    "requestState": "REQUEST_STATE",
}
ICON = re.compile(r"data:image/png;base64,[A-Za-z0-9+/=]+")


class Recorder:
    """Collect wire messages, grouped into named steps."""

    def __init__(self) -> None:
        self.entries: list[dict[str, Any]] = []

    def mark(self, step: str) -> None:
        """Start a new step; later messages belong to it."""
        self.entries.append({"step": step})

    def add(self, entry: dict[str, Any]) -> None:
        """Record one wire entry."""
        self.entries.append(entry)

    def merge_stdio_log(self, path: Path) -> None:
        """Append the lines a tee proxy wrote, keeping the step markers written into the same file."""
        for line in path.read_text(encoding="utf-8").splitlines():
            self.entries.append(json.loads(line))
        path.unlink()

    def steps(self) -> list[dict[str, Any]]:
        """Return the entries grouped by step."""
        grouped: list[dict[str, Any]] = []
        for entry in self.entries:
            if "step" in entry:
                grouped.append({"step": entry["step"], "messages": []})
            else:
                grouped[-1]["messages"].append(entry)
        return grouped


class LoggingStream(httpx2.AsyncByteStream):
    """Pass response bytes through and log each JSON-RPC message as it arrives."""

    def __init__(self, inner: httpx2.AsyncByteStream, is_sse: bool, recorder: Recorder) -> None:
        self.inner = inner
        self.is_sse = is_sse
        self.recorder = recorder
        self.buffer = b""

    async def __aiter__(self) -> AsyncIterator[bytes]:
        async for chunk in self.inner:
            self.buffer += chunk
            if self.is_sse:
                self._flush_events()
            yield chunk
        if not self.is_sse and self.buffer.strip():
            self.recorder.add(
                {"direction": "server_to_client", "message": json.loads(self.buffer)}
            )

    def _flush_events(self) -> None:
        """Log every complete SSE event in the buffer."""
        normalized = self.buffer.replace(b"\r\n", b"\n")
        *events, rest = normalized.split(b"\n\n")
        self.buffer = rest
        for event in events:
            fields: dict[str, str] = {}
            data = []
            for line in event.decode("utf-8").split("\n"):
                key, _, value = line.partition(":")
                value = value.removeprefix(" ")
                if key == "data":
                    data.append(value)
                elif key:
                    fields[key] = value
            entry: dict[str, Any] = {"direction": "server_to_client", "sse": fields}
            if data and "".join(data).strip():
                entry["message"] = json.loads("\n".join(data))
            self.recorder.add(entry)

    async def aclose(self) -> None:
        await self.inner.aclose()


class LoggingTransport(httpx2.AsyncBaseTransport):
    """Log each HTTP request, response status and headers, and every JSON-RPC body."""

    def __init__(self, recorder: Recorder) -> None:
        self.inner = httpx2.AsyncHTTPTransport()
        self.recorder = recorder

    async def handle_async_request(self, request: httpx2.Request) -> httpx2.Response:
        body = request.content
        entry: dict[str, Any] = {
            "direction": "client_to_server",
            "http": {
                "method": request.method,
                "path": request.url.path,
                "headers": pick_headers(request.headers),
            },
        }
        if body:
            entry["message"] = json.loads(body)
        self.recorder.add(entry)
        response = await self.inner.handle_async_request(request)
        content_type = response.headers.get("content-type", "")
        self.recorder.add(
            {
                "direction": "server_to_client",
                "http": {"status": response.status_code, "headers": pick_headers(response.headers)},
            }
        )
        stream = LoggingStream(response.stream, "text/event-stream" in content_type, self.recorder)
        return httpx2.Response(
            response.status_code,
            headers=response.headers,
            stream=stream,
            request=request,
            extensions=response.extensions,
        )

    async def aclose(self) -> None:
        await self.inner.aclose()


def pick_headers(headers: httpx2.Headers) -> dict[str, str]:
    """Keep only the headers that matter for MCP."""
    return {key.lower(): value for key, value in headers.items() if key.lower() in LOGGED_HEADERS}


async def send(client: Client, method: str, params: dict[str, Any]) -> dict[str, Any]:
    """Send a request and return the raw result (as test_mcp_tasks_stdio.py does)."""
    request = Request[dict[str, Any], str](method=method, params=params)
    return await client.session.send_request(request, RAW_RESULT)


async def accept_confirmation(context: Any, params: Any) -> ElicitResult:
    """Answer the close confirmation form with confirm=true."""
    return ElicitResult(action="accept", content={"confirm": True})


async def scenario(client: Client, recorder: Recorder, mark: Any, modern: bool) -> None:
    """Run the recorded steps on a connected client."""
    mark("tools/list")
    await client.list_tools()
    mark("tools/call create_conversation")
    created = await client.call_tool(
        "create_conversation", {"brief": "Transcript", "your_name": "Lead", "invitees": ["W"]}
    )
    token = json.loads(created.content[0].text)["token"]
    mark("skills/list")
    skills = await send(client, "skills/list", {})
    mark("skills/get")
    await send(client, "skills/get", {"uri": skills["skills"][0]["uri"]})
    if modern:
        mark("tools/call launch_participant (task-shaped result)")
        task = await send(
            client,
            "tools/call",
            {
                "name": "launch_participant",
                "arguments": {"token": token, "name": "Fresh", "plugin": "fixture"},
            },
        )
        mark("tasks/get")
        await send(client, "tasks/get", {"taskId": task["taskId"]})
        mark("tasks/cancel")
        await send(client, "tasks/cancel", {"taskId": task["taskId"]})
    else:
        mark("tasks/get (not bound in this era)")
        try:
            await send(client, "tasks/get", {"taskId": "unknown-task"})
        except Exception as error:  # noqa: BLE001 - the error reply is what we record
            print(f"  tasks/get in legacy era: {error}")
    mark("tools/call close_conversation (elicitation)")
    closed = await client.call_tool("close_conversation", {"token": token})
    assert not closed.is_error, closed


@asynccontextmanager
async def private_core() -> AsyncIterator[Path]:
    """Start a core in a fresh AGENTCY_HOME with the fake fresh-run plugin registered."""
    # A short /tmp path keeps the Unix socket path under the macOS limit, as the e2e tests do.
    home = Path(tempfile.mkdtemp(prefix="agentcy-cap-", dir="/tmp"))
    config = home / "plugins.json"
    plugin = {"command": [sys.executable, str(FIXTURE)], "model": "test/model"}
    config.write_text(json.dumps({"plugins": {"fixture": plugin}}), encoding="utf-8")
    config.chmod(0o600)
    (home / "workspace").mkdir()
    log = (home / "core.log").open("w")
    core = subprocess.Popen(  # noqa: S603
        [sys.executable, "-I", "-m", "agentcy.mcp.core"],
        cwd=home,
        env={"AGENTCY_HOME": str(home), "PATH": os.defpath},
        stdin=subprocess.DEVNULL,
        stdout=log,
        stderr=subprocess.STDOUT,
    )
    try:
        deadline = time.monotonic() + TIMEOUT
        while not (home / "core.sock").exists():
            assert core.poll() is None and time.monotonic() < deadline, "core did not start"
            time.sleep(0.02)
        yield home
    finally:
        core.terminate()
        try:
            core.wait(timeout=5)
        except subprocess.TimeoutExpired:
            core.kill()
            core.wait()
        log.close()
        await asyncio.sleep(1)  # let a cancelled fixture plugin exit before cleanup
        shutil.rmtree(home, ignore_errors=True)


async def capture_stdio(home: Path, mode: str) -> dict[str, Any]:
    """Record one stdio session in the given client mode."""
    recorder = Recorder()
    log_path = home / f"stdio-{mode}.jsonl"

    def mark(step: str) -> None:
        with log_path.open("a", encoding="utf-8") as log:
            log.write(json.dumps({"step": step}) + "\n")

    workspace = home / "workspace"
    parameters = StdioServerParameters(
        command=sys.executable,
        args=[str(TEE), str(log_path), "--", str(SHIM), "--workspace", str(workspace)],
        env={"AGENTCY_HOME": str(home), "PATH": os.defpath},
        cwd=workspace,
    )
    mark("connect")
    async with Client(
        parameters,
        mode=mode,
        extensions=[advertise(TASKS_EXTENSION, {})],
        elicitation_callback=accept_confirmation,
        read_timeout_seconds=TIMEOUT,
    ) as client:
        assert client.protocol_version == ERAS[mode], client.protocol_version
        await scenario(client, recorder, mark, modern=mode == "auto")
    recorder.merge_stdio_log(log_path)
    return transcript("stdio", mode, recorder)


def free_port() -> int:
    """Ask the system for a loopback port that is free now."""
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        return probe.getsockname()[1]


@asynccontextmanager
async def http_bridge(home: Path) -> AsyncIterator[str]:
    """Run the shim in HTTP mode on 127.0.0.1 and yield its MCP URL."""
    port = free_port()
    log = (home / "http.log").open("w")
    bridge = subprocess.Popen(  # noqa: S603
        [str(SHIM), "--workspace", str(home / "workspace"), "--http", str(port)],
        cwd=home / "workspace",
        env={"AGENTCY_HOME": str(home), "AGENTCY_HTTP_TOKEN": ACCESS_TOKEN, "PATH": os.defpath},
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
                assert bridge.poll() is None and time.monotonic() < deadline, "no HTTP bridge"
                time.sleep(0.05)
        yield f"http://127.0.0.1:{port}/mcp"
    finally:
        bridge.terminate()
        try:
            bridge.wait(timeout=5)
        except subprocess.TimeoutExpired:
            bridge.kill()
            bridge.wait()
        log.close()


async def capture_http(url: str, mode: str) -> dict[str, Any]:
    """Record one streamable HTTP session in the given client mode."""
    recorder = Recorder()
    headers = {"Authorization": f"Bearer {ACCESS_TOKEN}"}
    recorder.mark("connect")
    async with (
        httpx2.AsyncClient(
            headers=headers, timeout=TIMEOUT, transport=LoggingTransport(recorder)
        ) as http_client,
        Client(
            streamable_http_client(url, http_client=http_client),
            mode=mode,
            extensions=[advertise(TASKS_EXTENSION, {})],
            elicitation_callback=accept_confirmation,
            read_timeout_seconds=TIMEOUT,
        ) as client,
    ):
        assert client.protocol_version == ERAS[mode], client.protocol_version
        await scenario(client, recorder, recorder.mark, modern=mode == "auto")
        recorder.mark("disconnect")
    return transcript("http", mode, recorder)


async def capture_http_2025_03_26(url: str) -> dict[str, Any]:
    """Record the raw 2025-03-26 initialize POST from tests/e2e/test_http_bridge.py."""
    recorder = Recorder()
    recorder.mark("initialize 2025-03-26 (raw POST)")
    initialize = {
        "jsonrpc": "2.0",
        "id": 1,
        "method": "initialize",
        "params": {
            "protocolVersion": "2025-03-26",
            "capabilities": {},
            "clientInfo": {"name": "test", "version": "0"},
        },
    }
    headers = {
        "Accept": "application/json, text/event-stream",
        "Authorization": f"Bearer {ACCESS_TOKEN}",
    }
    async with httpx2.AsyncClient(timeout=TIMEOUT, transport=LoggingTransport(recorder)) as http:
        response = await http.post(url, json=initialize, headers=headers)
        await response.aread()
        assert response.status_code == 200, response.status_code
    return {
        "transport": "http",
        "era": "2025-03-26",
        "client": "raw httpx2 POST, body from tests/e2e/test_http_bridge.py:26-35",
        "steps": recorder.steps(),
    }


def transcript(transport: str, mode: str, recorder: Recorder) -> dict[str, Any]:
    """Wrap recorded steps with a header that says how they were made."""
    return {
        "transport": transport,
        "era": ERAS[mode],
        "client": (
            f"mcp 2.3.0 Client(mode={mode!r}, extensions=[advertise({TASKS_EXTENSION!r})], "
            "elicitation_callback=accept confirm=true)"
        ),
        "steps": recorder.steps(),
    }


def collect_secrets(value: Any, found: dict[str, str]) -> None:
    """Find credential values: tokens, join codes, MCP task IDs, session IDs."""
    if isinstance(value, dict):
        for key, item in value.items():
            if key in SECRET_KEYS and isinstance(item, str):
                found.setdefault(item, SECRET_KEYS[key])
            elif key == "invitations" and isinstance(item, dict):
                for code in item.values():
                    if isinstance(code, str):
                        found.setdefault(code, "JOIN_CODE")
            elif key == "mcp-session-id" and isinstance(item, str):
                found.setdefault(item, "SESSION_ID")
            else:
                collect_secrets(item, found)
    elif isinstance(value, list):
        for item in value:
            collect_secrets(item, found)
    elif isinstance(value, str) and value.startswith("{"):
        try:
            collect_secrets(json.loads(value), found)
        except json.JSONDecodeError:
            pass


def redact(document: dict[str, Any], home: Path) -> dict[str, Any]:
    """Replace credentials, the temporary home, and the icon bytes with placeholders."""
    found: dict[str, str] = {}
    collect_secrets(document, found)
    found.pop("unknown-task", None)
    text = json.dumps(document, indent=2, ensure_ascii=False)
    counters: dict[str, int] = {}
    # Longest first, so a value that contains another is replaced whole.
    for secret in sorted(found, key=len, reverse=True):
        kind = found[secret]
        counters[kind] = counters.get(kind, 0) + 1
        text = text.replace(secret, f"<{kind}_{counters[kind]}>")
    text = text.replace(ACCESS_TOKEN, "<BEARER_TOKEN>")
    text = text.replace(str(home), "<AGENTCY_HOME>")
    text = ICON.sub("data:image/png;base64,<ICON_PNG_BASE64>", text)
    return json.loads(text)


def tools_list_result(document: dict[str, Any]) -> dict[str, Any]:
    """Return the full tools/list result message of a transcript."""
    for step in document["steps"]:
        if step["step"] == "tools/list":
            for entry in step["messages"]:
                message = entry.get("message", {})
                if "result" in message:
                    return message["result"]
    raise AssertionError("no tools/list result")


def write(name: str, document: dict[str, Any]) -> None:
    """Write one JSON file next to this script."""
    (HERE / name).write_text(json.dumps(document, indent=2, ensure_ascii=False) + "\n")
    print(f"wrote {name}")


async def main() -> None:
    """Record every transcript."""
    async with private_core() as home:
        for mode, era in ERAS.items():
            document = redact(await capture_stdio(home, mode), home)
            write(f"stdio.{era}.json", document)
            write(f"tools-list.{era}.json", tools_list_result(document))
        async with http_bridge(home) as url:
            for mode, era in ERAS.items():
                write(f"http.{era}.json", redact(await capture_http(url, mode), home))
            write("http.2025-03-26-initialize.json", redact(await capture_http_2025_03_26(url), home))


if __name__ == "__main__":
    asyncio.run(main())

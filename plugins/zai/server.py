"""Run Z.ai workers through OpenCode with durable, credential-free launch records."""

import json
import os
import re
import shutil
import signal
import subprocess
import sys
import time
from pathlib import Path

from mcp.server.fastmcp import FastMCP

from agentcy.executables import absolute_search_path, find_on_path
from agentcy.mcp.fresh_server import FRESH_WORKER_PROMPT
from agentcy.mcp.plugin_contract import (
    AssignmentId,
    Capabilities,
    CostCap,
    RunResult,
    Start,
    Timeout,
    TokenCap,
)
from agentcy.storage.database import data_dir

MCP = FastMCP("agentcy-zai")
JOIN_VARIABLE = "AGENTCY_FRESH_JOIN_CODE"
PROMPT = FRESH_WORKER_PROMPT
ACTIVE = {"starting", "running"}
STATE_INTERVAL = 0.5
STATE_EXPIRY = 15


def run_path(assignment_id: str) -> Path:
    """Find the private state for the assignment ID validated by the MCP model."""
    return data_dir() / "fresh-runs" / assignment_id


def save(path: Path, result: RunResult) -> None:
    """Publish a complete state result atomically for independent inspect calls."""
    temp = path / ".state"
    temp.write_text(result.model_dump_json(), encoding="utf-8")
    temp.replace(path / "state.json")


@MCP.tool()
def capabilities() -> Capabilities:
    """Report the fresh-run protocol version."""
    return Capabilities(version=1)


@MCP.tool()
def inspect(assignment_id: AssignmentId) -> RunResult:
    """Read a stable launch record, or report that its state cannot be confirmed."""
    state = run_path(assignment_id) / "state.json"
    try:
        result = RunResult.model_validate_json(state.read_bytes())
        if result.state in ACTIVE and time.time() - state.stat().st_mtime > STATE_EXPIRY:
            return RunResult(state="unknown")
        return result
    except (OSError, ValueError):
        return RunResult(state="unknown")


@MCP.tool()
def cancel(assignment_id: AssignmentId) -> RunResult:
    """Ask the detached monitor to stop OpenCode and its child processes."""
    result = inspect(assignment_id)
    if result.state in ACTIVE:
        (run_path(assignment_id) / "cancel").touch()
    return result


def sandbox_profile(runtime: Path) -> str:
    """Restrict child writes to private scratch and shield the bridge store."""
    writable = json.dumps(str(runtime))
    root = data_dir()
    private_bridge = json.dumps(str(root))
    socket_path = json.dumps(str(root / "core.sock"))
    return (
        f"(version 1) (allow default) "
        f"(deny file-write* (require-all (require-not (subpath {writable})) "
        '(require-not (literal "/dev/null")))) '
        f"(deny file-read-data (require-all (subpath {private_bridge}) "
        f"(require-not (subpath {writable})) (require-not (literal {socket_path}))))"
    )


def platform_sandbox() -> bool:
    """Refuse to start without verified read-only and bridge-store isolation."""
    return sys.platform == "darwin" and Path("/usr/bin/sandbox-exec").is_file()


def prepare_environment(workspace: Path, runtime: Path, model: str) -> dict[str, str]:
    """Keep OpenCode isolated from the user's project settings and shared server."""
    allowed_tools = ["read", "glob", "grep", "agentcy_*"]
    settings = {
        "update": "disable",
        "providers": {
            model.split("/", 1)[0]: {"settings": {"apiKey": "{env:ZAI_API_KEY}"}},
        },
        "permissions": [
            {"action": "*", "resource": "*", "effect": "deny"},
            *({"action": tool, "resource": "*", "effect": "allow"} for tool in allowed_tools),
        ],
        "mcp": {
            "servers": {
                "agentcy": {
                    "type": "local",
                    "command": [
                        sys.executable,
                        "-E",
                        "-P",
                        "-m",
                        "agentcy.mcp.fresh_server",
                        str(workspace),
                    ],
                    "codemode": False,
                    "timeout": {"startup": 10_000, "catalog": 10_000, "execution": 60_000},
                },
            },
        },
    }
    config = runtime / "config" / "opencode" / "opencode.json"
    config.parent.mkdir(mode=0o700, parents=True)
    config.write_text(json.dumps(settings), encoding="utf-8")
    environment = {
        name: os.environ[name]
        for name in ("LANG", "LC_ALL", "ZAI_API_KEY", JOIN_VARIABLE)
        if name in os.environ
    }
    return {
        **environment,
        "PATH": absolute_search_path(),
        "AGENTCY_HOME": str(data_dir()),
        "OPENCODE_DISABLE_PROJECT_CONFIG": "1",
        "OPENCODE_DISABLE_AUTOUPDATE": "1",
        "XDG_CONFIG_HOME": str(runtime / "config"),
        "XDG_STATE_HOME": str(runtime / "state"),
        "XDG_CACHE_HOME": str(runtime / "cache"),
        "XDG_DATA_HOME": str(runtime / "data"),
        "TMPDIR": str(runtime),
    }


def stop(process: subprocess.Popen) -> None:
    """Stop the private process group and reap its leader."""
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    process.wait()


def parse_usage(output_file: Path) -> tuple[int | None, int | None]:
    """Extract optional input/output token counts from the private OpenCode output file."""
    try:
        text = output_file.read_text(encoding="utf-8", errors="ignore")
    except OSError:
        return None, None
    for line in reversed(text.splitlines()):
        stripped = line.strip()
        if stripped.startswith("{") and stripped.endswith("}"):
            try:
                payload = json.loads(stripped)
            except ValueError:
                continue
            usage = payload.get("usage") if isinstance(payload, dict) else None
            if isinstance(usage, dict):
                inp = usage.get("input_tokens", usage.get("prompt_tokens"))
                out = usage.get("output_tokens", usage.get("completion_tokens"))
                if (
                    isinstance(inp, int)
                    and not isinstance(inp, bool)
                    and isinstance(out, int)
                    and not isinstance(out, bool)
                    and inp >= 0
                    and out >= 0
                ):
                    return inp, out
    match = re.search(
        r"input[_ ]tokens[:=\s]+(\d+).*?output[_ ]tokens[:=\s]+(\d+)",
        text,
        re.IGNORECASE | re.DOTALL,
    )
    if match:
        return int(match.group(1)), int(match.group(2))
    return None, None


def monitor(process: subprocess.Popen, path: Path, timeout: int, output_file: Path) -> RunResult:
    """Keep a durable heartbeat and enforce cancellation even if the core stops."""
    deadline = time.monotonic() + timeout
    try:
        while (exit_code := process.poll()) is None:
            if (path / "cancel").exists():
                return RunResult(state="cancelled")
            if time.monotonic() >= deadline:
                return RunResult(state="timed_out")
            save(path, RunResult(state="running"))
            time.sleep(STATE_INTERVAL)
        input_tokens, output_tokens = parse_usage(output_file)
        return RunResult(
            state="finished" if exit_code == 0 else "failed",
            exit_code=exit_code,
            input_tokens=input_tokens,
            output_tokens=output_tokens,
        )
    finally:
        stop(process)


def run(assignment_id: str, workspace: Path, model: str, timeout: int) -> None:
    """Launch the isolated headless CLI, then publish only its lifecycle result."""
    path = run_path(assignment_id)
    runtime = path / "runtime"
    executable = find_on_path("opencode")
    sandbox = platform_sandbox()
    result = RunResult(state="failed")
    try:
        if (path / "cancel").exists():
            result = RunResult(state="cancelled")
        elif executable and sandbox:
            runtime.mkdir(mode=0o700)
            output_file = runtime / "output.log"
            with output_file.open("wb") as output_handle:
                process = subprocess.Popen(  # noqa: S603 - fixed sandbox launcher, no shell
                    [
                        "/usr/bin/sandbox-exec",
                        "-p",
                        sandbox_profile(runtime),
                        executable,
                        "--standalone",
                        "run",
                        "--auto",
                        "--model",
                        model,
                        PROMPT,
                    ],
                    cwd=workspace,
                    env=prepare_environment(workspace, runtime, model),
                    stdin=subprocess.DEVNULL,
                    stdout=output_handle,
                    stderr=subprocess.DEVNULL,
                    start_new_session=True,
                )
                result = monitor(process, path, timeout, output_file)
    except OSError:
        result = RunResult(state="failed")
    finally:
        save(path, result)
        if runtime.exists():
            shutil.rmtree(runtime)


@MCP.tool()
def start(
    assignment_id: AssignmentId,
    workspace: str,
    bridge_home: str,
    join_code: str,
    model: str,
    timeout_seconds: Timeout,
    token_cap: TokenCap | None = None,
    cost_cap_microusd: CostCap | None = None,
) -> RunResult:
    """Reserve this stable ID once and detach an independent OpenCode monitor."""
    request = Start(
        assignment_id=assignment_id,
        workspace=workspace,
        bridge_home=bridge_home,
        join_code=join_code,
        model=model,
        timeout_seconds=timeout_seconds,
        token_cap=token_cap,
        cost_cap_microusd=cost_cap_microusd,
    )
    checkout = Path(request.workspace).resolve()
    root = data_dir()
    if (
        not checkout.is_dir()
        or root.is_relative_to(checkout)
        or Path(request.bridge_home) != root
        or not re.fullmatch(r"zai(?:-coding-plan)?/glm-[A-Za-z0-9._-]+", request.model)
        or request.token_cap is not None
        or request.cost_cap_microusd is not None
        or not platform_sandbox()
        or not os.getenv("ZAI_API_KEY")
        or not find_on_path("opencode")
    ):
        return RunResult(state="failed")
    return detach(request, checkout)


def detach(request: Start, checkout: Path) -> RunResult:
    """Reserve an on-disk ID before starting the monitor to protect against replay."""
    path = run_path(request.assignment_id)
    path.parent.mkdir(mode=0o700, exist_ok=True)
    try:
        path.mkdir(mode=0o700)
    except FileExistsError:
        return inspect(request.assignment_id)
    result = RunResult(state="starting")
    save(path, result)
    try:
        # nosemgrep: dangerous-subprocess-use-audit
        subprocess.Popen(  # noqa: S603 - current interpreter in isolated mode, no shell
            [
                sys.executable,
                "-I",
                __file__,
                "--monitor",
                request.assignment_id,
                str(checkout),
                request.model,
                str(request.timeout_seconds),
            ],
            env={**os.environ, JOIN_VARIABLE: request.join_code},
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            start_new_session=True,
        )
    except OSError:
        result = RunResult(state="failed")
        save(path, result)
    return result


if __name__ == "__main__":
    os.umask(0o077)
    if len(sys.argv) == 6 and sys.argv[1] == "--monitor":
        run(sys.argv[2], Path(sys.argv[3]), sys.argv[4], int(sys.argv[5]))
    else:
        MCP.run(transport="stdio")

"""Serve controlled provider outcomes through the same MCP contract as a real plugin."""

import os
from pathlib import Path

from mcp.server.fastmcp import FastMCP

from agents_chat.mcp.plugin_contract import (
    AssignmentId,
    Capabilities,
    CostCap,
    RunResult,
    Timeout,
    TokenCap,
)
from agents_chat.storage.database import data_dir

MCP = FastMCP("fake-fresh-runs")


def marker(assignment_id: str) -> Path:
    """Store a harmless marker so a new MCP process can inspect a prior launch."""
    return data_dir() / assignment_id


@MCP.tool()
def capabilities() -> Capabilities:
    """Declare supported contract version."""
    return Capabilities(version=1)


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
    """Start once or simulate a provider failure or uncertain result."""
    if os.getenv("TEST_FRESH_RESULT") == "failed":
        return RunResult(state="failed", exit_code=1)
    if os.getenv("TEST_FRESH_RESULT") == "unknown":
        raise RuntimeError("provider interrupted")
    if not marker(assignment_id).exists():
        marker(assignment_id).write_text("running", encoding="utf-8")
    return inspect(assignment_id)


@MCP.tool()
def inspect(assignment_id: AssignmentId) -> RunResult:
    """Inspect a previously reported ID without starting a new process."""
    path = marker(assignment_id)
    if not path.is_file():
        return RunResult(state="unknown")
    return RunResult(state=path.read_text(encoding="utf-8"))


@MCP.tool()
def cancel(assignment_id: AssignmentId) -> RunResult:
    """Cancel a launched assignment and remember the result across sessions."""
    path = marker(assignment_id)
    if path.is_file():
        path.write_text("cancelled", encoding="utf-8")
    return inspect(assignment_id)


if __name__ == "__main__":
    MCP.run(transport="stdio")

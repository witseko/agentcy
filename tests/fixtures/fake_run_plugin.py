"""Serve controlled provider outcomes through the same MCP contract as a real plugin."""

import os
from pathlib import Path
from typing import Annotated, Literal

from mcp.server.mcpserver import MCPServer
from pydantic import BaseModel, ConfigDict, Field, StringConstraints

# Version one of the fresh-run plugin contract (protocol/fresh-runs.v1.schema.json).
AssignmentId = Annotated[str, StringConstraints(pattern=r"^[a-f0-9]{32}$")]
RunState = Literal["starting", "running", "finished", "failed", "cancelled", "timed_out", "unknown"]
Timeout = Annotated[int, Field(ge=1, le=3600)]
TokenCap = Annotated[int, Field(gt=0, le=100_000_000)]
CostCap = Annotated[int, Field(gt=0, le=100_000_000)]
TokenUsage = Annotated[int, Field(ge=0, le=1_000_000_000)]

MCP = MCPServer("fake-fresh-runs")


class ContractModel(BaseModel):
    """Validate cross-process messages without including their contents in errors."""

    model_config = ConfigDict(extra="forbid", hide_input_in_errors=True)


class Capabilities(ContractModel):
    """Declare the version and optional hard limits a plugin enforces."""

    version: Literal[1]
    token_cap: bool = False
    cost_cap: bool = False


class RunResult(ContractModel):
    """Report a provider state without returning native identifiers or content."""

    state: RunState
    exit_code: int | None = None
    input_tokens: TokenUsage | None = None
    output_tokens: TokenUsage | None = None


def data_dir() -> Path:
    """Return the private bridge data directory that the core passes in AGENTCY_HOME."""
    path = Path(os.getenv("AGENTCY_HOME", Path.home() / ".agentcy"))
    path.mkdir(mode=0o700, parents=True, exist_ok=True)
    if path.is_symlink() or path.stat().st_uid != os.getuid() or path.stat().st_mode & 0o077:
        raise ValueError("Bridge data directory must belong to you and have mode 0700")
    return path.resolve()


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

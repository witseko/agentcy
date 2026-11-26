"""A fresh-run plugin for video takes: joins the launched slot as a stand-in chat.

It answers the fresh-run plugin contract (protocol/fresh-runs.v1.schema.json),
so the bridge's launch states are real, but in place of starting a provider
it runs ``stand_in.py join`` with the slot's join code. The steps can then
post that worker's cues. record.zsh registers it in the take's private
plugins.json and passes AGENTCY_VIDEO_SCENE and AGENTCY_VIDEO_STATE.
"""

import os
import subprocess
import sys
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

MCP = MCPServer("stand-in-fresh-runs")
STAND_IN = Path(__file__).resolve().with_name("stand_in.py")


class Capabilities(BaseModel):
    """Declare the contract version the plugin answers."""

    model_config = ConfigDict(extra="forbid", hide_input_in_errors=True)
    version: Literal[1]


class RunResult(BaseModel):
    """Report a run state without native identifiers or content."""

    model_config = ConfigDict(extra="forbid", hide_input_in_errors=True)
    state: RunState


def data_dir() -> Path:
    """Return the private bridge data directory that the core passes in AGENTCY_HOME."""
    path = Path(os.getenv("AGENTCY_HOME", Path.home() / ".agentcy"))
    path.mkdir(mode=0o700, parents=True, exist_ok=True)
    if path.is_symlink() or path.stat().st_uid != os.getuid() or path.stat().st_mode & 0o077:
        raise ValueError("Bridge data directory must belong to you and have mode 0700")
    return path.resolve()


def marker(assignment_id: str) -> Path:
    """Keep each run's state in the take's bridge folder, so a new plugin process can read it."""
    return data_dir() / f"stand-in-run-{assignment_id}"


@MCP.tool()
def capabilities() -> Capabilities:
    """Declare the supported contract version."""
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
    """Join the slot once as a stand-in; repeated calls report the existing run."""
    path = marker(assignment_id)
    if not path.exists():
        path.write_text("running", encoding="utf-8")
        command = [
            sys.executable, str(STAND_IN), "--workspace", workspace,
            "--scene", os.environ["AGENTCY_VIDEO_SCENE"], "--state", os.environ["AGENTCY_VIDEO_STATE"],
            "join", "plugin-worker", join_code,
        ]
        # A fixed argument list from the bridge's own values; the code goes to a private process.
        subprocess.Popen(command, start_new_session=True, stdin=subprocess.DEVNULL)  # noqa: S603
    return inspect(assignment_id)


@MCP.tool()
def inspect(assignment_id: AssignmentId) -> RunResult:
    """Report the run state without starting anything."""
    path = marker(assignment_id)
    if not path.is_file():
        return RunResult(state="unknown")
    return RunResult(state=path.read_text(encoding="utf-8"))


@MCP.tool()
def cancel(assignment_id: AssignmentId) -> RunResult:
    """Mark the run cancelled; the stand-in has already joined and needs no stop."""
    path = marker(assignment_id)
    if path.is_file():
        path.write_text("cancelled", encoding="utf-8")
    return inspect(assignment_id)


if __name__ == "__main__":
    MCP.run()

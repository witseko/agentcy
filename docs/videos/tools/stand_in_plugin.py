"""A fresh-run plugin for video takes: joins the launched slot as a stand-in chat.

It answers the same MCP contract as the Z.ai and Gemini plugins, so the
bridge's launch states are real, but in place of starting OpenCode or
Gemini it runs ``stand_in.py join`` with the slot's join code. The steps
can then post that worker's cues. record.zsh registers it in the take's
private plugins.json and passes AGENTCY_VIDEO_SCENE and AGENTCY_VIDEO_STATE.
"""

import os
import subprocess
import sys
from pathlib import Path

from mcp.server.mcpserver import MCPServer

from agentcy.mcp.plugin_contract import (
    AssignmentId,
    Capabilities,
    CostCap,
    RunResult,
    Timeout,
    TokenCap,
)
from agentcy.storage.database import data_dir

MCP = MCPServer("stand-in-fresh-runs")
STAND_IN = Path(__file__).resolve().with_name("stand_in.py")


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

"""Send bounded MCP stdio requests to registered fresh-run plugins."""

import asyncio
import os
from pathlib import Path
from typing import TextIO

from mcp import ClientSession, MCPError, StdioServerParameters
from mcp.client.stdio import stdio_client

from agentcy.mcp.plugin_contract import Capabilities, RunResult, Start
from agentcy.mcp.plugin_settings import Plugin
from agentcy.storage.database import data_dir

RPC_TIMEOUT = 5


class PluginError(ValueError):
    """Signal a plugin failure without exposing provider content."""


async def invoke(plugin: Plugin, tool: str, arguments: dict, discard: TextIO) -> dict:
    """Open one plugin session, invoke an operation and validate its response envelope."""
    environment = {name: os.environ[name] for name in plugin.environment if name in os.environ}
    environment["AGENTCY_HOME"] = str(data_dir())
    config = StdioServerParameters(
        command=plugin.command[0], args=plugin.command[1:], env=environment
    )
    async with (
        stdio_client(config, errlog=discard) as streams,
        ClientSession(*streams) as session,
    ):
        await session.initialize()
        response = await session.call_tool(tool, arguments)
        if response.is_error or response.structured_content is None:
            raise PluginError("Fresh-run plugin operation failed")
        return response.structured_content


def call(plugin: Plugin, tool: str, arguments: dict) -> dict:
    """Bound a plugin exchange and map transport failures to a generic error."""
    try:
        with Path(os.devnull).open("w") as discard:
            return asyncio.run(
                asyncio.wait_for(invoke(plugin, tool, arguments, discard), RPC_TIMEOUT)
            )
    except (ExceptionGroup, MCPError, OSError, TimeoutError):
        raise PluginError("Fresh-run plugin communication failed") from None


def check_capabilities(plugin: Plugin) -> None:
    """Check that the configured provider supports the requested hard limits."""
    try:
        capability = Capabilities.model_validate(call(plugin, "capabilities", {}))
    except ValueError:
        raise PluginError("Fresh-run plugin contract is unavailable") from None
    if (plugin.token_cap and not capability.token_cap) or (
        plugin.cost_cap_microusd and not capability.cost_cap
    ):
        raise PluginError("Fresh-run plugin cannot enforce the configured cap")


def run_operation(plugin: Plugin, tool: str, assignment: Start | str) -> RunResult:
    """Start, inspect or cancel a run and parse only its state and exit status."""
    arguments = (
        assignment.model_dump(exclude_none=True)
        if isinstance(assignment, Start)
        else {"assignment_id": assignment}
    )
    try:
        return RunResult.model_validate(call(plugin, tool, arguments))
    except ValueError:
        raise PluginError("Fresh-run plugin state could not be confirmed") from None

"""Build MCP servers whose failed tool calls tell the caller why they failed."""

from typing import Any

from mcp.server.mcpserver import Context, MCPServer
from mcp.server.mcpserver.exceptions import ToolError, UnexpectedToolError
from mcp.types import CallToolResult, InputRequiredResult


class BridgeServer(MCPServer):
    """Show the message of every tool failure to the client, as mcp 1.x did.

    mcp 2 shows only "Error executing tool <name>" when a tool raises anything but
    ToolError. Our tools raise ValueError with the core's rejection text, which the
    calling model needs to correct its call.
    """

    async def call_tool(
        self, name: str, arguments: dict[str, Any], context: Context | None = None
    ) -> CallToolResult | InputRequiredResult:
        """Call a tool and keep the original error message on a failure."""
        try:
            return await super().call_tool(name, arguments, context)
        except UnexpectedToolError as error:
            raise ToolError(f"{error}: {error.__cause__}") from error.__cause__

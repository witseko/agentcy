"""Check the icon advertised by the Agents Chat stdio server."""

import asyncio
import struct
import sys
from base64 import b64decode
from pathlib import Path

from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client
from mcp.types import InitializeResult


async def initialize(workspace: Path) -> InitializeResult:
    """Connect to the normal CLI and return its MCP initialization result."""
    parameters = StdioServerParameters(
        command=sys.executable,
        args=[
            "-c",
            "from agents_chat.tools.cli import main; main()",
            "--workspace",
            str(workspace),
        ],
    )
    async with stdio_client(parameters) as streams, ClientSession(*streams) as session:
        return await asyncio.wait_for(session.initialize(), timeout=10)


def test_stdio_server_advertises_png_icon(tmp_path: Path) -> None:
    """Provide a self-contained 64-pixel icon and base instructions during initialization."""
    result = asyncio.run(initialize(tmp_path))
    server = result.serverInfo
    assert server.name == "agents-chat"
    assert result.instructions is not None
    assert "report brief progress updates to the lead periodically" in result.instructions
    assert server.icons is not None
    (icon,) = server.icons
    assert icon.mimeType == "image/png"
    assert icon.sizes == ["64x64"]
    prefix = "data:image/png;base64,"
    assert icon.src.startswith(prefix)
    png = b64decode(icon.src.removeprefix(prefix), validate=True)
    assert png.startswith(b"\x89PNG\r\n\x1a\n")
    assert struct.unpack_from(">II", png, 16) == (64, 64)
